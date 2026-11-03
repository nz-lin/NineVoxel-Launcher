//go:build windows

// 本文件提供 Windows 上的参数文件编码：必须按系统 ANSI 代码页写入。
//
// 该约束不是可选项，也不能按 Java 版本区分：已在 Java 17 / 21 / 25 上实测，
// 参数文件始终按系统默认字符集（`sun.jnu.encoding`，即 ANSI 代码页）读取。
// 简体中文 Windows 的代码页是 GBK，若按 UTF-8 写参数文件，含中文的游戏路径
// 在 JVM 眼里就是乱码，游戏随即以「找不到主类」报错，排查成本极高。

package launch

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// cpACP 是 Windows 的 ANSI 代码页标识（等价于 `GetACP` 的返回值）。
//
// 用 `CP_ACP` 而不是写死 936：用户的代码页可能是 950、932、1252，
// 甚至开启了「使用 Unicode UTF-8」测试选项而变成 65001。
const cpACP = 0

// procWideCharToMultiByte 是 kernel32!WideCharToMultiByte 的延迟绑定句柄。
//
// x/sys/windows 只导出了反向的 `MultiByteToWideChar`，正向转换需自行绑定；
// 用 `NewLazySystemDLL` 保证只从 System32 加载，避免 DLL 劫持。
var procWideCharToMultiByte = windows.NewLazySystemDLL("kernel32.dll").NewProc("WideCharToMultiByte")

// encodeArgFileContent 把参数文件文本编码为系统 ANSI 代码页。
//
// 返回错误表示文本含系统代码页无法表示的字符，调用方应给出可读提示，
// 而不是让 JVM 读到一个被替换成 `?` 的错误路径。
func encodeArgFileContent(text string) ([]byte, error) {
	if text == "" {
		return []byte{}, nil
	}
	return encodeWithSystemCodePage(text)
}

// encodeWithSystemCodePage 把文本转换为系统 ANSI 代码页编码。
//
// 转换后会用 `MultiByteToWideChar` 反解一次并逐字符比对：代码页无法表示的
// 字符（罕见字符、emoji）会被静默替换成 `?`，不检测就会把错误路径交给 JVM。
func encodeWithSystemCodePage(text string) ([]byte, error) {
	utf16, convertErr := windows.UTF16FromString(text)
	if convertErr != nil {
		return nil, convertErr
	}
	sourceLength := len(utf16) - 1

	needed, sizeErr := wideCharToMultiByte(&utf16[0], sourceLength, nil, 0)
	if sizeErr != nil {
		return nil, sizeErr
	}

	encoded := make([]byte, needed)
	written, writeErr := wideCharToMultiByte(&utf16[0], sourceLength, &encoded[0], needed)
	if writeErr != nil {
		return nil, writeErr
	}

	// 反解验证：确认没有任何字符被替换成默认字符。
	decodedLength, decodeErr := windows.MultiByteToWideChar(cpACP, 0, &encoded[0], int32(written), nil, 0)
	if decodeErr != nil {
		return nil, decodeErr
	}
	decoded := make([]uint16, int(decodedLength))
	if _, roundTripErr := windows.MultiByteToWideChar(cpACP, 0, &encoded[0], int32(written), &decoded[0], decodedLength); roundTripErr != nil {
		return nil, roundTripErr
	}
	if windows.UTF16ToString(decoded) != text {
		return nil, errors.New("参数文本含系统代码页无法表示的字符")
	}

	return encoded, nil
}

// wideCharToMultiByte 是 kernel32!WideCharToMultiByte 的最小封装。
//
// 参数 src 为 UTF-16 源串（不含结尾 NUL，由 sourceLength 给出长度）；
// dst 为 nil 时只查询所需字节数，否则写入 dst 并返回实际写入长度。
func wideCharToMultiByte(src *uint16, sourceLength int, dst *byte, dstLength int) (int, error) {
	var dstPtr uintptr
	if dst != nil {
		dstPtr = uintptr(unsafe.Pointer(dst))
	}

	written, _, callErr := procWideCharToMultiByte.Call(
		uintptr(cpACP),
		0,
		uintptr(unsafe.Pointer(src)),
		uintptr(sourceLength),
		dstPtr,
		uintptr(dstLength),
		0,
		0,
	)
	if written == 0 {
		if callErr == nil || errors.Is(callErr, windows.ERROR_SUCCESS) {
			callErr = errors.New("WideCharToMultiByte 调用失败")
		}
		return 0, callErr
	}
	return int(written), nil
}
