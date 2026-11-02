//go:build windows

package platform

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// kernel32GlobalMemoryStatusEx 是 GlobalMemoryStatusEx 的延迟绑定入口。
//
// 用 NewLazySystemDLL 而不是 syscall.NewLazyDLL：前者只从 System32 解析
// 系统 DLL，不受当前工作目录或 PATH 中同名文件影响（DLL 劫持防线）。
// x/sys/windows 未导出该 API 的封装，因此这里自己按 Win32 签名调用。
var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx 对应 Win32 的 MEMORYSTATUSEX。
//
// 字段顺序与类型必须与 C 结构逐字节一致：前两个是 DWORD（4 字节），
// 其后七个是 DWORDLONG（8 字节）。dwLength 必须由调用方先填入结构体大小，
// 否则 API 直接返回失败。
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// TotalMemoryBytes 返回本机物理内存总量（字节）。
//
// 存在的理由：界面上「全局默认最大分配内存」需要一个真实上限。写死一个数字
// 在两个方向都会骗人——8 GB 的机器上放开到 32 GB 会让用户分配出远超物理内存
// 的堆（触发换页、帧率崩塌），而 64 GB 的机器上又显得过窄。
//
// 读取方式为 GlobalMemoryStatusEx：纯查询，不分配内存、不改系统状态、不需要
// 任何特权。进程生命周期内该值不会变化，调用方可以缓存。
//
// 失败时返回 0（未知）。调用方**必须**按「未知」处理并回退到保守默认值，
// 不能把 0 当成「物理内存为 0」。
func TotalMemoryBytes() uint64 {
	var status memoryStatusEx
	status.Length = uint32(unsafe.Sizeof(status))
	ok, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if ok == 0 {
		return 0
	}
	return status.TotalPhys
}
