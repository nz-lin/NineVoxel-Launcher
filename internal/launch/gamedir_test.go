package launch

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestResolveInsideAcceptsSafeRelativePaths 验证 root 内部的合法相对路径被接受。
//
// 这是后续 P2-03 ~ P2-06 的主要用法：读取 root 内的 JSON、jar、库文件。
func TestResolveInsideAcceptsSafeRelativePaths(t *testing.T) {
	root := t.TempDir()

	cases := []string{
		"versions/1.21/1.21.json",
		"libraries/net/minecraft/launchwrapper/1.12/launchwrapper-1.12.jar",
		"assets/indexes/17.json",
		"options.txt",
		// 空路径表示 root 本身，合法。
		"",
	}

	for _, relative := range cases {
		t.Run(relative, func(t *testing.T) {
			got, err := resolveInside(root, relative)
			if err != nil {
				t.Fatalf("相对路径 %q 期望接受，实际错误: %v", relative, err)
			}
			// 空路径返回 root 本身。
			if relative == "" {
				if got != root {
					t.Fatalf("空相对路径期望返回 root，实际 %q", got)
				}
				return
			}
			// 返回值必须是绝对路径且位于 root 内。
			if !filepath.IsAbs(got) {
				t.Fatalf("返回值期望绝对路径，实际 %q", got)
			}
			if err := ensureWithinRoot(root, got); err != nil {
				t.Fatalf("返回值 %q 不在 root 内: %v", got, err)
			}
		})
	}
}

// TestResolveInsideRejectsParentTraversal 验证 `..` 段被拒绝。
//
// 这是最经典的路径穿越攻击面：`../../etc/passwd`、`a/../b` 都要拦。
func TestResolveInsideRejectsParentTraversal(t *testing.T) {
	root := t.TempDir()

	cases := []string{
		"../outside",
		"../../etc/passwd",
		"versions/../../outside",
		"a/../b",
		"..",
		// Windows 风格的反斜杠穿越：用转义写法，避免依赖 raw string 前缀。
		"..\\windows\\system32",
	}

	for _, relative := range cases {
		t.Run(relative, func(t *testing.T) {
			_, err := resolveInside(root, relative)
			if err == nil {
				t.Fatalf("含 .. 的相对路径 %q 期望拒绝，实际通过", relative)
			}
			if errorCode(err) != ErrorCodePathInvalid {
				t.Fatalf("错误码期望 %q，实际 %q", ErrorCodePathInvalid, errorCode(err))
			}
		})
	}
}

// TestResolveInsideRejectsAbsolutePaths 验证绝对路径注入被拒绝。
func TestResolveInsideRejectsAbsolutePaths(t *testing.T) {
	root := t.TempDir()

	cases := []string{
		"/etc/passwd",
		`\windows\system32\config`,
		`C:\Windows\System32`,
		`D:\game`,
		`C:relative`, // 盘符相对路径
	}

	for _, relative := range cases {
		t.Run(relative, func(t *testing.T) {
			_, err := resolveInside(root, relative)
			if err == nil {
				t.Fatalf("绝对路径 %q 期望拒绝，实际通过", relative)
			}
			if errorCode(err) != ErrorCodePathInvalid {
				t.Fatalf("错误码期望 %q，实际 %q", ErrorCodePathInvalid, errorCode(err))
			}
		})
	}
}

// TestResolveInsideRejectsNUL 验证 NUL 字符被拒绝。
//
// NUL 在部分系统调用处会被截断，是绕过扩展名/路径校验的常见手法。
func TestResolveInsideRejectsNUL(t *testing.T) {
	root := t.TempDir()

	_, err := resolveInside(root, "versions/1.21\x00.json")
	if err == nil {
		t.Fatal("含 NUL 的相对路径期望拒绝，实际通过")
	}
	if errorCode(err) != ErrorCodePathInvalid {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodePathInvalid, errorCode(err))
	}
}

