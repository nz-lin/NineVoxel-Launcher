package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

// ResolveRuntimeExecutable returns the absolute Java executable path for a
// runtime ID. Callers must keep the result in the backend and must not render
// or persist it in a frontend DTO.
func ResolveRuntimeExecutable(runtimeID string) (string, error) {
	id := strings.TrimSpace(runtimeID)
	if id == "" {
		return "", errors.New("未选择 Java 运行时")
	}

	row, findErr := findRuntimeRow(context.Background(), id)
	if findErr != nil {
		return "", errors.New("无法读取 Java 运行时记录")
	}
	if row != nil {
		if row.Kind == SourceDownloaded {
			directory, dirErr := managedRuntimeDir(*row)
			if dirErr != nil {
				return "", errors.New("Java 运行时目录不可用")
			}
			executable := filepath.Join(directory, "bin", javaExecutableName())
			if !isRegularExecutable(executable) && goruntime.GOOS == "darwin" {
				// 兼容修复前安装的 macOS 运行时：旧布局是 <dir>/Contents/Home/bin/java。
				legacy := filepath.Join(directory, "Contents", "Home", "bin", javaExecutableName())
				if isRegularExecutable(legacy) {
					executable = legacy
				}
			}
			if !isRegularExecutable(executable) {
				return "", errors.New("Java 运行时文件已缺失，请重新安装")
			}
			return executable, nil
		}
	}

	// Local runtimes do not store an absolute path in SQLite. Resolve them from
	// the explicitly configured Java candidate while keeping the path internal.
	for _, candidate := range enumerateJavaCandidates() {
		info, probeErr := probeJavaExecutable(candidate.path)
		if probeErr != nil {
			continue
		}
		summary := runtimeSummaryFromProbe(candidate.path, info, nowFunc().UnixMilli())
		if summary.RuntimeID == id {
			return candidate.path, nil
		}
	}
	return "", errors.New("找不到该 Java 运行时，请重新选择")
}

// isRegularExecutable 判断路径存在且为非空普通文件。
func isRegularExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// ResolveSelectedRuntimeExecutable resolves the runtime currently selected in
// RuntimeService. It is used only when no explicit instance/global Java path is
// configured.
func ResolveSelectedRuntimeExecutable() (string, error) {
	id, appErr := selectedRuntimeID(context.Background())
	if appErr != nil {
		return "", errors.New("无法读取已选 Java 运行时")
	}
	if strings.TrimSpace(id) == "" {
		return "", errors.New("尚未选择 Java 运行时")
	}
	return ResolveRuntimeExecutable(id)
}
