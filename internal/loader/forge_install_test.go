package loader

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/ninevoxel/launcher/internal/download"
)

// 本文件离线覆盖 Forge 安装规划的四个关键点：
//   - install_profile 的 data 占位符解析（maven / 字面量 / 安装器内文件 / 间接引用）
//   - processor 的 client 侧筛选与依赖去重
//   - 参数替换（内置键、data 键、未知键保留）
//   - MANIFEST.MF 的主类解析（含续行）

// ===== 安装器夹具 =====

// forgeFixture 描述一个合成的 Forge 安装器。
type forgeFixture struct {
	// processors 覆盖 install_profile 的 processors 数组。
	processors []map[string]any
	// data 覆盖 install_profile 的 data 对象。
	data map[string]any
	// libraries 覆盖 version.json 的 libraries 数组。
	libraries []map[string]any
	// extraFiles 是额外写入安装器 jar 的条目（路径 → 内容）。
	extraFiles map[string]string
}

// buildInstaller 按夹具生成一个可解析的安装器 zip。
func buildInstaller(t *testing.T, versionID, mcVersion string, fixture forgeFixture) string {
	t.Helper()
	profile := map[string]any{
		"spec":       1,
		"profile":    "forge",
		"version":    versionID,
		"path":       "net.minecraftforge:forge:" + mcVersion + "-1.0:shim",
		"minecraft":  mcVersion,
		"data":       fixture.data,
		"processors": fixture.processors,
	}
	version := map[string]any{
		"id":           versionID,
		"inheritsFrom": mcVersion,
		"mainClass":    "example.Main",
		"libraries":    fixture.libraries,
	}

	path := filepath.Join(t.TempDir(), "installer.jar")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	write := func(name string, body []byte) {
		entry, createErr := archive.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write(body); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	encodedProfile, _ := json.Marshal(profile)
	encodedVersion, _ := json.Marshal(version)
	write("install_profile.json", encodedProfile)
	write("version.json", encodedVersion)
	for name, body := range fixture.extraFiles {
		write(name, []byte(body))
	}
	if closeErr := archive.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return path
}

// stubProcessorJar 替换依赖解析，返回固定坐标的处理器 jar。
func stubProcessorJar(t *testing.T) {
	t.Helper()
	original := resolveProcessorJarDep
	t.Cleanup(func() { resolveProcessorJarDep = original })
	resolveProcessorJarDep = func(_ context.Context, _ string, _ Kind, _, coordinate string, _ []string) (ProcessorJar, *Error) {
		relative, err := mavenArtifactPath(coordinate)
		if err != nil {
			return ProcessorJar{}, installerError("bad coordinate", false)
		}
		return ProcessorJar{
			Coordinate:    coordinate,
			MavenBase:     "https://maven.minecraftforge.net/",
			URL:           "https://maven.minecraftforge.net/" + relative,
			SHA1:          strings.Repeat("a", 40),
			Size:          1024,
			TargetRelPath: "cache/processors/" + relative,
		}, nil
	}
}

// prepareRoot 在临时目录里放一份原版客户端 jar，满足规划前置条件。
func prepareRoot(t *testing.T, mcVersion string) (string, string) {
	t.Helper()
	root := t.TempDir()
	jarPath := filepath.Join(root, "versions", mcVersion, mcVersion+".jar")
	if err := os.MkdirAll(filepath.Dir(jarPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jarPath, []byte("vanilla-jar-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, jarPath
}

// ===== 规划 =====

// TestPlanForgeInstallResolvesAllValueForms 验证四种 data 取值形态都被正确解析。
func TestPlanForgeInstallResolvesAllValueForms(t *testing.T) {
	stubProcessorJar(t)
	root, vanillaJar := prepareRoot(t, "1.20.1")
	installer := buildInstaller(t, "1.20.1-forge-47.3.12", "1.20.1", forgeFixture{
		data: map[string]any{
			// maven 坐标 → libraries 布局
			"PATCHED": map[string]string{"client": "[com.example:patched:1.0:client]"},
			// 字面量 → 去掉引号
			"PATCHED_SHA": map[string]string{"client": "'0123456789abcdef0123456789abcdef01234567'"},
			// 安装器内文件 → 解压到临时目录
			"BINPATCH": map[string]string{"client": "/data/client.lzma"},
			// 间接引用 → 指向另一个 data 键
			"ALIAS": map[string]string{"client": "{PATCHED_SHA}"},
		},
		processors: []map[string]any{{
			"sides":   []string{"client"},
			"jar":     "com.example:patcher:1.0",
			"outputs": map[string]string{"{PATCHED}": "{ALIAS}"},
			"args":    []string{"--output", "{PATCHED}", "--apply", "{BINPATCH}", "--alias", "{ALIAS}"},
		}},
		extraFiles: map[string]string{"data/client.lzma": "lzma-bytes"},
	})

	plan, err := PlanForgeInstall(context.Background(), KindForge, root, installer, vanillaJar)
	if err != nil {
		t.Fatalf("规划失败：%s", err.Message)
	}
	defer CleanupTempDir(plan)

	if plan.VersionID != "1.20.1-forge-47.3.12" || plan.MCVersion != "1.20.1" {
		t.Fatalf("版本信息 = %q / %q", plan.VersionID, plan.MCVersion)
	}
	if len(plan.Processors) != 1 {
		t.Fatalf("processor 数量 = %d", len(plan.Processors))
	}
	step := plan.Processors[0]
	if len(step.Outputs) != 1 {
		t.Fatalf("产出数量 = %d", len(step.Outputs))
	}

	// maven 形态必须落在共享 libraries 下。
	expectedOutput := filepath.Join(root, "libraries", "com", "example", "patched", "1.0", "patched-1.0-client.jar")
	if step.Outputs[0].AbsolutePath != expectedOutput {
		t.Fatalf("产出路径 = %q，期望 %q", step.Outputs[0].AbsolutePath, expectedOutput)
	}
	// 间接引用必须解析成最终的字面量。
	if step.Outputs[0].SHA1 != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("产出哈希 = %q", step.Outputs[0].SHA1)
	}
	// 安装器内文件必须解压到临时目录且真实存在。
	var extracted string
	for index, arg := range step.Args {
		if arg == "--apply" && index+1 < len(step.Args) {
			extracted = step.Args[index+1]
		}
	}
	if !strings.HasPrefix(extracted, plan.TempDir) {
		t.Fatalf("解压路径 = %q，应位于临时目录 %q 下", extracted, plan.TempDir)
	}
	if body, readErr := os.ReadFile(extracted); readErr != nil || string(body) != "lzma-bytes" {
		t.Fatalf("解压内容 = %q, %v", body, readErr)
	}
}

// TestPlanForgeInstallSkipsServerProcessors 验证服务端 processor 被排除。
func TestPlanForgeInstallSkipsServerProcessors(t *testing.T) {
	stubProcessorJar(t)
	root, vanillaJar := prepareRoot(t, "1.20.1")
	installer := buildInstaller(t, "1.20.1-forge-47.3.12", "1.20.1", forgeFixture{
		processors: []map[string]any{
			{"sides": []string{"server"}, "jar": "com.example:server-only:1.0"},
			{"sides": []string{"client"}, "jar": "com.example:client-only:1.0"},
			// 空 sides 视为双侧，必须保留。
			{"jar": "com.example:both:1.0"},
		},
	})

	plan, err := PlanForgeInstall(context.Background(), KindForge, root, installer, vanillaJar)
	if err != nil {
		t.Fatalf("规划失败：%s", err.Message)
	}
	defer CleanupTempDir(plan)

	if len(plan.Processors) != 2 {
		t.Fatalf("processor 数量 = %d，期望 2（client + 无 sides）", len(plan.Processors))
	}
	for _, step := range plan.Processors {
		if strings.Contains(step.JarCoordinate, "server-only") {
			t.Fatalf("服务端 processor 不应进入客户端安装：%s", step.JarCoordinate)
		}
	}
}

// TestPlanForgeInstallDeduplicatesProcessorJars 验证重复坐标只解析一次。
func TestPlanForgeInstallDeduplicatesProcessorJars(t *testing.T) {
	stubProcessorJar(t)
	root, vanillaJar := prepareRoot(t, "1.20.1")
	installer := buildInstaller(t, "1.20.1-forge-47.3.12", "1.20.1", forgeFixture{
		processors: []map[string]any{
			{"sides": []string{"client"}, "jar": "com.example:a:1.0", "classpath": []string{"com.example:shared:1.0"}},
			{"sides": []string{"client"}, "jar": "com.example:b:1.0", "classpath": []string{"com.example:shared:1.0"}},
		},
	})

	plan, err := PlanForgeInstall(context.Background(), KindForge, root, installer, vanillaJar)
	if err != nil {
		t.Fatalf("规划失败：%s", err.Message)
	}
	defer CleanupTempDir(plan)

	// a + b + shared = 3，不是 4。
	if len(plan.ProcessorJars) != 3 {
		t.Fatalf("处理器依赖数量 = %d，期望 3", len(plan.ProcessorJars))
	}
	seen := map[string]bool{}
	for _, jar := range plan.ProcessorJars {
		if seen[jar.Coordinate] {
			t.Fatalf("依赖重复：%s", jar.Coordinate)
		}
		seen[jar.Coordinate] = true
	}
}

// TestPlanForgeInstallRejectsNonProcessorSpec 验证旧格式被明确拒绝。
func TestPlanForgeInstallRejectsNonProcessorSpec(t *testing.T) {
	root, vanillaJar := prepareRoot(t, "1.20.1")
	path := filepath.Join(t.TempDir(), "legacy.jar")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, _ := archive.Create("install_profile.json")
	_, _ = entry.Write([]byte(`{"spec":0,"version":"1.7.10-forge-10.13","minecraft":"1.7.10"}`))
	entry, _ = archive.Create("version.json")
	_, _ = entry.Write([]byte(`{"id":"1.7.10-forge-10.13","inheritsFrom":"1.7.10"}`))
	_ = archive.Close()
	_ = file.Close()

	_, planErr := PlanForgeInstall(context.Background(), KindForge, root, path, vanillaJar)
	if planErr == nil || !strings.Contains(planErr.Message, "旧的安装格式") {
		t.Fatalf("应拒绝旧格式，实际 %v", planErr)
	}
}

// TestPlanForgeInstallRequiresVanillaJar 验证缺少原版 jar 时提前失败。
func TestPlanForgeInstallRequiresVanillaJar(t *testing.T) {
	root, _ := prepareRoot(t, "1.20.1")
	installer := buildInstaller(t, "1.20.1-forge-47.3.12", "1.20.1", forgeFixture{})
	missing := filepath.Join(root, "versions", "1.20.1", "absent.jar")

	_, planErr := PlanForgeInstall(context.Background(), KindForge, root, installer, missing)
	if planErr == nil || !strings.Contains(planErr.Message, "原版客户端") {
		t.Fatalf("应要求原版 jar，实际 %v", planErr)
	}
}

// ===== 参数替换 =====

// TestSubstituteBuiltinsAndData 验证内置键与 data 键同时生效。
func TestSubstituteBuiltinsAndData(t *testing.T) {
	resolver := &forgeValueResolver{
		root:          "/data",
		installerPath: "/tmp/installer.jar",
		librariesDir:  "/data/libraries",
		minecraft:     "1.20.1",
		minecraftJar:  "/tmp/minecraft.jar",
		resolved:      map[string]string{"PATCHED": "/data/p.jar"},
	}

	cases := []struct{ input, want string }{
		{"{ROOT}/x", "/data/x"},
		{"{INSTALLER}", "/tmp/installer.jar"},
		{"{LIBRARY_DIR}/a.jar", "/data/libraries/a.jar"},
		{"{MINECRAFT_VERSION}", "1.20.1"},
		{"{MINECRAFT_JAR}", "/tmp/minecraft.jar"},
		{"{SIDE}", "client"},
		{"{PATCHED}", "/data/p.jar"},
		// 未知占位符必须原样保留：让 processor 自己报错比我们猜更准确。
		{"{UNKNOWN}/x", "{UNKNOWN}/x"},
		{"no-placeholder", "no-placeholder"},
	}
	for _, item := range cases {
		got, err := resolver.substitute(item.input)
		if err != nil {
			t.Fatalf("替换 %q 失败：%s", item.input, err.Message)
		}
		if got != item.want {
			t.Fatalf("替换 %q = %q，期望 %q", item.input, got, item.want)
		}
	}
}

// TestSubstituteHandlesNestedReference 验证值本身含占位符时会被继续展开。
func TestSubstituteHandlesNestedReference(t *testing.T) {
	resolver := &forgeValueResolver{
		root:     "/data",
		resolved: map[string]string{"A": "{ROOT}/nested"},
	}
	got, err := resolver.substitute("{A}/file")
	if err != nil {
		t.Fatalf("替换失败：%s", err.Message)
	}
	if got != "/data/nested/file" {
		t.Fatalf("嵌套替换 = %q", got)
	}
}

// TestDataValueRejectsCycle 验证循环引用被拒绝而不是无限递归。
func TestDataValueRejectsCycle(t *testing.T) {
	resolver := &forgeValueResolver{
		resolved:  map[string]string{},
		resolving: map[string]bool{},
		profile: forgeInstallProfile{Data: map[string]forgeProfileValue{
			"A": {Client: "{B}"},
			"B": {Client: "{A}"},
		}},
	}
	if _, err := resolver.dataValue("A"); err == nil {
		t.Fatal("循环引用应被拒绝")
	}
}

// ===== MANIFEST 解析 =====

// TestParseManifestMainClass 验证主类解析，含续行与大小写。
func TestParseManifestMainClass(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"普通", "Manifest-Version: 1.0\r\nMain-Class: example.Main\r\n", "example.Main"},
		{"无回车", "Manifest-Version: 1.0\nMain-Class: example.Main\n", "example.Main"},
		{"小写键", "main-class: example.Main\n", "example.Main"},
		// 清单续行：主类名被拆成两行时必须拼回去。
		{"续行", "Main-Class: example.really.\r\n long.Name\n", "example.really.long.Name"},
		{"缺失", "Manifest-Version: 1.0\n", ""},
		{"空", "", ""},
	}
	for _, item := range cases {
		if got := parseManifestMainClass([]byte(item.body)); got != item.want {
			t.Fatalf("%s：解析 = %q，期望 %q", item.name, got, item.want)
		}
	}
}

// ===== 计划拆分 =====

// TestProcessorAndLibraryPlans 验证两个下载计划的字段映射。
func TestProcessorAndLibraryPlans(t *testing.T) {
	plan := ForgeInstallPlan{
		VersionID: "1.20.1-forge-47.3.12",
		ProcessorJars: []ProcessorJar{{
			Coordinate:    "com.example:a:1.0",
			URL:           "https://maven.minecraftforge.net/com/example/a/1.0/a-1.0.jar",
			SHA1:          strings.Repeat("b", 40),
			Size:          2048,
			TargetRelPath: "cache/processors/com/example/a/1.0/a-1.0.jar",
		}},
		Libraries: []download.PlanItem{{TargetRelPath: "libraries/x.jar", ExpectedSize: 10}},
	}

	jarPlan := ProcessorJarPlan(plan)
	if len(jarPlan.Items) != 1 || jarPlan.Items[0].ExpectedSHA1 != strings.Repeat("b", 40) ||
		jarPlan.Items[0].TargetRelPath != "cache/processors/com/example/a/1.0/a-1.0.jar" {
		t.Fatalf("处理器计划 = %+v", jarPlan)
	}
	libPlan := LoaderLibraryPlan(plan)
	if len(libPlan.Items) != 1 || libPlan.Items[0].TargetRelPath != "libraries/x.jar" {
		t.Fatalf("库计划 = %+v", libPlan)
	}
}
