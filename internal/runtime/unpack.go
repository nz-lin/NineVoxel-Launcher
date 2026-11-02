// 本文件实现「归档模型」的受限解压器。
//
// 为什么需要它：官方 `java-runtime` manifest 目前是**逐文件 raw 模型**
// （每个文件一条下载记录），没有 zip 归档；但 manifest 结构与下载记录都可能
// 演进出压缩条目（卡面允许文件清单里也点名了「解压事务提交」）。因此这里提供
// 一个可复用的受限解压器，由 install.go 在遇到压缩条目时调用，而逐文件模型的
// 路径校验复用同一套 `unpackTargetPath`。
//
// 防护口径（与卡面第 4 条逐条对应）：
//   - Zip Slip：`..`、绝对路径、盘符、UNC、NUL 一律拒绝；
//   - 符号链接条目：一律跳过并记 warning，不创建链接；
//   - 资源炸弹：总字节上限、单文件上限、文件数上限、压缩比上限；
//   - 越界写入：每个目标路径都经 `artifact.EnsureWithinRoot` 二次校验。
package runtime

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// 解压限额的默认值（卡面第 4 条）。
const (
	// unpackDefaultMaxFiles 是解压文件数上限。
	unpackDefaultMaxFiles = 4000
	// unpackDefaultMaxTotalBytes 是解压总字节上限（1 GiB）。
	unpackDefaultMaxTotalBytes = int64(1 << 30)
	// unpackDefaultMaxFileBytes 是单文件字节上限（512 MiB）。
	unpackDefaultMaxFileBytes = int64(512 << 20)
	// unpackDefaultMaxCompressionRatio 是压缩比上限（100:1）。
	unpackDefaultMaxCompressionRatio = int64(100)
	// unpackCopyBufferSize 是解压拷贝缓冲区大小。
	unpackCopyBufferSize = 32 * 1024
)

// UnpackOptions 是受限解压参数。
//
// 零值不表示「无限制」：未显式给出的字段会取包级默认值，
// 这样调用方少写参数时也不会因为忘记限额而失去保护。
type UnpackOptions struct {
	// MaxFiles 为文件数上限；<=0 时取默认值。
	MaxFiles int
	// MaxTotalBytes 为解压总字节上限；<=0 时取默认值。
	MaxTotalBytes int64
	// MaxFileBytes 为单文件字节上限；<=0 时取默认值。
	MaxFileBytes int64
	// MaxCompressionRatio 为压缩比上限；<=0 时取默认值。
	MaxCompressionRatio int64
	// Executable 返回某个相对路径是否需要可执行位；nil 表示都不设置。
	Executable func(relPath string) bool
}

// UnpackReport 是一次解压的结果摘要。
type UnpackReport struct {
	// Files 为写出的普通文件数。
	Files int
	// Directories 为创建的目录数。
	Directories int
	// SkippedLinks 为被跳过的符号链接条目数。
	SkippedLinks int
	// TotalBytes 为写出的总字节数。
	TotalBytes int64
	// Warnings 为需要提示给用户的告警。
	Warnings []RuntimeProbeWarning
}

// normalized 返回补齐默认值后的选项。
func (o UnpackOptions) normalized() UnpackOptions {
	if o.MaxFiles <= 0 {
		o.MaxFiles = unpackDefaultMaxFiles
	}
	if o.MaxTotalBytes <= 0 {
		o.MaxTotalBytes = unpackDefaultMaxTotalBytes
	}
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = unpackDefaultMaxFileBytes
	}
	if o.MaxCompressionRatio <= 0 {
		o.MaxCompressionRatio = unpackDefaultMaxCompressionRatio
	}
	return o
}

