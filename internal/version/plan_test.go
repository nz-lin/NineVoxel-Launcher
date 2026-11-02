// 本文件是确定性 InstallPlan 构建的单元测试。
//
// 测试约束（P3-02 卡面）：
//   - **绝不联网**：全部 fixture 写在 t.TempDir() 中；
//   - 只验证解析与计划生成，不下载任何制品、不写数据库；
//   - plan_hash 的确定性是本卡的核心断言点。
package version

import (
	"strings"
	"testing"
)

// planFixtureDocument 构造一份覆盖全部可用来源的版本元数据。
//
// 覆盖：client、assetIndex、library、natives classifier、logging、
// javaVersion 与 mainClass。
func planFixtureDocument(id string) map[string]any {
	document := minimalVersionDocument(id)
	document["libraries"] = []any{
		libraryDocument("org.example:plain:1.0", sha1LibraryA, 100),
		nativesLibraryDocument("org.example:native:1.0", "natives-windows", sha1Native),
	}
	document["logging"] = map[string]any{
		"client": map[string]any{
			"argument": "-Dlog4j.configurationFile=${path}",
			"file": map[string]any{
				"id":   "client-1.12.xml",
				"sha1": sha1LogConfig,
				"size": 256,
				"url":  officialArtifactURL("piston-meta.mojang.com", sha1LogConfig),
			},
		},
	}
	return document
}

// setupPlanFixture 写入标准计划 fixture 并返回数据根。
func setupPlanFixture(t *testing.T) string {
	t.Helper()

	root := setupTestDataRoot(t)
	writeVersionMetadata(t, root, "1.20.1", planFixtureDocument("1.20.1"))
	return root
}

// findPlanArtifact 按 kind 与逻辑键前缀查找制品。
func findPlanArtifact(t *testing.T, plan *installPlan, kind, logicalKeyPrefix string) planArtifact {
	t.Helper()

	for _, item := range plan.Artifacts {
		if item.Kind == kind && strings.HasPrefix(item.LogicalKey, logicalKeyPrefix) {
			return item
		}
	}
	t.Fatalf("未找到制品 kind=%s logicalKey 前缀=%s，实际 %+v", kind, logicalKeyPrefix, plan.Artifacts)
	return planArtifact{}
}

// TestBuildInstallPlanIsDeterministic 验证同一输入两次构建产生相同 plan_hash。
func TestBuildInstallPlanIsDeterministic(t *testing.T) {
	root := setupPlanFixture(t)

	first, appErr := buildInstallPlan(root, "1.20.1")
	if appErr != nil {
		t.Fatalf("首次构建失败: %v", appErr)
	}
	second, appErr := buildInstallPlan(root, "1.20.1")
	if appErr != nil {
		t.Fatalf("二次构建失败: %v", appErr)
	}

	if first.PlanHash == "" {
		t.Fatalf("plan_hash 不应为空")
	}
	if first.PlanHash != second.PlanHash {
		t.Fatalf("同一输入两次构建的 plan_hash 应相同: %s != %s", first.PlanHash, second.PlanHash)
	}
	if len(first.PlanHash) != 64 {
		t.Fatalf("plan_hash 应为 64 位十六进制，实际 %q", first.PlanHash)
	}
	if first.ResolverVersion != ResolverVersion {
		t.Fatalf("解析器版本错误: %q", first.ResolverVersion)
	}
	if first.VersionID != "1.20.1" {
		t.Fatalf("版本标识错误: %q", first.VersionID)
	}
}

