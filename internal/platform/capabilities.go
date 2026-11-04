// Package platform 提供跨平台能力接口与编译期实现。
//
// 设计目标：调用方只依赖 Capabilities 的能力接口，具体实现由构建标签在
// 编译期选定（Windows / macOS / Linux），不引入运行时插件或 cgo 之外的动态加载。
package platform

import "os/exec"

// DirectoryOpener 打开系统文件管理器中的目录。
type DirectoryOpener interface {
	OpenDirectory(path string) error
}

// MemoryProbe 返回本机物理内存总量（字节）。
type MemoryProbe interface {
	TotalBytes() uint64
}

// ThemeProbe 返回系统当前是否偏好深色外观。
type ThemeProbe interface {
	PrefersDark() bool
}

// ArgFileCodec 把 Java 参数文件文本编码为平台要求的字符集。
type ArgFileCodec interface {
	Encode(text string) ([]byte, error)
}

// ProcessDecorator 按平台修饰子进程启动参数。
type ProcessDecorator interface {
	HideWindow(cmd *exec.Cmd)
}

// DiskSpaceProbe 返回指定路径所在卷的可用字节数。
type DiskSpaceProbe interface {
	AvailableBytes(path string) (int64, error)
}

// Capabilities 是一组编译期选定的平台能力实现。
type Capabilities struct {
	Directory DirectoryOpener
	Memory    MemoryProbe
	Theme     ThemeProbe
	ArgFile   ArgFileCodec
	Process   ProcessDecorator
	Disk      DiskSpaceProbe
}

// Current 是当前平台的默认能力集合。
//
// 各字段的底层函数由构建标签在编译期选择；测试可以整体替换 Current。
var Current = Capabilities{
	Directory: directoryCapability{},
	Memory:    memoryCapability{},
	Theme:     themeCapability{},
	ArgFile:   argFileCapability{},
	Process:   processCapability{},
	Disk:      diskCapability{},
}

type directoryCapability struct{}

func (directoryCapability) OpenDirectory(path string) error { return OpenDirectory(path) }

type memoryCapability struct{}

func (memoryCapability) TotalBytes() uint64 { return TotalMemoryBytes() }

type themeCapability struct{}

func (themeCapability) PrefersDark() bool { return SystemPrefersDark() }

type argFileCapability struct{}

func (argFileCapability) Encode(text string) ([]byte, error) { return encodeArgFileContent(text) }

type processCapability struct{}

func (processCapability) HideWindow(cmd *exec.Cmd) { HideWindow(cmd) }

type diskCapability struct{}

func (diskCapability) AvailableBytes(path string) (int64, error) { return availableBytes(path) }