// UnpackZip 把 zip 归档解压到 destDir，并施加全部路径与资源防护。
//
// destDir 可以不存在（会自动创建），但必须是绝对路径——相对路径会让
// `EnsureWithinRoot` 的边界判定失去意义。
func UnpackZip(zipPath, destDir string, options UnpackOptions) (UnpackReport, *AppError) {
	report := UnpackReport{Warnings: make([]RuntimeProbeWarning, 0)}
	opts := options.normalized()

	targetRoot := strings.TrimSpace(destDir)
	if targetRoot == "" || !filepath.IsAbs(targetRoot) {
		return report, newInputError(ErrorCodeRuntimeUnpack, messageUnpackTargetInvalid)
	}
	if mkdirErr := os.MkdirAll(targetRoot, 0o755); mkdirErr != nil {
		return report, newSystemError(ErrorCodeRuntimeUnpack, messageUnpackTargetInvalid, false)
	}

	reader, openErr := zip.OpenReader(zipPath)
	if openErr != nil {
		return report, newExternalError(ErrorCodeRuntimeUnpack, messageUnpackArchiveUnreadable, false)
	}
	defer func() {
		_ = reader.Close()
	}()

	if len(reader.File) > opts.MaxFiles {
		return report, newInputError(ErrorCodeRuntimeUnpack, messageUnpackTooManyFiles)
	}

	for _, entry := range reader.File {
		relPath, pathErr := unpackEntryPath(entry.Name)
		if pathErr != nil {
			return report, pathErr
		}
		target, targetErr := unpackTargetPath(targetRoot, relPath)
		if targetErr != nil {
			return report, targetErr
		}

		// 符号链接条目一律跳过：归档里的链接可以指向归档外的任意路径，
		// 在解压阶段创建它就等于把「越界写」延后到第一次读取时爆发。
		if entry.Mode()&os.ModeSymlink != 0 {
			report.SkippedLinks++
			report.Warnings = append(report.Warnings, RuntimeProbeWarning{
				Code:    WarningUnpackSkippedLink,
				Message: messageUnpackSkippedLink(relPath),
			})
			continue
		}

		if entry.FileInfo().IsDir() {
			if mkdirErr := os.MkdirAll(target, 0o755); mkdirErr != nil {
				return report, newSystemError(ErrorCodeRuntimeUnpack, messageUnpackWriteFailed, false)
			}
			report.Directories++
			continue
		}

		if exceedErr := checkEntryLimits(entry, opts); exceedErr != nil {
			return report, exceedErr
		}
		if err := writeEntry(entry, target, opts); err != nil {
			return report, err
		}
		report.Files++
		report.TotalBytes += int64(entry.UncompressedSize64)
		if report.TotalBytes > opts.MaxTotalBytes {
			return report, newInputError(ErrorCodeRuntimeUnpack, messageUnpackTooLarge)
		}
		if opts.Executable != nil && opts.Executable(relPath) {
			_ = os.Chmod(target, 0o755)
		}
	}
	return report, nil
}

// checkEntryLimits 校验单个条目的资源限额。
func checkEntryLimits(entry *zip.File, opts UnpackOptions) *AppError {
	size := int64(entry.UncompressedSize64)
	if size < 0 {
		return newInputError(ErrorCodeRuntimeUnpack, messageUnpackEntryInvalid(entry.Name))
	}
	if size > opts.MaxFileBytes {
		return newInputError(ErrorCodeRuntimeUnpack, messageUnpackFileTooLarge(entry.Name))
	}
	compressed := int64(entry.CompressedSize64)
	if compressed > 0 && size > 0 {
		ratio := size / compressed
		if ratio > opts.MaxCompressionRatio {
			return newInputError(ErrorCodeRuntimeUnpack, messageUnpackRatioExceeded(entry.Name))
		}
	}
	return nil
}

// writeEntry 把一个文件条目写入 target。
func writeEntry(entry *zip.File, target string, opts UnpackOptions) *AppError {
	if mkdirErr := os.MkdirAll(filepath.Dir(target), 0o755); mkdirErr != nil {
		return newSystemError(ErrorCodeRuntimeUnpack, messageUnpackWriteFailed, false)
	}
	source, openErr := entry.Open()
	if openErr != nil {
		return newExternalError(ErrorCodeRuntimeUnpack, messageUnpackArchiveUnreadable, false)
	}
	defer func() {
		_ = source.Close()
	}()

	destination, createErr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if createErr != nil {
		return newSystemError(ErrorCodeRuntimeUnpack, messageUnpackWriteFailed, false)
	}
	buffer := make([]byte, unpackCopyBufferSize)
	written, copyErr := io.CopyBuffer(destination, io.LimitReader(source, opts.MaxFileBytes+1), buffer)
	syncErr := destination.Sync()
	closeErr := destination.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		return newSystemError(ErrorCodeRuntimeUnpack, messageUnpackWriteFailed, false)
	}
	if written > opts.MaxFileBytes {
		_ = os.Remove(target)
		return newInputError(ErrorCodeRuntimeUnpack, messageUnpackFileTooLarge(entry.Name))
	}
	return nil
}

// unpackEntryPath 归一化并校验归档条目名。
func unpackEntryPath(name string) (string, *AppError) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(name), "/")
	if trimmed == "" {
		return "", newInputError(ErrorCodeRuntimeUnpack, messageUnpackEntryInvalid(name))
	}
	cleaned, cleanErr := artifact.ValidateRelativePath(trimmed)
	if cleanErr != nil {
		return "", newInputError(ErrorCodeRuntimeUnpack, messageUnpackEntryInvalid(name))
	}
	return cleaned, nil
}

// unpackTargetPath 把相对路径安全地解析到 root 之内。
//
// 逐文件 raw 模型与归档模型共用本函数：只要写盘，就必须过这里。
func unpackTargetPath(root, relPath string) (string, *AppError) {
	cleaned, cleanErr := artifact.ValidateRelativePath(relPath)
	if cleanErr != nil {
		return "", newInputError(ErrorCodeRuntimeUnpack, messageUnpackEntryInvalid(relPath))
	}
	target := filepath.Join(root, filepath.FromSlash(cleaned))
	if withinErr := artifact.EnsureWithinRoot(root, target); withinErr != nil {
		return "", newInputError(ErrorCodeRuntimeUnpack, messageUnpackEntryInvalid(relPath))
	}
	return target, nil
}