// TestBuildInstallPlanCoversAllSources 验证五类制品与 Java 要求被登记。
func TestBuildInstallPlanCoversAllSources(t *testing.T) {
	root := setupPlanFixture(t)

	plan, appErr := buildInstallPlan(root, "1.20.1")
	if appErr != nil {
		t.Fatalf("构建失败: %v", appErr)
	}

	// client + assetIndex + library + native + logging。
	if plan.ArtifactCount != 5 || len(plan.Artifacts) != 5 {
		t.Fatalf("制品数量应为 5，实际 count=%d len=%d: %+v",
			plan.ArtifactCount, len(plan.Artifacts), plan.Artifacts)
	}
	if plan.TotalBytes != 2048+1024+100+512+256 {
		t.Fatalf("制品总大小错误: %d", plan.TotalBytes)
	}

	client := findPlanArtifact(t, plan, "client", "client/")
	if !strings.HasPrefix(client.TargetRelPath, "versions/1.20.1/") {
		t.Fatalf("客户端 jar 目标路径应在 versions/<id>/ 下: %q", client.TargetRelPath)
	}
	if client.SHA1 != sha1Client || !client.Immutable {
		t.Fatalf("客户端制品字段错误: %+v", client)
	}

	assetIndex := findPlanArtifact(t, plan, "asset", "assetIndex/")
	if assetIndex.TargetRelPath != "artifacts/indexes/5.json" {
		t.Fatalf("资源索引目标路径错误: %q", assetIndex.TargetRelPath)
	}

	library := findPlanArtifact(t, plan, "library", "org.example:plain")
	if library.TargetRelPath != "artifacts/objects/aa/"+sha1LibraryA {
		t.Fatalf("库目标路径应为内容寻址: %q", library.TargetRelPath)
	}

	native := findPlanArtifact(t, plan, "native", "org.example:native")
	if !native.Extract {
		t.Fatalf("本地库应标记为需要解压: %+v", native)
	}
	if native.NativesClassifier != "natives-windows" {
		t.Fatalf("classifier 错误: %q", native.NativesClassifier)
	}
	if len(native.ExtractExclude) != 2 {
		t.Fatalf("extract.exclude 应原样保留，实际 %+v", native.ExtractExclude)
	}
	if native.TargetRelPath != "artifacts/objects/cc/"+sha1Native {
		t.Fatalf("本地库目标路径错误: %q", native.TargetRelPath)
	}

	logging := findPlanArtifact(t, plan, "logging", "logConfig/")
	if logging.TargetRelPath != "artifacts/objects/dd/"+sha1LogConfig {
		t.Fatalf("日志配置目标路径错误: %q", logging.TargetRelPath)
	}

	// 制品必须按 targetRelPath 稳定排序。
	for index := 1; index < len(plan.Artifacts); index++ {
		if plan.Artifacts[index-1].TargetRelPath > plan.Artifacts[index].TargetRelPath {
			t.Fatalf("制品未按 targetRelPath 排序: %q > %q",
				plan.Artifacts[index-1].TargetRelPath, plan.Artifacts[index].TargetRelPath)
		}
	}

	if plan.JavaVersion.Major != 17 || plan.JavaVersion.Source != "metadata" {
		t.Fatalf("Java 版本要求错误: %+v", plan.JavaVersion)
	}
	if plan.Status != "launch_supported" {
		t.Fatalf("状态应为 launch_supported，实际 %q", plan.Status)
	}
	if !plan.Compatibility.MetadataSupported || !plan.Compatibility.InstallSupported ||
		!plan.Compatibility.LaunchSupported || plan.Compatibility.LoaderSupported {
		t.Fatalf("四层兼容状态错误: %+v", plan.Compatibility)
	}
	if plan.MetadataSize <= 0 {
		t.Fatalf("应记录版本 JSON 大小，实际 %d", plan.MetadataSize)
	}
}

// TestBuildInstallPlanHashChangesWithArtifact 验证改动制品字段会改变 plan_hash。
func TestBuildInstallPlanHashChangesWithArtifact(t *testing.T) {
	root := setupPlanFixture(t)

	baseline, appErr := buildInstallPlan(root, "1.20.1")
	if appErr != nil {
		t.Fatalf("基准构建失败: %v", appErr)
	}

	// 修改普通的库条目大小：只有 size 变化。
	document := planFixtureDocument("1.20.1")
	document["libraries"] = []any{
		libraryDocument("org.example:plain:1.0", sha1LibraryA, 101),
		nativesLibraryDocument("org.example:native:1.0", "natives-windows", sha1Native),
	}
	writeVersionMetadata(t, root, "1.20.1", document)

	changed, appErr := buildInstallPlan(root, "1.20.1")
	if appErr != nil {
		t.Fatalf("改动后构建失败: %v", appErr)
	}
	if changed.PlanHash == baseline.PlanHash {
		t.Fatalf("制品大小变化后 plan_hash 应改变")
	}

	// 修改版本 JSON 但只改 generatedAtMs 之外的元数据无关字段不影响哈希：
	// 用两次构建同一份内容验证（generatedAtMs 天然可能不同）。
	again, appErr := buildInstallPlan(root, "1.20.1")
	if appErr != nil {
		t.Fatalf("复核构建失败: %v", appErr)
	}
	if again.PlanHash != changed.PlanHash {
		t.Fatalf("同一内容重复构建 plan_hash 应稳定: %s != %s", again.PlanHash, changed.PlanHash)
	}
}