// TestEnsureWithinRootPrefixBoundary 验证「前缀相同但不是子目录」的边界。
//
// 关键用例：root = `C:\game`，候选 `C:\game2` 必须判为越界。
// 若用字符串前缀比较就会误判为「在内部」，这是本卡必须防住的漏洞。
func TestEnsureWithinRootPrefixBoundary(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("前缀边界用例针对 Windows 盘符路径")
	}

	cases := []struct {
		name      string
		root      string
		candidate string
		wantErr   bool
	}{
		{"同目录", `C:\game`, `C:\game`, false},
		{"子目录", `C:\game`, `C:\game\versions`, false},
		{"深子目录", `C:\game`, `C:\game\a\b\c`, false},
		{"前缀陷阱 game2", `C:\game`, `C:\game2`, true},
		{"前缀陷阱 game2 深层", `C:\game`, `C:\game2\versions`, true},
		{"完全外部", `C:\game`, `C:\Windows`, true},
		{"大小写不敏感同目录", `C:\game`, `c:\GAME`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ensureWithinRoot(tc.root, tc.candidate)
			if tc.wantErr && err == nil {
				t.Fatalf("候选 %q 相对 root %q 期望越界，实际被判为内部", tc.candidate, tc.root)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("候选 %q 相对 root %q 期望在内部，实际被判为越界: %v", tc.candidate, tc.root, err)
			}
		})
	}
}

// TestResolveInsideRejectsSymlinkEscape 验证符号链接逃逸被拒绝。
//
// 攻击场景：root 内放一个指向外部目录的链接，然后请求「链接内的文件」。
// 若不解析真实路径，就会读到 root 之外的内容。
func TestResolveInsideRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	// 在 root 内创建一个指向 outside 的符号链接。
	linkPath := filepath.Join(root, "escape")
	if err := os.Symlink(outside, linkPath); err != nil {
		// Windows 非管理员账户可能无法创建符号链接；此时跳过该用例。
		t.Skipf("无法创建符号链接（可能缺少权限）: %v", err)
	}

	// 通过链接访问外部目录内的文件，必须被拒绝。
	_, err := resolveInside(root, "escape/outside-file.txt")
	if err == nil {
		t.Fatal("符号链接逃逸期望拒绝，实际通过")
	}
	if errorCode(err) != ErrorCodePathInvalid {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodePathInvalid, errorCode(err))
	}
}

// TestResolveInsideAllowsInternalSymlink 验证 root 内部链接不被误拒。
//
// 链接指向 root 内部时属于合法用法（例如用户把 versions 链接到别的盘但仍在根内）。
func TestResolveInsideAllowsInternalSymlink(t *testing.T) {
	root := t.TempDir()

	// 在 root 内建一个子目录与指向它的链接。
	innerDir := filepath.Join(root, "real-versions")
	if err := os.MkdirAll(innerDir, 0o755); err != nil {
		t.Fatalf("准备内部目录失败: %v", err)
	}
	linkPath := filepath.Join(root, "versions-link")
	if err := os.Symlink(innerDir, linkPath); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	// root 内部的符号链接应被接受。
	if _, err := resolveInside(root, "versions-link"); err != nil {
		t.Fatalf("root 内部符号链接期望接受，实际错误: %v", err)
	}
}

// TestNormalizeGameRootRejectsInvalid 验证游戏根目录输入的四类拒绝。
func TestNormalizeGameRootRejectsInvalid(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "空值", input: "   ", wantErr: ErrorCodePathInvalid},
		{name: "相对路径", input: "./minecraft", wantErr: ErrorCodePathInvalid},
		{name: "含父级段", input: `C:\a\..\b`, wantErr: ErrorCodePathInvalid},
		{name: "含 NUL", input: "C:\\game\x00", wantErr: ErrorCodePathInvalid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := normalizeGameRoot(tc.input)
			if err == nil {
				t.Fatalf("输入 %q 期望拒绝，实际通过", tc.input)
			}
			if errorCode(err) != tc.wantErr {
				t.Fatalf("错误码期望 %q，实际 %q", tc.wantErr, errorCode(err))
			}
		})
	}
}

