package settings

import (
	"context"
	"errors"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// TestSchemaHasNineWhitelistedKeys 验证白名单恰好包含冻结的九个 key。
//
// 前六个 key 的数量与名字是冻结契约（接口冻结清单 §8.7）；第七个
// `download.mirrorEnabled` 由 D-57 追加；第八个 `launch.javaPath` 用于
// 用户显式指定 Java 路径；第九个 `launch.maxMemoryMb` 持久化默认分配内存。
// 多一个或少一个都会让前端设置页与后端校验不一致。
func TestSchemaHasNineWhitelistedKeys(t *testing.T) {
	expected := []string{
		KeyAppearanceTheme,
		KeyAppearanceLanguage,
		KeyWindowWidth,
		KeyWindowHeight,
		KeyWindowMaximized,
		KeyUpdateChannel,
		KeyDownloadMirrorEnabled,
		KeyLaunchJavaPath,
		KeyLaunchMaxMemoryMb,
	}

	schemas := AllSchemas()
	if len(schemas) != len(expected) {
		t.Fatalf("白名单期望 %d 项，实际 %d 项", len(expected), len(schemas))
	}

	for _, key := range expected {
		if _, ok := SchemaByKey(key); !ok {
			t.Fatalf("冻结 key %q 不在白名单内", key)
		}
	}
}

// TestDefaultsMatchFrozenValues 验证默认值与卡面 A 节逐项一致。
func TestDefaultsMatchFrozenValues(t *testing.T) {
	defaults := Defaults()

	cases := []struct {
		key  string
		want any
	}{
		{KeyAppearanceTheme, ThemeSystem},
		{KeyAppearanceLanguage, LocaleZhHans},
		{KeyWindowWidth, 1120},
		{KeyWindowHeight, 760},
		{KeyWindowMaximized, false},
		{KeyUpdateChannel, ChannelStable},
		{KeyDownloadMirrorEnabled, false},
		{KeyLaunchJavaPath, ""},
		{KeyLaunchMaxMemoryMb, 8192},
	}

	for _, tc := range cases {
		got, ok := defaults[tc.key]
		if !ok {
			t.Fatalf("默认值缺少 key %q", tc.key)
		}
		if got != tc.want {
			t.Fatalf("key %q 的默认值期望 %v，实际 %v", tc.key, tc.want, got)
		}
	}
}

// TestValidateChangeRejectsUnknownKey 验证白名单外的 key 被拒绝。
//
// 该行为对应 APP_CONFIG_002：不得静默忽略未知 key。
func TestValidateChangeRejectsUnknownKey(t *testing.T) {
	err := ValidateChange("unknown.key", "x")
	if err == nil {
		t.Fatal("未知 key 期望返回错误，实际通过")
	}
	if !errors.Is(err, err) {
		// 占位断言：ValidationError 是值类型，用 errors.As 检查其具体类型。
	}

	var validation ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("错误类型期望 ValidationError，实际 %T", err)
	}
	if translation := validation.Error(); translation[:len(ErrorCodeConfigInvalid)] != ErrorCodeConfigInvalid {
		t.Fatalf("错误信息应以 %s 开头，实际 %q", ErrorCodeConfigInvalid, translation)
	}
}

// TestValidateChangeRejectsReadOnlyKey 验证只读 key（update.channel）不可写。
func TestValidateChangeRejectsReadOnlyKey(t *testing.T) {
	if err := ValidateChange(KeyUpdateChannel, ChannelBeta); err == nil {
		t.Fatal("只读 key 期望拒绝写入，实际通过")
	}
}

// TestValidateChangeStringValues 验证字符串类 key 的取值白名单。
func TestValidateChangeStringValues(t *testing.T) {
	valid := []struct {
		key   string
		value string
	}{
		{KeyAppearanceTheme, ThemeLight},
		{KeyAppearanceTheme, ThemeDark},
		{KeyAppearanceTheme, ThemeSystem},
		{KeyAppearanceLanguage, LocaleZhHans},
		{KeyAppearanceLanguage, LocaleZhHant},
		{KeyAppearanceLanguage, LocaleEn},
	}
	for _, tc := range valid {
		if err := ValidateChange(tc.key, tc.value); err != nil {
			t.Fatalf("合法值 %q=%q 被拒绝: %v", tc.key, tc.value, err)
		}
	}

	invalid := []struct {
		key   string
		value any
	}{
		{KeyAppearanceTheme, "auto"},
		{KeyAppearanceTheme, 123},
		{KeyAppearanceLanguage, "zh"},
		{KeyAppearanceLanguage, nil},
	}
	for _, tc := range invalid {
		if err := ValidateChange(tc.key, tc.value); err == nil {
			t.Fatalf("非法值 %q=%v 期望拒绝，实际通过", tc.key, tc.value)
		}
	}
}