// TestPlanHashExcludesGeneratedAtAndSourceURI 验证哈希排除生成时间与来源地址。
func TestPlanHashExcludesGeneratedAtAndSourceURI(t *testing.T) {
	root := setupPlanFixture(t)

	plan, appErr := buildInstallPlan(root, "1.20.1")
	if appErr != nil {
		t.Fatalf("构建失败: %v", appErr)
	}
	baseline := computePlanHash(plan)

	// 生成时间不参与哈希。
	mutated := *plan
	mutated.GeneratedAtMs = plan.GeneratedAtMs + 12345
	if computePlanHash(&mutated) != baseline {
		t.Fatalf("generatedAtMs 不应参与 plan_hash")
	}

	// 来源地址不参与哈希：把每个制品的 sourceUri 换掉（模拟镜像改写前的原文）。
	mutated = *plan
	mutated.Artifacts = make([]planArtifact, len(plan.Artifacts))
	copy(mutated.Artifacts, plan.Artifacts)
	for index := range mutated.Artifacts {
		mutated.Artifacts[index].SourceURI = "https://launcher.mojang.com/v1/other/" + mutated.Artifacts[index].SHA1
	}
	if computePlanHash(&mutated) != baseline {
		t.Fatalf("sourceUri 不应参与 plan_hash")
	}
}

// TestBuildInstallPlanMissingClientIsUnsupported 验证缺客户端 jar 时的降级。
func TestBuildInstallPlanMissingClientIsUnsupported(t *testing.T) {
	root := setupTestDataRoot(t)

	document := planFixtureDocument("no-client")
	document["downloads"] = map[string]any{}
	writeVersionMetadata(t, root, "no-client", document)

	plan, appErr := buildInstallPlan(root, "no-client")
	if appErr != nil {
		t.Fatalf("缺客户端 jar 不应返回致命错误: %v", appErr)
	}
	requireWarningCode(t, plan.Warnings, WarningClientDownloadMissing)
	if plan.Compatibility.InstallSupported {
		t.Fatalf("缺客户端 jar 时不应标记为可安装")
	}
	if plan.Status != "unsupported" {
		t.Fatalf("状态应为 unsupported，实际 %q", plan.Status)
	}
	for _, item := range plan.Artifacts {
		if item.Kind == "client" {
			t.Fatalf("不应登记客户端制品: %+v", item)
		}
	}
}

// TestBuildInstallPlanMissingAssetIndexIsUnsupported 验证缺资源索引时的降级。
func TestBuildInstallPlanMissingAssetIndexIsUnsupported(t *testing.T) {
	root := setupTestDataRoot(t)

	document := planFixtureDocument("no-index")
	delete(document, "assetIndex")
	writeVersionMetadata(t, root, "no-index", document)

	plan, appErr := buildInstallPlan(root, "no-index")
	if appErr != nil {
		t.Fatalf("缺资源索引不应返回致命错误: %v", appErr)
	}
	requireWarningCode(t, plan.Warnings, WarningAssetIndexMissing)
	if plan.Compatibility.InstallSupported || plan.Status != "unsupported" {
		t.Fatalf("缺资源索引时应为 unsupported: %+v / %q", plan.Compatibility, plan.Status)
	}
}

// TestBuildInstallPlanSkipsMaliciousLibrary 验证恶意库条目被跳过而非整单失败。
func TestBuildInstallPlanSkipsMaliciousLibrary(t *testing.T) {
	root := setupTestDataRoot(t)

	document := minimalVersionDocument("evil-lib")
	document["libraries"] = []any{
		map[string]any{
			"name": "org.example:plain-http:1.0",
			"downloads": map[string]any{
				"artifact": map[string]any{
					"path": "org/example/plain-http-1.0.jar",
					"sha1": sha1LibraryA,
					"size": 100,
					"url":  "http://libraries.minecraft.net/org/example/plain-http-1.0.jar",
				},
			},
		},
		map[string]any{
			"name": "org.example:plain-bad-sha:1.0",
			"downloads": map[string]any{
				"artifact": map[string]any{
					"path": "org/example/plain-bad-sha-1.0.jar",
					"sha1": "not-a-sha1",
					"size": 100,
					"url":  officialArtifactURL("libraries.minecraft.net", sha1LibraryB),
				},
			},
		},
		libraryDocument("org.example:ok:1.0", sha1LibraryB, 200),
	}
	writeVersionMetadata(t, root, "evil-lib", document)

	plan, appErr := buildInstallPlan(root, "evil-lib")
	if appErr != nil {
		t.Fatalf("恶意库条目不应使整单失败: %v", appErr)
	}
	skipped := 0
	for _, warning := range plan.Warnings {
		if warning.Code == WarningLibrarySkipped {
			skipped++
		}
	}
	if skipped != 2 {
		t.Fatalf("应跳过 2 个非法库条目，实际 %d: %+v", skipped, plan.Warnings)
	}
	if len(plan.Artifacts) != 3 {
		// client + assetIndex + 1 个合法库。
		t.Fatalf("只应保留 client、assetIndex 与 1 个合法库，实际 %+v", plan.Artifacts)
	}
	findPlanArtifact(t, plan, "library", "org.example:ok")
}

