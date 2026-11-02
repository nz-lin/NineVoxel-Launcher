// 本文件验证 P2-13 的启动配置持久化（internal/launch/profile.go）。
//
// 测试边界（任务卡第 63 条）：
//   - 全部用 t.TempDir() 下的临时 SQLite 数据库，**不触碰真实用户数据目录**；
//   - 不联网、不启动任何子进程、不启动 Minecraft；
//   - 覆盖：首次插入、重复保存走更新、最近一条、失效配置、删除幂等、
//     空值与相对路径拒绝、迁移版本记录、对外摘要不含绝对路径。
package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/ninevoxel/launcher/internal/settings"
)

// setupProfileTestDB 把启动配置库指向当前用例的临时目录。
//
// 做法：替换包级路径解析函数 + 重置连接状态，用例结束后恢复默认解析，
// 保证用例之间互不影响，也不会写入真实 app.db。
func setupProfileTestDB(t *testing.T) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "app.db")
	originalPathFunc := profileDatabasePath
	profileDatabasePath = func() settings.DatabasePathStatus {
		return settings.DatabasePathStatus{Path: dbPath, Kind: settings.PathKindUserConfig}
	}
	resetProfileDatabaseForTest()

	t.Cleanup(func() {
		resetProfileDatabaseForTest()
		profileDatabasePath = originalPathFunc
	})
}

// newFakeJava 在临时目录里造一个假的 Java 可执行文件并返回其绝对路径。
//
// 只用于验证「文件存在」这一层校验，不会被真正执行。
func newFakeJava(t *testing.T, root string, fileName string) string {
	t.Helper()

	javaPath := filepath.Join(root, "jdk-21", "bin", fileName)
	if err := os.MkdirAll(filepath.Dir(javaPath), 0o755); err != nil {
		t.Fatalf("创建假 Java 目录失败: %v", err)
	}
	if err := os.WriteFile(javaPath, []byte("fake-java"), 0o644); err != nil {
		t.Fatalf("写入假 Java 文件失败: %v", err)
	}
	return javaPath
}

// newFakeGameDirectory 在临时目录里造一个假的游戏目录并返回其绝对路径。
func newFakeGameDirectory(t *testing.T, root string) string {
	t.Helper()

	gameDir := filepath.Join(root, ".minecraft")
	if err := os.MkdirAll(filepath.Join(gameDir, "versions", "1.20.1"), 0o755); err != nil {
		t.Fatalf("创建假游戏目录失败: %v", err)
	}
	return gameDir
}

// countProfileRows 直接查询启动配置表行数，用于验证「重复保存不产生第二行」。
func countProfileRows(t *testing.T) int {
	t.Helper()

	db, dbErr := profileDatabase()
	if dbErr != nil {
		t.Fatalf("打开启动配置库失败: %v", dbErr)
	}
	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM launch_profiles").Scan(&total); err != nil {
		t.Fatalf("统计启动配置行数失败: %v", err)
	}
	return total
}

// TestSaveLaunchProfileInsertsRow 验证首次保存写入一行，并返回脱敏摘要。
func TestSaveLaunchProfileInsertsRow(t *testing.T) {
	setupProfileTestDB(t)

	root := t.TempDir()
	javaPath := newFakeJava(t, root, "java.exe")
	gameDir := newFakeGameDirectory(t, root)

	summary, saveErr := SaveLaunchProfile(SaveLaunchProfileRequest{
		JavaPath:      javaPath,
		GameDirectory: gameDir,
		VersionID:     "1.20.1",
	})
	if saveErr != nil {
		t.Fatalf("保存启动配置失败: %v", saveErr)
	}
	if summary == nil {
		t.Fatal("保存启动配置返回了空摘要")
	}
	if summary.ID == "" {
		t.Error("配置 id 不应为空")
	}
	if summary.VersionID != "1.20.1" {
		t.Errorf("版本 id 不符：期望 1.20.1，实际 %s", summary.VersionID)
	}
	if summary.JavaDisplay == "" || summary.GameRootDisplay == "" {
		t.Error("展示串不应为空")
	}
	if summary.LastUsedAtMs <= 0 {
		t.Errorf("最近使用时间应为正数，实际 %d", summary.LastUsedAtMs)
	}
	if got := countProfileRows(t); got != 1 {
		t.Errorf("首次保存后应只有 1 行，实际 %d 行", got)
	}
}