// TestValidateChangeWindowRange 验证窗口尺寸的闭区间校验与整数约束。
func TestValidateChangeWindowRange(t *testing.T) {
	// 边界值必须被接受（闭区间）。
	for _, tc := range []struct {
		key   string
		value int
	}{
		{KeyWindowWidth, MinWindowWidth},
		{KeyWindowWidth, MaxWindowWidth},
		{KeyWindowHeight, MinWindowHeight},
		{KeyWindowHeight, MaxWindowHeight},
	} {
		if err := ValidateChange(tc.key, tc.value); err != nil {
			t.Fatalf("边界值 %q=%d 被拒绝: %v", tc.key, tc.value, err)
		}
	}

	// 越界值必须被拒绝（含刚超出边界 1）。
	for _, tc := range []struct {
		key   string
		value any
	}{
		{KeyWindowWidth, MinWindowWidth - 1},
		{KeyWindowWidth, MaxWindowWidth + 1},
		{KeyWindowHeight, MinWindowHeight - 1},
		{KeyWindowHeight, MaxWindowHeight + 1},
		{KeyWindowWidth, 1120.5},
		{KeyWindowWidth, "1120"},
	} {
		if err := ValidateChange(tc.key, tc.value); err == nil {
			t.Fatalf("非法值 %q=%v 期望拒绝，实际通过", tc.key, tc.value)
		}
	}
}

// TestValidateChangeAcceptsNumericForms 验证多种数值形态被统一接受。
//
// JSON 反序列化会得到 float64，Wails 绑定可能传 int / int64 / json.Number；
// 若只接受其中一种，前端就无法保存窗口尺寸。
func TestValidateChangeAcceptsNumericForms(t *testing.T) {
	if err := ValidateChange(KeyWindowWidth, float64(1120)); err != nil {
		t.Fatalf("float64(1120) 应被接受: %v", err)
	}
	if err := ValidateChange(KeyWindowWidth, int64(1120)); err != nil {
		t.Fatalf("int64(1120) 应被接受: %v", err)
	}
}

// TestNormalizeSnapshotFallsBackToDefaults 验证脏数据被收敛为合法默认值。
//
// 场景：数据库里存着旧版本写入的非法值或白名单外 key。
// 界面必须拿到六个合法值，不能因为一条坏记录整页打不开。
func TestNormalizeSnapshotFallsBackToDefaults(t *testing.T) {
	stored := map[string]any{
		KeyAppearanceTheme:    "auto",     // 非法取值
		KeyAppearanceLanguage: 123,        // 类型错误
		KeyWindowWidth:        -5,         // 越界
		KeyWindowHeight:       "tall",     // 类型错误
		KeyWindowMaximized:    "yes",      // 类型错误
		"legacy.unknown.key":  "whatever", // 白名单外
	}

	normalized := NormalizeSnapshot(stored)

	if normalized[KeyAppearanceTheme] != ThemeSystem {
		t.Fatalf("非法主题应回退 %q，实际 %v", ThemeSystem, normalized[KeyAppearanceTheme])
	}
	if normalized[KeyAppearanceLanguage] != LocaleZhHans {
		t.Fatalf("非法语言应回退 %q，实际 %v", LocaleZhHans, normalized[KeyAppearanceLanguage])
	}
	if normalized[KeyWindowWidth] != 1120 {
		t.Fatalf("越界宽度应回退 1120，实际 %v", normalized[KeyWindowWidth])
	}
	if normalized[KeyWindowHeight] != 760 {
		t.Fatalf("非法高度应回退 760，实际 %v", normalized[KeyWindowHeight])
	}
	if normalized[KeyWindowMaximized] != false {
		t.Fatalf("非法最大化应回退 false，实际 %v", normalized[KeyWindowMaximized])
	}
	// 白名单外 key 不得出现在结果里。
	if _, ok := normalized["legacy.unknown.key"]; ok {
		t.Fatal("白名单外 key 不应出现在规范化结果中")
	}
	// 只读 key 始终展示默认值。
	if normalized[KeyUpdateChannel] != ChannelStable {
		t.Fatalf("只读 key 应固定为 %q，实际 %v", ChannelStable, normalized[KeyUpdateChannel])
	}
}

