package instance

import (
	"os"
	"path/filepath"
	"testing"
)

// writeLease 向核心租约表写入一条记录，模拟「游戏正在运行」。
//
// 直接写文件而不是调用 launch 包的登记接口：实例侧的判定只应依赖持久化的
// 租约表，这样两个包之间没有运行时耦合。
func writeLease(t *testing.T, root, versionID string, pid uint32, startMs int64) {
	t.Helper()
	body := `{"schemaVersion":1,"leases":[{"versionId":"` + versionID +
		`","pid":` + itoa(pid) + `,"processStartMs":` + itoa64(startMs) + `,"acquiredAtMs":1}]}`
	fixtureWrite(t, filepath.Join(root, "core-index", "leases.json"), []byte(body))
}

// itoa 把整数转成十进制字符串（避免为一个测试引入 strconv 之外的工具）。
func itoa(value uint32) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 10)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// itoa64 把 int64 转成十进制字符串。
func itoa64(value int64) string {
	if value < 0 {
		return "0"
	}
	return itoa(uint32(value))
}

// TestRemoveCoreVersionRejectedWhileRunning 验证游戏运行时拒绝删除版本。
//
// 引用检查只看实例配置；运行中的游戏即使实例配置已改掉，仍在从该目录加载
// JAR。少了这一层，「游戏运行中换加载器」会把进程正在用的文件删掉。
func TestRemoveCoreVersionRejectedWhileRunning(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("运行中"))
	if err != nil {
		t.Fatal(err)
	}
	loaderID := "fabric-loader-0.16.14-" + item.MCVersion
	writeCoreVersion(t, s.root, loaderID)

	// 先把实例切到加载器版本，确保「引用检查」通过（没有其他实例引用）。
	updated := item
	updated.Loader = "fabric"
	updated.LoaderVersion = "0.16.14"
	updated.VersionID = loaderID
	updated.InstallationState = "installed"
	if err := s.saveLocked(updated); err != nil {
		t.Fatal(err)
	}

	// 模拟该版本正在运行。
	writeLease(t, s.root, loaderID, 4242, 777000)

	if s.removeCoreVersionIfUnreferenced(loaderID, item.ID) {
		t.Fatal("游戏运行中不应删除该版本")
	}
	if _, statErr := os.Stat(filepath.Join(s.root, "versions", loaderID)); statErr != nil {
		t.Fatalf("版本目录被误删: %v", statErr)
	}
}

// TestRemoveCoreVersionAllowedWhenIdle 验证无租约且无引用时正常删除。
func TestRemoveCoreVersionAllowedWhenIdle(t *testing.T) {
	s := testService(t)
	item, err := s.CreateInstance(testConfig("空闲"))
	if err != nil {
		t.Fatal(err)
	}
	loaderID := "fabric-loader-0.16.14-" + item.MCVersion
	writeCoreVersion(t, s.root, loaderID)

	updated := item
	updated.Loader = "fabric"
	updated.LoaderVersion = "0.16.14"
	updated.VersionID = loaderID
	updated.InstallationState = "installed"
	if err := s.saveLocked(updated); err != nil {
		t.Fatal(err)
	}

	if !s.removeCoreVersionIfUnreferenced(loaderID, item.ID) {
		t.Fatal("无引用且无租约时应删除")
	}
	if _, statErr := os.Stat(filepath.Join(s.root, "versions", loaderID)); !os.IsNotExist(statErr) {
		t.Fatalf("版本目录应已删除，实际 stat: %v", statErr)
	}
}

// TestVersionInUseByRunningGameReadsLeaseTable 验证租约判定读取持久化表。
func TestVersionInUseByRunningGameReadsLeaseTable(t *testing.T) {
	s := testService(t)

	if s.versionInUseByRunningGame("26.3") {
		t.Fatal("没有租约表时不应判定为运行中")
	}

	writeLease(t, s.root, "26.3", 4242, 777000)
	// 租约管理器按数据根缓存，这里重建服务以读到新写入的表。
	reopened := &InstanceService{root: s.root}
	if !reopened.versionInUseByRunningGame("26.3") {
		t.Fatal("租约表里有的版本应判定为运行中")
	}
	if reopened.versionInUseByRunningGame("26.4") {
		t.Fatal("租约表里没有的版本不应判定为运行中")
	}
}