// TestNormalizeGameRootRejectsDriveRoot 验证盘符根目录被拒绝。
func TestNormalizeGameRootRejectsDriveRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("盘符根用例针对 Windows")
	}

	// 用 system drive 的根，避免依赖具体盘符是否存在。
	driveRoot := filepath.VolumeName(os.Getenv("SystemDrive")+`\`) + `\`

	_, _, err := normalizeGameRoot(driveRoot)
	if err == nil {
		t.Fatalf("盘符根 %q 期望拒绝，实际通过", driveRoot)
	}
	if errorCode(err) != ErrorCodePathInvalid {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodePathInvalid, errorCode(err))
	}
}

// TestNormalizeGameRootRejectsMissingAndFile 验证不存在与指向文件的两种拒绝。
func TestNormalizeGameRootRejectsMissingAndFile(t *testing.T) {
	t.Run("不存在", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "not-exist-dir")
		_, _, err := normalizeGameRoot(missing)
		if err == nil {
			t.Fatal("不存在的目录期望拒绝，实际通过")
		}
		if errorCode(err) != ErrorCodePathInvalid {
			t.Fatalf("错误码期望 %q，实际 %q", ErrorCodePathInvalid, errorCode(err))
		}
	})

	t.Run("指向文件", func(t *testing.T) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, "a-file.txt")
		if writeErr := os.WriteFile(filePath, []byte("x"), 0o600); writeErr != nil {
			t.Fatalf("准备测试文件失败: %v", writeErr)
		}

		_, _, err := normalizeGameRoot(filePath)
		if err == nil {
			t.Fatal("指向文件期望拒绝，实际通过")
		}
		if errorCode(err) != ErrorCodePathInvalid {
			t.Fatalf("错误码期望 %q，实际 %q", ErrorCodePathInvalid, errorCode(err))
		}
	})
}

// TestNormalizeGameRootAcceptsRealDirectory 验证真实目录被接受并返回归一化路径。
func TestNormalizeGameRootAcceptsRealDirectory(t *testing.T) {
	dir := t.TempDir()

	got, warnings, err := normalizeGameRoot(dir)
	if err != nil {
		t.Fatalf("真实目录期望接受，实际错误: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("归一化结果期望绝对路径，实际 %q", got)
	}
	// 符号链接解析成功时不应有警告。
	if len(warnings) != 0 {
		t.Fatalf("期望无警告，实际 %v", warnings)
	}
}

// TestCheckGameDirectoryStructureMatrix 验证结构检查的三态判定矩阵。
//
// 覆盖：全缺失 / 全部齐全 / versions 是文件 / libraries 空目录。
func TestCheckGameDirectoryStructureMatrix(t *testing.T) {
	t.Run("结构全缺失但目录存在", func(t *testing.T) {
		root := t.TempDir()

		result := checkGameDirectory(root)

		// 关键语义：结构缺失**不算错误**，errorCode 必须为空。
		if result.ErrorCode != "" {
			t.Fatalf("结构缺失期望无错误码，实际 %q", result.ErrorCode)
		}
		if result.Valid {
			t.Fatal("空目录期望 valid=false")
		}
		if result.VersionsCount != 0 {
			t.Fatalf("versions 数量期望 0，实际 %d", result.VersionsCount)
		}
		// 提示必须说明缺 versions。
		if result.LaunchableHint != hintGameDirMissingVersions {
			t.Fatalf("提示期望 %q，实际 %q", hintGameDirMissingVersions, result.LaunchableHint)
		}
		// 六个检查项都必须存在且为 missing。
		if len(result.Checks) != 6 {
			t.Fatalf("检查项期望 6 个，实际 %d", len(result.Checks))
		}
		for _, item := range result.Checks {
			if item.Status != checkStatusMissing {
				t.Fatalf("检查项 %q 期望 missing，实际 %q", item.Name, item.Status)
			}
		}
		// 展示形态必须非空且不含完整路径。
		if result.GameRootDisplay == "" {
			t.Fatal("展示形态不应为空")
		}
	})

	t.Run("结构齐全", func(t *testing.T) {
		root := t.TempDir()
		mustMkdirAll(t, filepath.Join(root, dirVersions, "1.21"))
		mustMkdirAll(t, filepath.Join(root, dirVersions, "1.20"))
		mustMkdirAll(t, filepath.Join(root, dirLibraries, "net", "minecraft"))
		mustMkdirAll(t, filepath.Join(root, dirAssets, dirAssetsIndexes))
		mustMkdirAll(t, filepath.Join(root, dirAssets, dirAssetsObjects))
		mustWriteFile(t, filepath.Join(root, fileLauncherProfiles), "{}")

		result := checkGameDirectory(root)

		if result.ErrorCode != "" {
			t.Fatalf("结构齐全期望无错误码，实际 %q", result.ErrorCode)
		}
		if !result.Valid {
			t.Fatal("结构齐全期望 valid=true")
		}
		if result.VersionsCount != 2 {
			t.Fatalf("versions 直接子目录期望 2，实际 %d", result.VersionsCount)
		}
		if result.LaunchableHint != "" {
			t.Fatalf("可启动时期望无提示，实际 %q", result.LaunchableHint)
		}
		for _, item := range result.Checks {
			if item.Status != checkStatusPresent {
				t.Fatalf("检查项 %q 期望 present，实际 %q", item.Name, item.Status)
			}
		}
	})

	t.Run("versions 存在但是文件", func(t *testing.T) {
		root := t.TempDir()
		mustWriteFile(t, filepath.Join(root, dirVersions), "not a dir")

		result := checkGameDirectory(root)

		if result.Valid {
			t.Fatal("versions 是文件时期望 valid=false")
		}
		// 关键：必须区分 not_a_directory 与 missing。
		for _, item := range result.Checks {
			if item.Name == checkNameVersions {
				if item.Status != checkStatusNotADirectory {
					t.Fatalf("versions 期望 not_a_directory，实际 %q", item.Status)
				}
			}
		}
		if result.LaunchableHint != hintGameDirVersionsNotDir {
			t.Fatalf("提示期望 %q，实际 %q", hintGameDirVersionsNotDir, result.LaunchableHint)
		}
	})

	t.Run("libraries 存在但无分组子目录", func(t *testing.T) {
		root := t.TempDir()
		mustMkdirAll(t, filepath.Join(root, dirVersions, "1.21"))
		mustMkdirAll(t, filepath.Join(root, dirLibraries))

		result := checkGameDirectory(root)

		// libraries 本身仍是 present（目录存在），但说明文案要指出没有分组。
		for _, item := range result.Checks {
			if item.Name == checkNameLibraries {
				if item.Status != checkStatusPresent {
					t.Fatalf("libraries 期望 present，实际 %q", item.Status)
				}
				if item.Detail != detailLibrariesNoGroup {
					t.Fatalf("libraries 说明期望 %q，实际 %q", detailLibrariesNoGroup, item.Detail)
				}
			}
		}
	})

	t.Run("versions 是空目录", func(t *testing.T) {
		root := t.TempDir()
		mustMkdirAll(t, filepath.Join(root, dirVersions))

		result := checkGameDirectory(root)

		if result.Valid {
			t.Fatal("versions 无子目录时期望 valid=false")
		}
		if result.LaunchableHint != hintGameDirNoVersions {
			t.Fatalf("提示期望 %q，实际 %q", hintGameDirNoVersions, result.LaunchableHint)
		}
	})
}

// TestCheckGameDirectoryDoesNotCreateAnything 验证检查过程对游戏目录零写入。
//
// 断言方式：检查前后目录项集合完全一致（不新增 logs/、saves/、options.txt 等）。
func TestCheckGameDirectoryDoesNotCreateAnything(t *testing.T) {
	root := t.TempDir()

	before := listAllEntries(t, root)
	result := checkGameDirectory(root)
	after := listAllEntries(t, root)

	if result.ErrorCode != "" {
		t.Fatalf("空目录检查不应报错，实际 %q", result.ErrorCode)
	}
	if len(before) != len(after) {
		t.Fatalf("检查过程创建了目录项：前 %v，后 %v", before, after)
	}
	for index := range before {
		if before[index] != after[index] {
			t.Fatalf("目录项发生变化：前 %v，后 %v", before, after)
		}
	}
	// 空目录检查后仍然是空目录。
	if len(after) != 0 {
		t.Fatalf("检查后目录应为空，实际含 %v", after)
	}
}

// TestCheckGameDirectoryRejectsMissing 验证目录不存在时返回 PATH_INVALID_001。
func TestCheckGameDirectoryRejectsMissing(t *testing.T) {
	result := checkGameDirectory(filepath.Join(t.TempDir(), "missing-dir"))

	if result.ErrorCode != ErrorCodePathInvalid {
		t.Fatalf("错误码期望 %q，实际 %q", ErrorCodePathInvalid, result.ErrorCode)
	}
	if result.Valid {
		t.Fatal("目录不存在时期望 valid=false")
	}
}

// TestBuildGameRootDisplay 验证游戏目录脱敏展示形态（C-11 末两段）。
func TestBuildGameRootDisplay(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("示例针对 Windows 路径形态")
	}

	cases := []struct {
		path string
		want string
	}{
		{
			path: `C:\Users\songr\AppData\Roaming\.minecraft`,
			want: `游戏目录\…\Roaming\.minecraft`,
		},
		{
			// 盘符段不算「可展示目录名」：必须仍加类别前缀与省略标记，
			// 否则会把 `C:\minecraft` 原样回显成 `C:\minecraft`，泄漏路径前缀。
			path: `C:\minecraft`,
			want: `游戏目录\…\minecraft`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := buildGameRootDisplay(tc.path)
			if got != tc.want {
				t.Fatalf("展示形态期望 %q，实际 %q", tc.want, got)
			}
			// 不得含完整路径的盘符或用户名前缀。
			if containsAny(got, `C:\`, "Users", "AppData") {
				t.Fatalf("展示形态 %q 仍含敏感前缀", got)
			}
		})
	}
}

// TestGameDirErrorCodeFrozen 验证 GAME_DIR_001 的码值与冻结表一致。
func TestGameDirErrorCodeFrozen(t *testing.T) {
	if ErrorCodeGameDir != "GAME_DIR_001" {
		t.Fatalf("GAME_DIR_001 常量值错误: %q", ErrorCodeGameDir)
	}
}

// mustMkdirAll 创建目录，失败即中止测试。
//
// 只在测试的临时目录内使用，不触碰真实游戏目录。
func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("创建测试目录 %q 失败: %v", path, err)
	}
}

// mustWriteFile 写入测试文件，失败即中止测试。
//
// 只在测试的临时目录内使用。
func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试文件 %q 失败: %v", path, err)
	}
}

// listAllEntries 列出目录下的全部直接条目名，用于零写入断言。
func listAllEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("列举目录 %q 失败: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// errorCode 从错误中提取稳定错误码。
//
// 测试辅助函数：一并支持 `*Error`（normalizeGameRoot / checkGameDirectory 的返回）
// 与 `error`（resolveInside 的返回，其底层仍是 *Error）。
// 提取不到时返回空串，让断言自然失败并打印实际值。
func errorCode(err error) string {
	if err == nil {
		return ""
	}

	var launchErr *Error
	if errors.As(err, &launchErr) {
		return launchErr.Code
	}
	return ""
}
