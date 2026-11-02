package download

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
)

// verifyBufferSize 是流式校验的缓冲区大小。
//
// 取 64 KiB：够大以免频繁 syscall，又不至于在同时校验多个大文件时
// 把内存压得过紧。校验必须流式（卡面第 2 条），任何「整文件读入内存」
// 的做法都会在 1 GiB 级别的客户端 JAR 上直接把进程撑爆。
const verifyBufferSize = 64 * 1024

// VerifyExpectation 是一次校验的期望值。
//
// 哈希字段允许为空：资产以 SHA-1 为寻址键（D-27），而部分旧版本元数据
// 只给 SHA-1；SHA-256 存在时则必须匹配。方案形状由 P3-02 的下发数据决定，
// 本包只按「给了就校验」处理，**不存在跳过校验的开关**。
type VerifyExpectation struct {
	// ExpectedSize 是期望字节数；<= 0 视为未知，校验直接失败。
	ExpectedSize int64

	// ExpectedSHA1 是期望的 SHA-1（小写十六进制），可为空。
	ExpectedSHA1 string

	// ExpectedSHA256 是期望的 SHA-256（小写十六进制），可为空。
	ExpectedSHA256 string
}

// VerifyResult 是一次流式校验的实测结果。
//
// 即使校验失败也会带回实测值：隔离原因（.reason.json）需要记录
// 「期望 vs 实际」，否则事后无法判断是下错文件还是源端给了坏文件。
type VerifyResult struct {
	// Size 是实测字节数。
	Size int64

	// SHA1 是实测 SHA-1（小写十六进制）。
	SHA1 string

	// SHA256 是实测 SHA-256（小写十六进制）。
	SHA256 string
}

// HashFile 流式计算文件的 SHA-1 与 SHA-256。
//
// 用 io.MultiWriter 让一次磁盘读取同时喂两个哈希，避免为了两个摘要
// 把文件读两遍（大文件上是数秒级差异）。返回的 *Error 只表示「读不了
// 这个文件」，不表示「校验不通过」——后者由 VerifyFile 判定。
func HashFile(path string) (VerifyResult, *Error) {
	file, err := os.Open(path)
	if err != nil {
		return VerifyResult{}, newSystemError(ErrorCodeVerify, messageVerifyFileUnreadable, false)
	}
	defer func() {
		_ = file.Close()
	}()

	sha1Hasher := sha1.New()
	sha256Hasher := sha256.New()
	return hashStream(file, sha1Hasher, sha256Hasher)
}

// hashStream 从已打开的文件流式读到末尾，返回字节数与两个摘要。
func hashStream(file *os.File, sha1Hasher hash.Hash, sha256Hasher hash.Hash) (VerifyResult, *Error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return VerifyResult{}, newSystemError(ErrorCodeVerify, messageVerifyFileUnreadable, false)
	}
	buffer := make([]byte, verifyBufferSize)
	writer := io.MultiWriter(sha1Hasher, sha256Hasher)
	var total int64
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			if _, writeErr := writer.Write(buffer[:read]); writeErr != nil {
				return VerifyResult{}, newSystemError(ErrorCodeVerify, messageVerifyFileUnreadable, false)
			}
			total += int64(read)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return VerifyResult{}, newSystemError(ErrorCodeVerify, messageVerifyFileUnreadable, false)
		}
	}
	return VerifyResult{
		Size:   total,
		SHA1:   hex.EncodeToString(sha1Hasher.Sum(nil)),
		SHA256: hex.EncodeToString(sha256Hasher.Sum(nil)),
	}, nil
}

// VerifyFile 校验文件的大小与哈希是否与期望一致。
//
// 三条独立判定，任一不满足都判定失败（卡面第 2、3 条）：
//  1. 实测大小必须等于 ExpectedSize；
//  2. 给了 ExpectedSHA1 就必须匹配；
//  3. 给了 ExpectedSHA256 就必须匹配。
//
// 失败时同时返回实测结果，供隔离原因记录使用。
func VerifyFile(path string, expect VerifyExpectation) (VerifyResult, *Error) {
	result, hashErr := HashFile(path)
	if hashErr != nil {
		return result, hashErr
	}
	if verifyErr := verifyResult(result, expect); verifyErr != nil {
		return result, verifyErr
	}
	return result, nil
}

// verifyResult 是 VerifyFile 的纯判定部分（便于单测直接构造结果）。
func verifyResult(result VerifyResult, expect VerifyExpectation) *Error {
	if expect.ExpectedSize <= 0 {
		return newInputError(ErrorCodeVerify, messageVerifySizeUnknown)
	}
	if result.Size != expect.ExpectedSize {
		return newInputError(ErrorCodeSize, messageVerifySizeMismatch)
	}
	if expected := normalizeExpectedHash(expect.ExpectedSHA1); expected != "" && expected != result.SHA1 {
		return newInputError(ErrorCodeVerify, messageVerifyHashMismatch)
	}
	if expected := normalizeExpectedHash(expect.ExpectedSHA256); expected != "" && expected != result.SHA256 {
		return newInputError(ErrorCodeVerify, messageVerifyHashMismatch)
	}
	return nil
}

// normalizeExpectedHash 归一化期望哈希（去空白、转小写）。
//
// 元数据里偶尔出现大写十六进制或带引号的值，直接比较会把正确文件判成坏文件；
// 实测值始终是小写，因此这里只归一化期望一侧。
func normalizeExpectedHash(value string) string {
	trimmed := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		switch character := value[index]; {
		case character == ' ' || character == '\t' || character == '\n' || character == '\r':
			continue
		case character >= 'A' && character <= 'Z':
			trimmed = append(trimmed, character+('a'-'A'))
		default:
			trimmed = append(trimmed, character)
		}
	}
	return string(trimmed)
}