// TestNormalizeSnapshotKeepsValidValues 验证合法存储值被原样保留。
func TestNormalizeSnapshotKeepsValidValues(t *testing.T) {
	stored := map[string]any{
		KeyAppearanceTheme:    ThemeDark,
		KeyAppearanceLanguage: LocaleEn,
		KeyWindowWidth:        float64(1600), // JSON 反序列化的典型形态
		KeyWindowHeight:       900,
		KeyWindowMaximized:    true,
	}

	normalized := NormalizeSnapshot(stored)

	if normalized[KeyAppearanceTheme] != ThemeDark {
		t.Fatalf("合法主题被改动: %v", normalized[KeyAppearanceTheme])
	}
	if normalized[KeyAppearanceLanguage] != LocaleEn {
		t.Fatalf("合法语言被改动: %v", normalized[KeyAppearanceLanguage])
	}
	// 整数必须收敛为 int，而不是 float64（避免前端收到 1600.0）。
	if value, ok := normalized[KeyWindowWidth].(int); !ok || value != 1600 {
		t.Fatalf("宽度期望 int(1600)，实际 %T(%v)", normalized[KeyWindowWidth], normalized[KeyWindowWidth])
	}
	if normalized[KeyWindowMaximized] != true {
		t.Fatalf("合法最大化值被改动: %v", normalized[KeyWindowMaximized])
	}
}

// TestIsKnownKey 验证白名单判定。
func TestIsKnownKey(t *testing.T) {
	for _, key := range []string{KeyAppearanceTheme, KeyWindowWidth, KeyUpdateChannel} {
		if !IsKnownKey(key) {
			t.Fatalf("key %q 应被识别为已知", key)
		}
	}
	for _, key := range []string{"", "unknown", "appearance.theme.extra"} {
		if IsKnownKey(key) {
			t.Fatalf("key %q 不应被识别为已知", key)
		}
	}
}