// TestBuildInstallPlanUnknownJavaVersion 验证缺 Java 版本时不猜测。
func TestBuildInstallPlanUnknownJavaVersion(t *testing.T) {
	root := setupTestDataRoot(t)

	document := planFixtureDocument("no-java")
	delete(document, "javaVersion")
	writeVersionMetadata(t, root, "no-java", document)

	plan, appErr := buildInstallPlan(root, "no-java")
	if appErr != nil {
		t.Fatalf("构建失败: %v", appErr)
	}
	if plan.JavaVersion.Major != 0 || plan.JavaVersion.Source != "unknown" {
		t.Fatalf("缺 Java 版本时应置 0 且来源为 unknown: %+v", plan.JavaVersion)
	}
	requireWarningCode(t, plan.Warnings, WarningJavaVersionUnknown)
}

// TestBuildInstallPlanWithoutMainClassIsMetadataOnly 验证缺 mainClass 时不可启动。
func TestBuildInstallPlanWithoutMainClassIsMetadataOnly(t *testing.T) {
	root := setupTestDataRoot(t)

	document := planFixtureDocument("no-main")
	delete(document, "mainClass")
	writeVersionMetadata(t, root, "no-main", document)

	plan, appErr := buildInstallPlan(root, "no-main")
	if appErr != nil {
		t.Fatalf("构建失败: %v", appErr)
	}
	if !plan.Compatibility.InstallSupported {
		t.Fatalf("缺 mainClass 不影响安装能力: %+v", plan.Compatibility)
	}
	if plan.Compatibility.LaunchSupported {
		t.Fatalf("缺 mainClass 时不应标记为可启动")
	}
	if plan.Status != "metadata_only" {
		t.Fatalf("状态应为 metadata_only，实际 %q", plan.Status)
	}
}

// TestBuildInstallPlanRejectsBrokenInheritChain 验证继承链成环返回 VERSION_JSON_001。
func TestBuildInstallPlanRejectsBrokenInheritChain(t *testing.T) {
	root := setupTestDataRoot(t)

	a := minimalVersionDocument("a")
	a["inheritsFrom"] = "b"
	b := minimalVersionDocument("b")
	b["inheritsFrom"] = "a"
	writeVersionMetadata(t, root, "a", a)
	writeVersionMetadata(t, root, "b", b)

	if _, appErr := buildInstallPlan(root, "a"); appErr == nil {
		t.Fatalf("成环版本应返回错误")
	} else {
		requireCode(t, appErr, ErrorCodeVersionJSON)
	}
}

// TestInstallPlanDTOHidesSourceURI 验证对外 DTO 不暴露来源地址。
func TestInstallPlanDTOHidesSourceURI(t *testing.T) {
	root := setupPlanFixture(t)

	plan, appErr := buildInstallPlan(root, "1.20.1")
	if appErr != nil {
		t.Fatalf("构建失败: %v", appErr)
	}
	dto := plan.toDTO()

	if dto.Summary.PlanHash != plan.PlanHash || dto.Summary.VersionID != "1.20.1" {
		t.Fatalf("计划摘要错误: %+v", dto.Summary)
	}
	if dto.Summary.ResolverVersion != ResolverVersion {
		t.Fatalf("摘要解析器版本错误: %q", dto.Summary.ResolverVersion)
	}
	if len(dto.Artifacts) != len(plan.Artifacts) {
		t.Fatalf("DTO 制品数量错误: %d", len(dto.Artifacts))
	}
	for _, item := range dto.Artifacts {
		if item.SourceURI != "" {
			t.Fatalf("对外 DTO 不应包含来源地址: %+v", item)
		}
		if item.SourceID == "" {
			t.Fatalf("对外 DTO 应保留来源标识: %+v", item)
		}
		if item.TargetRelPath == "" || strings.HasPrefix(item.TargetRelPath, "/") {
			t.Fatalf("目标路径必须是相对路径: %q", item.TargetRelPath)
		}
		if item.Hashes.SHA1 == "" {
			t.Fatalf("应包含 SHA-1 摘要: %+v", item)
		}
	}
}

