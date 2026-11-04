package instance

import (
	"os"
	"path/filepath"
	"testing"
)

// writeCoreVersion 在核心中创建指定原版版本的元数据文件。
func writeCoreVersion(t *testing.T, root, versionID string) {
	t.Helper()
	fixtureWrite(t, filepath.Join(root, "versions", versionID, versionID+".json"), []byte(`{"id":"`+versionID+`"}`))
}

// writeLoaderCoreVersion 在 `loaders/` 下创建加载器版本目录。
func writeLoaderCoreVersion(t *testing.T, root, versionID string) {
	t.Helper()
	fixtureWrite(t, filepath.Join(root, "loaders", versionID, versionID+".json"), []byte(`{"id":"`+versionID+`"}`))
}

// TestUninstallLoaderKeepsVersionReferencedByOthers 验证卸载加载器不会删除
// 其他实例仍在引用的核心版本。
//
// 这是共享核心引入的真实故障：两个实例共用同一个核心版本，其中一个卸载
// 加载器时把版本目录删掉，另一个实例随即无法启动，而故障现象与操作对象
// 完全无关（改的是 A，坏的是 B）。
func TestUninstallLoaderKeepsVersionReferencedByOthers(t *testing.T) {
	s := testService(t)

	// 两个实例引用同一个加载器版本。
	first, err := s.CreateInstance(testConfig("第一个"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateInstance(testConfig("第二个"))
	if err != nil {
		t.Fatal(err)
	}
	loaderVersion := "0.16.14"
	loaderID := "fabric-loader-" + loaderVersion + "-" + first.MCVersion
	writeCoreVersion(t, s.root, first.MCVersion)
	writeLoaderCoreVersion(t, s.root, loaderID)

	for _, item := range []Instance{first, second} {
		updated := item
		updated.Loader = "fabric"
		updated.LoaderVersion = loaderVersion
		updated.VersionID = loaderID
		updated.InstallationState = "installed"
		if err := s.saveLocked(updated); err != nil {
			t.Fatal(err)
		}
	}

	// 只卸载第一个。
	if _, err := s.UninstallLoader(IDRequest{InstanceID: first.ID}); err != nil {
		t.Fatal(err)
	}

	// 第二个仍在引用，核心版本必须保留。
	if _, statErr := os.Stat(filepath.Join(s.root, "loaders", loaderID)); statErr != nil {
		t.Fatalf("被其他实例引用的核心版本被误删: %v", statErr)
	}

	// 第二个实例启动时应当仍然认为已安装。
	list, err := s.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		if item.ID == second.ID && item.InstallationState != "installed" {
			t.Fatalf("仍被引用的实例状态被误改: %+v", item)
		}
	}
}

// TestUninstallLoaderRemovesUnreferencedVersion 验证无人引用时正常清理。
func TestUninstallLoaderRemovesUnreferencedVersion(t *testing.T) {
	s := testService(t)

	item, err := s.CreateInstance(testConfig("唯一引用"))
	if err != nil {
		t.Fatal(err)
	}
	loaderVersion := "0.16.14"
	loaderID := "fabric-loader-" + loaderVersion + "-" + item.MCVersion
	writeCoreVersion(t, s.root, item.MCVersion)
	writeLoaderCoreVersion(t, s.root, loaderID)

	updated := item
	updated.Loader = "fabric"
	updated.LoaderVersion = loaderVersion
	updated.VersionID = loaderID
	updated.InstallationState = "installed"
	if err := s.saveLocked(updated); err != nil {
		t.Fatal(err)
	}

	if _, err := s.UninstallLoader(IDRequest{InstanceID: item.ID}); err != nil {
		t.Fatal(err)
	}

	if _, statErr := os.Stat(filepath.Join(s.root, "loaders", loaderID)); !os.IsNotExist(statErr) {
		t.Fatalf("无人引用的核心版本应被清理，实际 stat: %v", statErr)
	}
	// 原版版本必须保留：卸载加载器不等于删除游戏。
	if _, statErr := os.Stat(filepath.Join(s.root, "versions", item.MCVersion)); statErr != nil {
		t.Fatalf("原版版本被误删: %v", statErr)
	}
}

// TestListInstancesReportsMissingCoreVersion 验证核心版本缺失时列表如实反映。
//
// 清单里仍写着 installed，但核心目录已不存在（例如被其他实例误删）。
// 此时必须显示为未安装，否则用户看到「已安装」却启动失败，无从判断原因。
func TestListInstancesReportsMissingCoreVersion(t *testing.T) {
	s := testService(t)

	item, err := s.CreateInstance(testConfig("核心丢失"))
	if err != nil {
		t.Fatal(err)
	}
	// 标记为已安装，但不创建任何核心版本目录。
	item.InstallationState = "installed"
	item.InstallationProgress = 1
	item.VersionID = item.MCVersion
	if err := s.saveLocked(item); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("实例数量 = %d", len(list.Items))
	}
	if list.Items[0].InstallationState != "not_installed" {
		t.Fatalf("核心版本缺失时应显示未安装，实际 %+v", list.Items[0])
	}

	// 补上核心版本后应恢复为已安装。
	writeCoreVersion(t, s.root, item.MCVersion)
	list, err = s.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if list.Items[0].InstallationState != "installed" {
		t.Fatalf("核心版本恢复后应显示已安装，实际 %+v", list.Items[0])
	}
}

// TestListInstancesDoesNotOverrideFailedState 验证失败状态不被覆盖。
//
// 只有「清单说已安装但核心缺失」这一种情况需要修正；失败与安装中状态
// 由各自流程维护，不能被列表查询抹掉。
func TestListInstancesDoesNotOverrideFailedState(t *testing.T) {
	s := testService(t)

	item, err := s.CreateInstance(testConfig("失败态"))
	if err != nil {
		t.Fatal(err)
	}
	item.InstallationState = "failed"
	item.InstallationError = "network fixture failed"
	if err := s.saveLocked(item); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListInstances()
	if err != nil {
		t.Fatal(err)
	}
	if list.Items[0].InstallationState != "failed" || list.Items[0].InstallationError == "" {
		t.Fatalf("失败状态被覆盖: %+v", list.Items[0])
	}
}
