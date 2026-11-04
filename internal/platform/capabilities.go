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

// ProcessProbe 查询进程的身份信息，用于判定租约是否残留。
//
// 为什么需要它：启动器崩溃后，磁盘上的运行时租约不会被主动清除。恢复时必须
// 判断「登记租约时那个进程是否还活着」。**不能只看 PID**——操作系统会复用
// PID，必须结合进程启动时间比对。
type ProcessProbe interface {
	// ProcessState 返回进程状态与启动时间（UTC 毫秒）。
	//
	// 第二个返回值为启动时间，仅在状态为 ProcessAlive 时有效。
	// 无法读取进程信息时返回 ProcessUnknown，调用方应保守处理。
	ProcessState(pid uint32) (ProcessState, int64)
}

// ProcessState 是进程存活判定结果。
type ProcessState int

const (
	// ProcessUnknown 表示无法判定（权限不足、平台不支持）。
	ProcessUnknown ProcessState = iota
	// ProcessGone 表示进程不存在。
	ProcessGone
	// ProcessAlive 表示进程存在。
	ProcessAlive
)

// Capabilities 是一组编译期选定的平台能力实现。
type Capabilities struct {
	Directory DirectoryOpener
	Memory    MemoryProbe
	Theme     ThemeProbe
	ArgFile   ArgFileCodec
	Process   ProcessDecorator
	Identity  ProcessProbe
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
	Identity:  identityCapability{},
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

type identityCapability struct{}

func (identityCapability) ProcessState(pid uint32) (ProcessState, int64) { return processState(pid) }