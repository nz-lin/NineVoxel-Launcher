package platform

import (
	"runtime"
	"testing"
)

// TestCurrentCapabilitiesAreWired 验证编译期能力集合不存在空实现。
func TestCurrentCapabilitiesAreWired(t *testing.T) {
	if Current.Directory == nil || Current.Memory == nil || Current.Theme == nil ||
		Current.ArgFile == nil || Current.Process == nil || Current.Disk == nil {
		t.Fatal("平台能力集合存在空字段")
	}
}

// TestMemoryProbeOnSupportedPlatforms 验证三大桌面平台能读到物理内存。
func TestMemoryProbeOnSupportedPlatforms(t *testing.T) {
	switch runtime.GOOS {
	case "windows", "darwin", "linux":
		if bytes := Current.Memory.TotalBytes(); bytes == 0 {
			t.Fatalf("%s 上物理内存探测返回 0", runtime.GOOS)
		}
	default:
		t.Skip("该平台未实现物理内存探测")
	}
}

// TestDiskSpaceProbeOnTempDir 验证磁盘空间能力可用于真实目录。
func TestDiskSpaceProbeOnTempDir(t *testing.T) {
	available, err := Current.Disk.AvailableBytes(t.TempDir())
	if err != nil {
		t.Fatalf("磁盘空间探测失败：%v", err)
	}
	if available <= 0 {
		t.Fatalf("可用空间 = %d，期望大于 0", available)
	}
}

// TestArgFileCodecProducesBytes 验证参数文件编码能力可用。
func TestArgFileCodecProducesBytes(t *testing.T) {
	encoded, err := Current.ArgFile.Encode("--username 玩家")
	if err != nil {
		t.Fatalf("参数文件编码失败：%v", err)
	}
	if len(encoded) == 0 {
		t.Fatal("参数文件编码结果为空")
	}
}
