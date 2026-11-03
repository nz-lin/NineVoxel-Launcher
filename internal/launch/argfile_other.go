//go:build !windows

// 本文件提供非 Windows 平台的参数文件编码。
//
// Linux / macOS 上 JVM 读取参数文件时使用平台默认字符集，实践中为 UTF-8，
// 因此直接按 UTF-8 写入即可。

package launch

// encodeArgFileContent 在非 Windows 平台上按 UTF-8 写入参数文件。
func encodeArgFileContent(text string) ([]byte, error) {
	return []byte(text), nil
}