// TestLaunchProfileSummaryHasNoAbsolutePath 验证对外摘要不含绝对路径。
//
// 这是 P2-13 第 55 条的安全硬约束：绝对路径可以入库，但永不返回前端。
// 临时目录在 Windows 上形如 C:\Users\<用户名>\AppData\Local\Temp\...，
// 因此可以直接断言摘要里既没有盘符、也没有用户名字段。
func TestLaunchProfileSummaryHasNoAbsolutePath(t *testing.T) {
	setupProfileTestDB(t)

	root := t.TempDir()
	javaPath := newFakeJava(t, root, "java.exe")
	gameDir := newFakeGameDirectory(t, root)

	summary, saveErr := SaveLaunchProfile(SaveLaunchProfileRequest{
		JavaPath:      javaPath,
		GameDirectory: gameDir,
		VersionID:     "1.20.1",
	})
	if saveErr != nil {
		t.Fatalf("保存启动配置失败: %v", saveErr)
	}

	// 临时目录的绝对路径本身就是「不该出现在展示串里的东西」。
	forbidden := []string{root, filepath.Dir(root)}
	if volume := filepath.VolumeName(root); volume != "" {
		forbidden = append(forbidden, volume)
	}
	for _, needle := range forbidden {
		if needle == "" {
			continue
		}
		if strings.Contains(summary.JavaDisplay, needle) {
			t.Errorf("Java 展示串泄露了绝对路径片段 %q：%s", needle, summary.JavaDisplay)
		}
		if strings.Contains(summary.GameRootDisplay, needle) {
			t.Errorf("目录展示串泄露了绝对路径片段 %q：%s", needle, summary.GameRootDisplay)
		}
	}

	// 展示串本身要有可辨识的末段，否则脱敏过度等于没有展示价值。
	if !strings.Contains(strings.ToLower(summary.JavaDisplay), "java.exe") {
		t.Errorf("Java 展示串应保留可辨识的文件名，实际 %s", summary.JavaDisplay)
	}
}

// TestSaveLaunchProfileUpdatesExistingRow 验证同一组启动条件重复保存只更新一行。
func TestSaveLaunchProfileUpdatesExistingRow(t *testing.T) {
	setupProfileTestDB(t)

	root := t.TempDir()
	javaPath := newFakeJava(t, root, "java.exe")
	gameDir := newFakeGameDirectory(t, root)

	first, firstErr := SaveLaunchProfile(SaveLaunchProfileRequest{
		JavaPath:      javaPath,
		GameDirectory: gameDir,
		VersionID:     "1.20.1",
	})
	if firstErr != nil {
		t.Fatalf("首次保存失败: %v", firstErr)
	}

	second, secondErr := SaveLaunchProfile(SaveLaunchProfileRequest{
		JavaPath:      javaPath,
		GameDirectory: gameDir,
		VersionID:     "1.20.1",
	})
	if secondErr != nil {
		t.Fatalf("重复保存失败: %v", secondErr)
	}

	if got := countProfileRows(t); got != 1 {
		t.Fatalf("重复保存后应仍然只有 1 行，实际 %d 行", got)
	}
	if second.ID != first.ID {
		t.Errorf("重复保存应保留原 id：期望 %s，实际 %s", first.ID, second.ID)
	}
	if second.LastUsedAtMs < first.LastUsedAtMs {
		t.Errorf("最近使用时间不应倒退：首次 %d，再次 %d", first.LastUsedAtMs, second.LastUsedAtMs)
	}
}

// TestGetMostRecentLaunchProfile 验证「取最近一条」按最近使用时间倒序。
func TestGetMostRecentLaunchProfile(t *testing.T) {
	setupProfileTestDB(t)

	root := t.TempDir()
	javaPath := newFakeJava(t, root, "java.exe")
	olderDir := filepath.Join(root, "older", ".minecraft")
	newerDir := filepath.Join(root, "newer", ".minecraft")
	for _, dir := range []string{olderDir, newerDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("创建游戏目录失败: %v", err)
		}
	}

	if _, err := SaveLaunchProfile(SaveLaunchProfileRequest{
		JavaPath:      javaPath,
		GameDirectory: olderDir,
		VersionID:     "1.19.4",
	}); err != nil {
		t.Fatalf("保存旧配置失败: %v", err)
	}
	if _, err := SaveLaunchProfile(SaveLaunchProfileRequest{
		JavaPath:      javaPath,
		GameDirectory: newerDir,
		VersionID:     "1.20.1",
	}); err != nil {
		t.Fatalf("保存新配置失败: %v", err)
	}

	summary, getErr := GetMostRecentLaunchProfile()
	if getErr != nil {
		t.Fatalf("读取最近配置失败: %v", getErr)
	}
	if summary == nil {
		t.Fatal("最近配置不应为空")
	}
	if summary.VersionID != "1.20.1" {
		t.Errorf("应取最近使用的配置，期望版本 1.20.1，实际 %s", summary.VersionID)
	}
}