// TestMirrorEnabledSettingPersistsThroughSQLiteStore 验证第七个 key 真的可用。
//
// 覆盖三件事：白名单接受布尔写入、SQLite 存储能落盘并回读、快照收敛不会把它
// 当作白名单外 key 丢弃。数据库路径通过重定向用户配置目录指向临时目录，
// 绝不触碰真实用户数据目录。
func TestMirrorEnabledSettingPersistsThroughSQLiteStore(t *testing.T) {
	tempDir := t.TempDir()
	if goruntime.GOOS == "windows" {
		// AppRoot 在 Windows 上走 os.UserCacheDir（%LocalAppData%），
		// 只重定向 AppData（Roaming）不会生效，测试会写到真实用户目录。
		t.Setenv("AppData", tempDir)
		t.Setenv("LocalAppData", tempDir)
	} else {
		t.Setenv("HOME", tempDir)
		t.Setenv("XDG_CONFIG_HOME", tempDir)
		t.Setenv("XDG_DATA_HOME", tempDir)
	}

	// 第一步：白名单与可写性。只读或类型不符都会在这里失败。
	if err := ValidateChange(KeyDownloadMirrorEnabled, true); err != nil {
		t.Fatalf("镜像开关应为可写的布尔项: %v", err)
	}

	ctx := context.Background()
	// 用独立连接而不是共享池：本用例要断言落盘路径，且不能把临时路径
	// 变成整个进程后续测试的默认数据库。
	store, err := OpenSQLiteStoreAt(ctx, filepath.Join(tempDir, "db", "app.db"))
	if err != nil {
		t.Fatalf("打开设置存储失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// 防御：确认落到临时目录，避免任何情况下写进真实用户配置。
	if !strings.HasPrefix(store.Path(), tempDir) {
		t.Fatalf("数据库路径未落在临时目录: %s", store.Path())
	}

	if err := store.Save(ctx, map[string]any{KeyDownloadMirrorEnabled: true}, 1000); err != nil {
		t.Fatalf("写入镜像开关失败: %v", err)
	}

	values, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if enabled, ok := values[KeyDownloadMirrorEnabled]; !ok || enabled != true {
		t.Fatalf("回读值 = %v（存在=%v），期望 true", enabled, ok)
	}

	// 快照收敛必须保留它：白名单外的 key 会在这里被静默丢弃。
	if NormalizeSnapshot(values)[KeyDownloadMirrorEnabled] != true {
		t.Fatal("NormalizeSnapshot 丢弃了镜像开关，说明该 key 未真正进入白名单")
	}
}

// TestValidateChangeAcceptsLaunchJavaPath 验证 `launch.javaPath` 的类型与长度约束。
//
// 该 key 用的是新增的 KindPath：不受 allowed 列表约束，且**空串必须合法**——
// 空串是「未指定」的正式语义，此时启动器只使用自己安装的运行时。
func TestValidateChangeAcceptsLaunchJavaPath(t *testing.T) {
	// 空串表示「未指定」，必须被接受，否则「清除路径」会被后端拒绝。
	if err := ValidateChange(KeyLaunchJavaPath, ""); err != nil {
		t.Fatalf("空路径应被接受：%v", err)
	}

	// 常见 Windows 与 POSIX 路径都原样接受：后端不解析、不校验存在性，
	// 只负责持久化；有效性由 internal/runtime 的探测负责。
	paths := []string{
		`C:\Program Files\Java\jdk-21\bin\java.exe`,
		`D:\NineVoxel\javaruntime\java-21\bin\java.exe`,
		"/usr/lib/jvm/java-21-openjdk/bin/java",
	}
	for _, path := range paths {
		if err := ValidateChange(KeyLaunchJavaPath, path); err != nil {
			t.Fatalf("路径 %q 应被接受：%v", path, err)
		}
	}

	// 非字符串类型必须拒绝：前端传入的 any 可能被伪造。
	if err := ValidateChange(KeyLaunchJavaPath, 42); err == nil {
		t.Fatal("非字符串值应被拒绝")
	}

	// 超长内容必须拒绝，避免把垃圾写进设置表。
	if err := ValidateChange(KeyLaunchJavaPath, strings.Repeat("a", maxPathLen+1)); err == nil {
		t.Fatal("超长路径应被拒绝")
	}
}

// TestValidateChangeAcceptsLaunchMaxMemoryMb 验证 `launch.maxMemoryMb` 的区间约束。
//
// 存储层只保证闭区间：下限是 Minecraft 能启动的门槛，上限 1 TiB 只用于挡住
// 垃圾输入。真实可用上限由界面按本机物理内存计算，不在这一层校验。
func TestValidateChangeAcceptsLaunchMaxMemoryMb(t *testing.T) {
	// 默认值与常见取值都必须被接受。
	for _, mb := range []int{MinLaunchMemoryMb, 4096, 8192, 16384, 32768, 65536} {
		if err := ValidateChange(KeyLaunchMaxMemoryMb, mb); err != nil {
			t.Fatalf("%d MiB 应被接受：%v", mb, err)
		}
	}

	// 低于下限必须拒绝：2 GB 以下的堆无法启动现代 Minecraft。
	if err := ValidateChange(KeyLaunchMaxMemoryMb, MinLaunchMemoryMb-1); err == nil {
		t.Fatal("低于下限的值应被拒绝")
	}

	// 高于存储层上限必须拒绝。
	if err := ValidateChange(KeyLaunchMaxMemoryMb, MaxLaunchMemoryMb+1); err == nil {
		t.Fatal("超过存储层上限的值应被拒绝")
	}

	// 非数值类型必须拒绝：前端传入的 any 可能被伪造。
	if err := ValidateChange(KeyLaunchMaxMemoryMb, "8192"); err == nil {
		t.Fatal("字符串应被拒绝")
	}
}