// TestGetVersionDetailReturnsPlanSummary 验证详情接口返回计划摘要与兼容状态。
func TestGetVersionDetailReturnsPlanSummary(t *testing.T) {
	setupPlanFixture(t)

	detail, appErr := getVersionDetail(VersionDetailRequest{VersionID: "1.20.1"})
	if appErr != nil {
		t.Fatalf("获取版本详情失败: %v", appErr)
	}
	if detail.ID != "1.20.1" || detail.Type != "release" {
		t.Fatalf("版本标识或类型错误: %+v", detail)
	}
	if detail.Size <= 0 {
		t.Fatalf("应返回元数据大小，实际 %d", detail.Size)
	}
	if detail.Plan.ArtifactCount != 5 || detail.Plan.PlanHash == "" {
		t.Fatalf("计划摘要错误: %+v", detail.Plan)
	}
	if detail.JavaRequirement.Major != 17 {
		t.Fatalf("Java 要求错误: %+v", detail.JavaRequirement)
	}
	if detail.Compat.Status != "launch_supported" {
		t.Fatalf("兼容状态错误: %+v", detail.Compat)
	}
}

// TestResolveInstallPlanRequiresDataRoot 验证数据根不可用时的错误码。
func TestResolveInstallPlanRequiresDataRoot(t *testing.T) {
	SetDataRootProvider(nil)
	t.Cleanup(func() { SetDataRootProvider(nil) })

	if _, appErr := resolveInstallPlan(ResolveInstallPlanRequest{VersionID: "1.20.1"}); appErr == nil {
		t.Fatalf("数据根未注入时应返回错误")
	} else {
		requireCode(t, appErr, ErrorCodeDataRootUnavailable)
	}

	if _, appErr := getVersionDetail(VersionDetailRequest{VersionID: "1.20.1"}); appErr == nil {
		t.Fatalf("数据根未注入时应返回错误")
	} else {
		requireCode(t, appErr, ErrorCodeDataRootUnavailable)
	}
}

// TestResolveInstallPlanEmptyVersionID 验证空版本 id 被拒绝。
func TestResolveInstallPlanEmptyVersionID(t *testing.T) {
	setupPlanFixture(t)

	if _, appErr := resolveInstallPlan(ResolveInstallPlanRequest{}); appErr == nil {
		t.Fatalf("空版本 id 应返回错误")
	} else {
		requireCode(t, appErr, ErrorCodeVersionJSON)
	}
}

// TestBuildInstallPlanInheritChainKeepsChildLibraries 验证继承链下的计划确定性。
//
// 子版本与父版本各声明一个库，合并后应同时出现在计划中；
// 同坐标库只保留子版本内容（与 P2 合并规则一致）。
func TestBuildInstallPlanInheritChainKeepsChildLibraries(t *testing.T) {
	root := setupTestDataRoot(t)

	child := minimalVersionDocument("forge-child")
	child["inheritsFrom"] = "vanilla"
	child["libraries"] = []any{
		libraryDocument("org.example:shared:1.0", sha1LibraryA, 100),
	}
	parent := planFixtureDocument("vanilla")
	writeVersionMetadata(t, root, "forge-child", child)
	writeVersionMetadata(t, root, "vanilla", parent)

	plan, appErr := buildInstallPlan(root, "forge-child")
	if appErr != nil {
		t.Fatalf("构建失败: %v", appErr)
	}

	shared := 0
	for _, item := range plan.Artifacts {
		if strings.HasPrefix(item.LogicalKey, "org.example:shared") {
			shared++
			if item.SHA1 != sha1LibraryA {
				t.Fatalf("同坐标库应保留子版本内容: %+v", item)
			}
		}
	}
	if shared != 1 {
		t.Fatalf("同坐标库应只登记一次，实际 %d 次", shared)
	}
	// 父版本的普通库、本地库、日志配置仍然保留。
	findPlanArtifact(t, plan, "library", "org.example:plain")
	findPlanArtifact(t, plan, "native", "org.example:native")
	findPlanArtifact(t, plan, "logging", "logConfig/")

	// 再构建一次应得到相同哈希。
	again, appErr := buildInstallPlan(root, "forge-child")
	if appErr != nil {
		t.Fatalf("复核构建失败: %v", appErr)
	}
	if again.PlanHash != plan.PlanHash {
		t.Fatalf("继承链场景下 plan_hash 应稳定: %s != %s", plan.PlanHash, again.PlanHash)
	}
}