// TestGetMostRecentLaunchProfileWithoutRows 验证首次使用（无任何配置）返回 nil 而不是错误。
func TestGetMostRecentLaunchProfileWithoutRows(t *testing.T) {
	setupProfileTestDB(t)

	summary, getErr := GetMostRecentLaunchProfile()
	if getErr != nil {
		t.Fatalf("空库读取不应报错: %v", getErr)
	}
	if summary != nil {
		t.Errorf("空库应返回 nil 摘要，实际 %+v", summary)
	}
}

// TestGetMostRecentLaunchProfileRejectsStalePath 验证历史配置失效时返回 LAUNCH_PROFILE_001。
func TestGetMostRecentLaunchProfileRejectsStalePath(t *testing.T) {
	setupProfileTestDB(t)

	root := t.TempDir()
	javaPath := newFakeJava(t, root, "java.exe")
	gameDir := newFakeGameDirectory(t, root)

	if _, err := SaveLaunchProfile(SaveLaunchProfileRequest{
		JavaPath:      javaPath,
		GameDirectory: gameDir,
		VersionID:     "1.20.1",
	}); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}

	// 模拟用户卸载 Java：删除文件后恢复流程必须如实报告不可用。
	if err := os.Remove(javaPath); err != nil {
		t.Fatalf("删除假 Java 失败: %v", err)
	}

	summary, getErr := GetMostRecentLaunchProfile()
	if getErr == nil {
		t.Fatalf("Java 已不存在时应返回错误，实际返回 %+v", summary)
	}
	if getErr.Code != ErrorCodeLaunchProfile {
		t.Errorf("错误码不符：期望 %s，实际 %s", ErrorCodeLaunchProfile, getErr.Code)
	}
	if getErr.Message != messageProfilePathUnusable {
		t.Errorf("错误文案不符：%s", getErr.Message)
	}
}

// TestDeleteLaunchProfile 验证删除，并确认删除不存在的 id 也算成功（幂等）。
func TestDeleteLaunchProfile(t *testing.T) {
	setupProfileTestDB(t)

	root := t.TempDir()
	javaPath := newFakeJava(t, root, "java.exe")
	gameDir := newFakeGameDirectory(t, root)

	summary, saveErr := SaveLaunchProfile(SaveLaunchProfileRequest{
		JavaPath:      javaPath,
		GameDirectory: gameDir,
		VersionID:     "1.20.1",
	})
	if saveErr != nil {
		t.Fatalf("保存配置失败: %v", saveErr)
	}

	if deleteErr := DeleteLaunchProfile(summary.ID); deleteErr != nil {
		t.Fatalf("删除配置失败: %v", deleteErr)
	}
	if got := countProfileRows(t); got != 0 {
		t.Errorf("删除后应为 0 行，实际 %d 行", got)
	}

	// 幂等：重复删除不存在的 id 也算成功。
	if deleteErr := DeleteLaunchProfile(summary.ID); deleteErr != nil {
		t.Errorf("重复删除应为幂等成功，实际报错: %v", deleteErr)
	}
}

// TestSaveLaunchProfileRejectsInvalidInput 验证空值与相对路径被拒绝。
func TestSaveLaunchProfileRejectsInvalidInput(t *testing.T) {
	setupProfileTestDB(t)

	cases := []struct {
		name     string
		req      SaveLaunchProfileRequest
		wantText string
	}{
		{
			name:     "缺少 Java 路径",
			req:      SaveLaunchProfileRequest{GameDirectory: `C:\game`, VersionID: "1.20.1"},
			wantText: messageProfileJavaPathEmpty,
		},
		{
			name:     "缺少游戏目录",
			req:      SaveLaunchProfileRequest{JavaPath: `C:\jdk\bin\java.exe`, VersionID: "1.20.1"},
			wantText: messageProfileGameDirEmpty,
		},
		{
			name:     "缺少版本 id",
			req:      SaveLaunchProfileRequest{JavaPath: `C:\jdk\bin\java.exe`, GameDirectory: `C:\game`},
			wantText: messageProfileVersionEmpty,
		},
		{
			name:     "相对路径",
			req:      SaveLaunchProfileRequest{JavaPath: `jdk\bin\java.exe`, GameDirectory: `game`, VersionID: "1.20.1"},
			wantText: messageProfilePathNotAbsolute,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			summary, saveErr := SaveLaunchProfile(testCase.req)
			if saveErr == nil {
				t.Fatalf("应返回错误，实际返回 %+v", summary)
			}
			if saveErr.Code != ErrorCodeLaunchProfile {
				t.Errorf("错误码不符：期望 %s，实际 %s", ErrorCodeLaunchProfile, saveErr.Code)
			}
			if saveErr.Message != testCase.wantText {
				t.Errorf("错误文案不符：期望 %q，实际 %q", testCase.wantText, saveErr.Message)
			}
		})
	}

	if got := countProfileRows(t); got != 0 {
		t.Errorf("非法输入不应写入任何行，实际 %d 行", got)
	}
}

// TestLaunchProfileMigrationVersion 验证 000002 迁移被记录为版本 2。
//
// 同时确认 000001 仍然只应用一次（checksum 校验由 database.Migrate 负责，
// 这里只断言两条记录都存在且版本号连续）。
func TestLaunchProfileMigrationVersion(t *testing.T) {
	setupProfileTestDB(t)

	db, dbErr := profileDatabase()
	if dbErr != nil {
		t.Fatalf("打开启动配置库失败: %v", dbErr)
	}

	versions := make([]int, 0, 4)
	rows, queryErr := db.Query("SELECT version FROM schema_migrations ORDER BY version")
	if queryErr != nil {
		t.Fatalf("查询迁移记录失败: %v", queryErr)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			t.Fatalf("扫描迁移版本失败: %v", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历迁移记录失败: %v", err)
	}

	if len(versions) < 2 {
		t.Fatalf("期望至少两条迁移记录，实际 %v", versions)
	}
	if versions[0] != 1 || versions[1] != 2 {
		t.Errorf("迁移版本应连续为 1、2，实际 %v", versions)
	}

	// 重复调用 Migrate 是幂等的：再次打开不应新增迁移记录。
	secondDB, secondErr := profileDatabase()
	if secondErr != nil {
		t.Fatalf("再次打开启动配置库失败: %v", secondErr)
	}
	var total int
	if err := secondDB.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&total); err != nil {
		t.Fatalf("统计迁移记录失败: %v", err)
	}
	if total != len(versions) {
		t.Errorf("重复迁移不应新增记录：期望 %d 条，实际 %d 条", len(versions), total)
	}
}

// TestProfileDatabaseUnavailableDegrades 验证数据库路径不可用时不崩溃而是返回可降级错误。
func TestProfileDatabaseUnavailableDegrades(t *testing.T) {
	originalPathFunc := profileDatabasePath
	profileDatabasePath = func() settings.DatabasePathStatus {
		return settings.DatabasePathStatus{Kind: settings.PathKindUnavailable}
	}
	resetProfileDatabaseForTest()
	t.Cleanup(func() {
		resetProfileDatabaseForTest()
		profileDatabasePath = originalPathFunc
	})

	summary, saveErr := SaveLaunchProfile(SaveLaunchProfileRequest{
		JavaPath:      `C:\jdk\bin\java.exe`,
		GameDirectory: `C:\game`,
		VersionID:     "1.20.1",
	})
	if saveErr == nil {
		t.Fatalf("存储不可用时应返回错误，实际返回 %+v", summary)
	}
	if saveErr.Code != ErrorCodeLaunchProfile {
		t.Errorf("错误码不符：期望 %s，实际 %s", ErrorCodeLaunchProfile, saveErr.Code)
	}
	if saveErr.Message != messageProfileStorageUnavailable {
		t.Errorf("错误文案不符：%s", saveErr.Message)
	}

	// 列表读取同样降级而不是崩溃。
	if _, listErr := ListLaunchProfiles(); listErr == nil {
		t.Error("存储不可用时列表读取应返回错误")
	}

	// 确保没有意外留下连接。
	if db, _ := profileDatabase(); db != nil {
		t.Error("存储不可用时不应持有数据库连接")
	}
}

// TestListLaunchProfilesReturnsSummaries 验证列表返回全部配置的脱敏摘要。
func TestListLaunchProfilesReturnsSummaries(t *testing.T) {
	setupProfileTestDB(t)

	root := t.TempDir()
	javaPath := newFakeJava(t, root, "java.exe")
	for _, versionID := range []string{"1.19.4", "1.20.1"} {
		gameDir := filepath.Join(root, versionID, ".minecraft")
		if err := os.MkdirAll(gameDir, 0o755); err != nil {
			t.Fatalf("创建游戏目录失败: %v", err)
		}
		if _, err := SaveLaunchProfile(SaveLaunchProfileRequest{
			JavaPath:      javaPath,
			GameDirectory: gameDir,
			VersionID:     versionID,
		}); err != nil {
			t.Fatalf("保存配置 %s 失败: %v", versionID, err)
		}
	}

	summaries, listErr := ListLaunchProfiles()
	if listErr != nil {
		t.Fatalf("读取配置列表失败: %v", listErr)
	}
	if len(summaries) != 2 {
		t.Fatalf("期望 2 条配置，实际 %d 条", len(summaries))
	}
	for _, summary := range summaries {
		if strings.Contains(summary.JavaDisplay, root) {
			t.Errorf("列表摘要泄露了绝对路径：%s", summary.JavaDisplay)
		}
	}
}
