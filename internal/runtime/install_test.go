package runtime

// 本文件覆盖 P3-09 的安装、解压与选择流程。
//
// 测试约束（卡面第 11 条 + 派单硬约束）：
//   - 不访问任何真实网络：`runtimeDocumentFetcher` 与 `runtimeTransferFactory` 全部替换为
//     本地实现，manifest 与归档内容都由测试构造；
//   - 不启动任何真实 Java 进程：`runtimeLocalProbeList` 替换为空实现；
//   - 不写用户目录：数据根与数据库路径都指向 `t.TempDir()`；
//   - 不产生真实事件：`runtimeProgressSink` 替换为记录器。

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/download"
)

// runtimeTestComponent 是测试使用的官方组件名。
const runtimeTestComponent = "java-runtime-gamma"

// runtimeTestVersionName 是测试使用的官方版本名（含 `+`，用于验证标识归一化）。
const runtimeTestVersionName = "21.0.5+11"

// runtimeTestManifestHost 是测试使用的官方清单主机（真实域名，但不会真的被请求）。
const runtimeTestManifestHost = "https://launchermeta.mojang.com/v1/products/java-runtime"

// fakeRuntimeFile 描述一个测试用的 manifest 文件条目。
type fakeRuntimeFile struct {
	// Type 为 manifest 条目类型（directory / file / link）。
	Type string
	// Executable 表示该文件需要可执行位。
	Executable bool
	// Body 是该文件的内容；目录与链接为空。
	Body []byte
}

// recordedRuntimeEvent 记录一次进度事件。
type recordedRuntimeEvent struct {
	TaskID    string
	State     string
	Progress  float64
	ErrorCode string
}

// runtimeInstallFixture 是一次安装测试的环境。
type runtimeInstallFixture struct {
	t          *testing.T
	dataRoot   string
	dbPath     string
	files      map[string]fakeRuntimeFile
	events     []recordedRuntimeEvent
	transfers  int
	failOnPath string
	allPayload []byte
	docPayload []byte

	oldDataRoot     func() (string, *AppError)
	oldDBPath       func() string
	oldFetcher      runtimeDocumentFetcherFunc
	oldTransfer     func(stagingRoot string) download.TransferFunc
	oldProgressSink func(taskID, state string, progress float64, errorCode string)
	oldProbeList    func() (RuntimeList, *AppError)
}

// createRuntimeInstallDir 在 fixture 的数据根下创建 runtimes/<id>。
//
// 列表现在会校验 downloaded 记录对应的目录是否存在，目录不在就不显示。
// 这些只测选择逻辑的用例必须补一个真实目录，否则记录会被当成失效数据跳过，
// 表现就是「写进去了却选不中」。
func createRuntimeInstallDir(t *testing.T, fixture *runtimeInstallFixture, runtimeID string) {
	t.Helper()
	dir := filepath.Join(fixture.dataRoot, artifact.DirRuntimes, runtimeID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建运行时目录失败：%v", err)
	}
}

// newRuntimeInstallFixture 搭建隔离的安装环境并返回 fixture。
func newRuntimeInstallFixture(t *testing.T, files map[string]fakeRuntimeFile) *runtimeInstallFixture {
	t.Helper()

	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	if mkdirErr := os.MkdirAll(dataRoot, 0o755); mkdirErr != nil {
		t.Fatalf("准备数据根失败：%v", mkdirErr)
	}

	fixture := &runtimeInstallFixture{
		t:               t,
		dataRoot:        dataRoot,
		dbPath:          filepath.Join(root, "app.db"),
		files:           files,
		oldDataRoot:     runtimeDataRoot,
		oldDBPath:       runtimeDatabasePathResolver,
		oldFetcher:      runtimeDocumentFetcher,
		oldTransfer:     runtimeTransferFactory,
		oldProgressSink: runtimeProgressSink,
		oldProbeList:    runtimeLocalProbeList,
	}

	fixture.docPayload = buildComponentManifestPayload(t, files)
	fixture.allPayload = buildAllManifestPayload(t, fixture.docPayload, runtimeTestComponent, runtimeTestVersionName)

	runtimeDataRoot = func() (string, *AppError) { return dataRoot, nil }
	runtimeDatabasePathResolver = func() string { return fixture.dbPath }
	runtimeDocumentFetcher = func(_ context.Context, rawURL string, _ int) ([]byte, *AppError) {
		switch rawURL {
		case runtimeAllManifestURL:
			return fixture.allPayload, nil
		case runtimeComponentManifestURLForTest():
			return fixture.docPayload, nil
		default:
			return nil, newInputError(ErrorCodeRuntimeInstall, "测试未准备该地址："+rawURL)
		}
	}
	runtimeTransferFactory = func(stagingRoot string) download.TransferFunc {
		return fixture.transfer(stagingRoot)
	}
	runtimeProgressSink = func(taskID, state string, progress float64, errorCode string) {
		fixture.events = append(fixture.events, recordedRuntimeEvent{
			TaskID:    taskID,
			State:     state,
			Progress:  progress,
			ErrorCode: errorCode,
		})
	}
	runtimeLocalProbeList = func() (RuntimeList, *AppError) {
		return RuntimeList{Items: []RuntimeSummary{}}, nil
	}

	resetRuntimeDatabaseForTest(t)

	t.Cleanup(func() {
		runtimeDataRoot = fixture.oldDataRoot
		runtimeDatabasePathResolver = fixture.oldDBPath
		runtimeDocumentFetcher = fixture.oldFetcher
		runtimeTransferFactory = fixture.oldTransfer
		runtimeProgressSink = fixture.oldProgressSink
		runtimeLocalProbeList = fixture.oldProbeList
		if runtimeDB != nil {
			_ = runtimeDB.Close()
		}
		runtimeDBOnce = sync.Once{}
		runtimeDB = nil
		runtimeDBErr = nil
	})

	return fixture
}

// transfer 返回把文件写入 staging 的假传输实现。
func (f *runtimeInstallFixture) transfer(stagingRoot string) download.TransferFunc {
	return func(_ context.Context, task download.Task) download.TransferResult {
		f.transfers++
		if f.failOnPath != "" && task.TargetRelPath == f.failOnPath {
			return download.TransferResult{
				Err:       errors.New("模拟网络失败"),
				ErrorCode: download.ErrorCodeSource,
			}
		}
		file, ok := f.files[task.TargetRelPath]
		if !ok {
			return download.TransferResult{
				Err:       errors.New("测试未准备该文件"),
				ErrorCode: download.ErrorCodeSource,
			}
		}
		target := filepath.Join(stagingRoot, filepath.FromSlash(task.TargetRelPath))
		if mkdirErr := os.MkdirAll(filepath.Dir(target), 0o755); mkdirErr != nil {
			return download.TransferResult{Err: mkdirErr, ErrorCode: download.ErrorCodeCommit}
		}
		if writeErr := os.WriteFile(target, file.Body, 0o644); writeErr != nil {
			return download.TransferResult{Err: writeErr, ErrorCode: download.ErrorCodeCommit}
		}
		return download.TransferResult{
			Downloaded: int64(len(file.Body)),
			TotalSize:  task.ExpectedSize,
		}
	}
}

// finalDir 返回当前 fixture 期望的安装目录。
func (f *runtimeInstallFixture) finalDir(runtimeID string) string {
	return filepath.Join(f.dataRoot, "runtimes", runtimeID)
}

// eventStates 返回记录到的事件状态序列。
func (f *runtimeInstallFixture) eventStates() []string {
	states := make([]string, 0, len(f.events))
	for _, event := range f.events {
		states = append(states, event.State)
	}
	return states
}

// resetRuntimeDatabaseForTest 丢弃进程内缓存的数据库句柄，让下一个测试重新打开。
func resetRuntimeDatabaseForTest(t *testing.T) {
	t.Helper()
	if runtimeDB != nil {
		_ = runtimeDB.Close()
	}
	runtimeDBOnce = sync.Once{}
	runtimeDB = nil
	runtimeDBErr = nil
}

// runtimeComponentManifestURLForTest 返回 all.json 中声明的组件 manifest 地址。
func runtimeComponentManifestURLForTest() string {
	return runtimeTestManifestHost + "/" + runtimeTestComponent + "/manifest.json"
}

// sha1Hex 计算内容的 SHA-1 十六进制串。
func sha1Hex(payload []byte) string {
	sum := sha1.Sum(payload)
	return hex.EncodeToString(sum[:])
}

// buildComponentManifestPayload 生成组件 manifest 的 JSON。
func buildComponentManifestPayload(t *testing.T, files map[string]fakeRuntimeFile) []byte {
	t.Helper()
	entries := make(map[string]any, len(files))
	for relPath, file := range files {
		entry := map[string]any{"type": file.Type}
		if file.Executable {
			entry["executable"] = true
		}
		if file.Type == runtimeFileTypeFile {
			entry["downloads"] = map[string]any{
				"raw": map[string]any{
					"url":  "https://launchermeta.mojang.com/v1/raw/" + sha1Hex(file.Body),
					"sha1": sha1Hex(file.Body),
					"size": len(file.Body),
				},
			}
		}
		entries[relPath] = entry
	}
	payload, marshalErr := json.Marshal(map[string]any{"files": entries})
	if marshalErr != nil {
		t.Fatalf("构造组件清单失败：%v", marshalErr)
	}
	return payload
}

// buildAllManifestPayload 生成 all.json 的 JSON。
func buildAllManifestPayload(t *testing.T, docPayload []byte, component, versionName string) []byte {
	t.Helper()
	platformToken, ok := manifestPlatformToken()
	if !ok {
		t.Skip("当前平台不在官方清单覆盖范围内，跳过安装测试")
	}
	document := map[string]any{
		platformToken: map[string]any{
			component: []any{
				map[string]any{
					"manifest": map[string]any{
						"url":  runtimeComponentManifestURLForTest(),
						"sha1": sha1Hex(docPayload),
						"size": len(docPayload),
					},
					"version": map[string]any{"name": versionName, "released": "2025-01-01"},
				},
			},
		},
	}
	payload, marshalErr := json.Marshal(document)
	if marshalErr != nil {
		t.Fatalf("构造 all.json 失败：%v", marshalErr)
	}
	return payload
}

// defaultRuntimeFiles 是安装测试使用的一组文件。
func defaultRuntimeFiles() map[string]fakeRuntimeFile {
	return map[string]fakeRuntimeFile{
		"bin/java":      {Type: runtimeFileTypeFile, Executable: true, Body: []byte("java-binary")},
		"lib/modules":   {Type: runtimeFileTypeFile, Body: []byte("modules")},
		"release":       {Type: runtimeFileTypeFile, Body: []byte("JAVA_VERSION=\"21.0.5\"\n")},
		"legal":         {Type: runtimeFileTypeDirectory},
		"java":          {Type: runtimeFileTypeLink},
		"legal/LICENSE": {Type: runtimeFileTypeFile, Body: []byte("license")},
		"bin/java.cfg":  {Type: runtimeFileTypeFile, Body: []byte("cfg")},
	}
}

// expectedRuntimeID 返回当前平台下预期的 Runtime 标识。
func expectedRuntimeID() string {
	return runtimeTestComponent + "-" + currentPlatformName() + "-" + currentArch() + "-21.0.5-11"
}

// TestManifestPlatformTokenReturnsSupportedPlatform 验证平台键在受支持平台上非空。
func TestManifestPlatformTokenReturnsSupportedPlatform(t *testing.T) {
	token, ok := manifestPlatformToken()
	if !ok {
		t.Skip("当前平台不在官方清单覆盖范围内")
	}
	if strings.TrimSpace(token) == "" {
		t.Fatal("平台键不应为空")
	}
	if !strings.Contains(token, "-") && token != "linux" {
		t.Fatalf("平台键格式不符合预期：%s", token)
	}
}

// TestBuildRuntimeIDNormalizesVersion 验证标识拼接与字符归一化。
func TestBuildRuntimeIDNormalizesVersion(t *testing.T) {
	identifier, appErr := buildRuntimeID(runtimeTestComponent, "windows-x64", runtimeTestVersionName)
	if appErr != nil {
		t.Fatalf("拼接标识失败：%v", appErr)
	}
	if want := expectedRuntimeID(); identifier != want {
		t.Fatalf("标识 = %q，期望 %q", identifier, want)
	}
	if strings.Contains(identifier, "+") {
		t.Fatal("标识中不应出现 `+`")
	}
}

// TestBuildRuntimeIDRejectsInvalidParts 验证非法片段被拒绝。
func TestBuildRuntimeIDRejectsInvalidParts(t *testing.T) {
	cases := map[string]struct {
		component string
		version   string
	}{
		"空组件":  {component: "", version: runtimeTestVersionName},
		"空版本":  {component: runtimeTestComponent, version: ""},
		"含空格":  {component: runtimeTestComponent, version: "21.0.5 LTS"},
		"含括号":  {component: runtimeTestComponent, version: "21.0.5(1)"},
		"含斜杠":  {component: "java/runtime", version: runtimeTestVersionName},
		"含反斜杠": {component: "java\\runtime", version: runtimeTestVersionName},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if _, appErr := buildRuntimeID(testCase.component, "windows-x64", testCase.version); appErr == nil {
				t.Fatal("期望返回错误，实际通过")
			} else if appErr.Code != ErrorCodeRuntimeInstall {
				t.Fatalf("错误码 = %s，期望 %s", appErr.Code, ErrorCodeRuntimeInstall)
			}
		})
	}
}

// TestSanitizeIDPart 验证片段归一化。
func TestSanitizeIDPart(t *testing.T) {
	cases := map[string]string{
		"Java-Runtime-Gamma": "java-runtime-gamma",
		"21.0.5+11":          "21.0.5-11",
		"  17.0.9  ":         "17.0.9",
		"a_b.c":              "a_b.c",
		"":                   "",
		"a b":                "",
		"a/b":                "",
	}
	for input, want := range cases {
		if got := sanitizeIDPart(input); got != want {
			t.Fatalf("sanitizeIDPart(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestIsRuntimeIDCharsetValid 验证字符集判定。
func TestIsRuntimeIDCharsetValid(t *testing.T) {
	if !isRuntimeIDCharsetValid("java-runtime-gamma-windows-x64-21.0.5-11") {
		t.Fatal("合法标识被误判为非法")
	}
	for _, invalid := range []string{"", "ABC", "a b", "a/b", "a+b"} {
		if isRuntimeIDCharsetValid(invalid) {
			t.Fatalf("非法标识 %q 被误判为合法", invalid)
		}
	}
}

// TestStripQuery 验证查询串与片段被剔除。
func TestStripQuery(t *testing.T) {
	cases := map[string]string{
		"https://launchermeta.mojang.com/a.json":            "https://launchermeta.mojang.com/a.json",
		"https://launchermeta.mojang.com/a.json?sig=secret": "https://launchermeta.mojang.com/a.json",
		"https://launchermeta.mojang.com/a.json#frag":       "https://launchermeta.mojang.com/a.json",
		"  https://launchermeta.mojang.com/a.json  ":        "https://launchermeta.mojang.com/a.json",
		"": "",
	}
	for input, want := range cases {
		if got := stripQuery(input); got != want {
			t.Fatalf("stripQuery(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestParseAllManifest 验证 all.json 的解析与拒绝。
func TestParseAllManifest(t *testing.T) {
	if _, appErr := parseAllManifest(nil); appErr == nil {
		t.Fatal("空内容应被拒绝")
	}
	if _, appErr := parseAllManifest([]byte("{")); appErr == nil {
		t.Fatal("非法 JSON 应被拒绝")
	}
	if _, appErr := parseAllManifest([]byte("{}")); appErr == nil {
		t.Fatal("空对象应被拒绝")
	}
	payload := []byte(`{"linux":{"java-runtime-gamma":[{"manifest":{"url":"https://launchermeta.mojang.com/x","sha1":"aa","size":1},"version":{"name":"21"}}]}}`)
	all, appErr := parseAllManifest(payload)
	if appErr != nil {
		t.Fatalf("合法内容解析失败：%v", appErr)
	}
	if len(all) != 1 {
		t.Fatalf("平台数量 = %d，期望 1", len(all))
	}
}

// TestSelectManifestEntry 验证条目挑选。
func TestSelectManifestEntry(t *testing.T) {
	all := runtimeAllManifest{
		"windows-x64": {
			runtimeTestComponent: []runtimeManifestEntry{
				{Manifest: runtimeManifestDownload{URL: "https://launchermeta.mojang.com/m", SHA1: "aa", Size: 10}, Version: struct {
					Name     string `json:"name"`
					Released string `json:"released"`
				}{Name: "21.0.5"}},
			},
		},
	}
	entry, ok := selectManifestEntry(all, "windows-x64", runtimeTestComponent)
	if !ok {
		t.Fatal("应命中条目")
	}
	if entry.Version.Name != "21.0.5" {
		t.Fatalf("版本名 = %q", entry.Version.Name)
	}
	if _, ok := selectManifestEntry(all, "linux", runtimeTestComponent); ok {
		t.Fatal("平台缺失时应失败")
	}
	if _, ok := selectManifestEntry(all, "windows-x64", "java-runtime-alpha"); ok {
		t.Fatal("组件缺失时应失败")
	}
	broken := runtimeAllManifest{"windows-x64": {runtimeTestComponent: []runtimeManifestEntry{{Manifest: runtimeManifestDownload{Size: 0}}}}}
	if _, ok := selectManifestEntry(broken, "windows-x64", runtimeTestComponent); ok {
		t.Fatal("缺少地址或大小应失败")
	}
}

// TestVerifyManifestDocument 验证组件清单的 size/sha1 校验。
func TestVerifyManifestDocument(t *testing.T) {
	payload := []byte(`{"files":{}}`)
	good := runtimeManifestDownload{SHA1: sha1Hex(payload), Size: int64(len(payload))}
	if appErr := verifyManifestDocument(payload, good); appErr != nil {
		t.Fatalf("合法内容被拒绝：%v", appErr)
	}
	if appErr := verifyManifestDocument(payload, runtimeManifestDownload{SHA1: sha1Hex(payload), Size: 999}); appErr == nil {
		t.Fatal("大小不符应被拒绝")
	}
	if appErr := verifyManifestDocument(payload, runtimeManifestDownload{SHA1: strings.Repeat("0", 40), Size: int64(len(payload))}); appErr == nil {
		t.Fatal("校验值不符应被拒绝")
	}
	empty := runtimeManifestDownload{}
	if appErr := verifyManifestDocument(payload, empty); appErr != nil {
		t.Fatalf("未声明大小与校验值时应放行：%v", appErr)
	}
}

// TestParseComponentManifest 验证组件清单解析。
func TestParseComponentManifest(t *testing.T) {
	if _, appErr := parseComponentManifest([]byte("{}")); appErr == nil {
		t.Fatal("缺少 files 应被拒绝")
	}
	if _, appErr := parseComponentManifest([]byte("{")); appErr == nil {
		t.Fatal("非法 JSON 应被拒绝")
	}
	files, appErr := parseComponentManifest([]byte(`{"files":{"bin/java":{"type":"file","downloads":{"raw":{"url":"https://launchermeta.mojang.com/j","sha1":"aa","size":3}}}}}`))
	if appErr != nil {
		t.Fatalf("合法清单解析失败：%v", appErr)
	}
	if _, ok := files["bin/java"]; !ok {
		t.Fatal("应包含 bin/java")
	}
}

// TestInspectManifestFilesCountsAndLimits 验证统计与上限。
func TestInspectManifestFilesCountsAndLimits(t *testing.T) {
	files := runtimeManifestFiles{
		"bin/java":    {Type: runtimeFileTypeFile, Downloads: runtimeManifestDownloads{Raw: &runtimeManifestDownload{URL: "https://launchermeta.mojang.com/j", SHA1: "aa", Size: 10}}},
		"lib/modules": {Type: runtimeFileTypeFile, Downloads: runtimeManifestDownloads{Raw: &runtimeManifestDownload{URL: "https://launchermeta.mojang.com/m", SHA1: "bb", Size: 20}}},
		"legal":       {Type: runtimeFileTypeDirectory},
		"java":        {Type: runtimeFileTypeLink},
	}
	stats, appErr := inspectManifestFiles(files)
	if appErr != nil {
		t.Fatalf("统计失败：%v", appErr)
	}
	if stats.fileCount != 2 || stats.totalBytes != 30 {
		t.Fatalf("统计 = %d 个文件 / %d 字节，期望 2 / 30", stats.fileCount, stats.totalBytes)
	}
	if stats.vendor != VendorOpenJDK {
		t.Fatalf("厂商 = %q，期望 %q", stats.vendor, VendorOpenJDK)
	}
}

// TestInspectManifestFilesRejectsInvalidEntries 验证非法条目被拒绝。
func TestInspectManifestFilesRejectsInvalidEntries(t *testing.T) {
	cases := map[string]runtimeManifestFiles{
		"缺下载信息": {"bin/java": {Type: runtimeFileTypeFile}},
		"未知类型":  {"bin/java": {Type: "socket"}},
		"路径穿越":  {"../escape": {Type: runtimeFileTypeFile, Downloads: runtimeManifestDownloads{Raw: &runtimeManifestDownload{Size: 1}}}},
		"空路径":   {"": {Type: runtimeFileTypeFile, Downloads: runtimeManifestDownloads{Raw: &runtimeManifestDownload{Size: 1}}}},
		"大小为零":  {"bin/java": {Type: runtimeFileTypeFile, Downloads: runtimeManifestDownloads{Raw: &runtimeManifestDownload{Size: 0}}}},
		"只有目录":  {"legal": {Type: runtimeFileTypeDirectory}},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if _, appErr := inspectManifestFiles(files); appErr == nil {
				t.Fatal("期望返回错误，实际通过")
			}
		})
	}
}

// TestInspectManifestFilesEnforcesLimits 验证文件数、总字节与单文件上限。
func TestInspectManifestFilesEnforcesLimits(t *testing.T) {
	tooMany := make(runtimeManifestFiles, runtimeMaxFiles+1)
	for index := 0; index <= runtimeMaxFiles; index++ {
		relPath := "file-" + strings.Repeat("x", 1) + string(rune('a'+index%26)) + "-" + formatIndex(index)
		tooMany[relPath] = runtimeManifestFile{Type: runtimeFileTypeFile, Downloads: runtimeManifestDownloads{Raw: &runtimeManifestDownload{Size: 1}}}
	}
	if _, appErr := inspectManifestFiles(tooMany); appErr == nil {
		t.Fatal("超文件数应被拒绝")
	} else if appErr.Code != ErrorCodeRuntimeUnpack {
		t.Fatalf("错误码 = %s，期望 %s", appErr.Code, ErrorCodeRuntimeUnpack)
	}

	tooLarge := runtimeManifestFiles{
		"a": {Type: runtimeFileTypeFile, Downloads: runtimeManifestDownloads{Raw: &runtimeManifestDownload{Size: runtimeMaxFileBytes}}},
		"b": {Type: runtimeFileTypeFile, Downloads: runtimeManifestDownloads{Raw: &runtimeManifestDownload{Size: runtimeMaxFileBytes}}},
		"c": {Type: runtimeFileTypeFile, Downloads: runtimeManifestDownloads{Raw: &runtimeManifestDownload{Size: runtimeMaxFileBytes}}},
	}
	if _, appErr := inspectManifestFiles(tooLarge); appErr == nil {
		t.Fatal("超总字节应被拒绝")
	}

	oversized := runtimeManifestFiles{
		"a": {Type: runtimeFileTypeFile, Downloads: runtimeManifestDownloads{Raw: &runtimeManifestDownload{Size: runtimeMaxFileBytes + 1}}},
	}
	if _, appErr := inspectManifestFiles(oversized); appErr == nil {
		t.Fatal("超单文件上限应被拒绝")
	}
}

// formatIndex 把数字格式化为十进制串（避免多余的 import）。
func formatIndex(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// TestInstallRuntimeRejectsMissingLicense 验证未接受许可时直接拒绝。
func TestInstallRuntimeRejectsMissingLicense(t *testing.T) {
	fixture := newRuntimeInstallFixture(t, defaultRuntimeFiles())
	_, appErr := installRuntime(context.Background(), InstallRuntimeRequest{
		Component:     runtimeTestComponent,
		AcceptLicense: false,
	})
	if appErr == nil {
		t.Fatal("未接受许可时应失败")
	}
	if appErr.Code != ErrorCodeRuntimeInstall {
		t.Fatalf("错误码 = %s，期望 %s", appErr.Code, ErrorCodeRuntimeInstall)
	}
	if !strings.Contains(appErr.Message, "许可") {
		t.Fatalf("文案应提到许可：%s", appErr.Message)
	}
	if fixture.transfers != 0 {
		t.Fatal("拒绝许可后不应发起任何下载")
	}
}

// TestInstallRuntimeRejectsMissingComponent 验证缺少组件名时拒绝。
func TestInstallRuntimeRejectsMissingComponent(t *testing.T) {
	newRuntimeInstallFixture(t, defaultRuntimeFiles())
	_, appErr := installRuntime(context.Background(), InstallRuntimeRequest{AcceptLicense: true})
	if appErr == nil {
		t.Fatal("缺少组件名时应失败")
	}
	if appErr.Code != ErrorCodeRuntimeInstall {
		t.Fatalf("错误码 = %s", appErr.Code)
	}
}

// TestInstallRuntimeRejectsUnknownComponent 验证官方清单没有该组件时返回未找到。
func TestInstallRuntimeRejectsUnknownComponent(t *testing.T) {
	fixture := newRuntimeInstallFixture(t, defaultRuntimeFiles())
	fixture.allPayload = buildAllManifestPayload(t, []byte(`{"files":{}}`), "java-runtime-alpha", runtimeTestVersionName)
	_, appErr := installRuntime(context.Background(), InstallRuntimeRequest{
		Component:     runtimeTestComponent,
		AcceptLicense: true,
	})
	if appErr == nil {
		t.Fatal("组件缺失时应失败")
	}
	if appErr.Code != ErrorCodeRuntimeNotFound {
		t.Fatalf("错误码 = %s，期望 %s", appErr.Code, ErrorCodeRuntimeNotFound)
	}
}

// TestInstallRuntimeSucceeds 验证完整安装流程。
func TestInstallRuntimeSucceeds(t *testing.T) {
	fixture := newRuntimeInstallFixture(t, defaultRuntimeFiles())
	accepted, appErr := installRuntime(context.Background(), InstallRuntimeRequest{
		Component:     runtimeTestComponent,
		AcceptLicense: true,
	})
	if appErr != nil {
		t.Fatalf("安装失败：%v", appErr)
	}
	runtimeID := expectedRuntimeID()
	if accepted.TaskID != runtimeEventTaskPrefix+runtimeID {
		t.Fatalf("taskId = %q", accepted.TaskID)
	}
	if accepted.State != runtimeEventStateCompleted || accepted.Duplicate {
		t.Fatalf("结果 = %+v", accepted)
	}

	finalDir := fixture.finalDir(runtimeID)
	for _, relPath := range []string{"bin/java", "lib/modules", "release", "legal/LICENSE", "bin/java.cfg"} {
		if _, statErr := os.Stat(filepath.Join(finalDir, filepath.FromSlash(relPath))); statErr != nil {
			t.Fatalf("安装结果缺少 %s：%v", relPath, statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(finalDir, "java")); statErr == nil {
		t.Fatal("链接条目不应落盘")
	}
	if _, statErr := os.Stat(filepath.Join(fixture.dataRoot, "runtimes", runtimeStagingPrefix+runtimeID)); statErr == nil {
		t.Fatal("提交后暂存目录不应保留")
	}

	content, readErr := os.ReadFile(filepath.Join(finalDir, "release"))
	if readErr != nil {
		t.Fatalf("读取安装文件失败：%v", readErr)
	}
	if string(content) != string(fixture.files["release"].Body) {
		t.Fatalf("文件内容 = %q", string(content))
	}

	rows, listErr := installedRuntimeRows(context.Background())
	if listErr != nil {
		t.Fatalf("读取记录失败：%v", listErr)
	}
	if len(rows) != 1 {
		t.Fatalf("记录条数 = %d，期望 1", len(rows))
	}
	row := rows[0]
	if row.ID != runtimeID || row.Kind != SourceDownloaded || row.Component != runtimeTestComponent {
		t.Fatalf("记录 = %+v", row)
	}
	if row.MajorVersion != 21 {
		t.Fatalf("主版本 = %d，期望 21", row.MajorVersion)
	}
	if row.FullVersion != runtimeTestVersionName {
		t.Fatalf("完整版本 = %q", row.FullVersion)
	}
	if row.Selected {
		t.Fatal("新安装的 Runtime 不应自动选中")
	}
	if row.LocalRelPath != "runtimes/"+runtimeID {
		t.Fatalf("相对路径 = %q", row.LocalRelPath)
	}
	if strings.Contains(row.SourceURI, "?") {
		t.Fatal("来源地址不应包含查询串")
	}

	states := fixture.eventStates()
	// 期望：下载开始（0%）→ 下载完成（100%，由 done==total 触发）→ 提交中 → 完成。
	// 中间是否出现额外的 downloading 取决于节流窗口，因此只断言首尾与顺序。
	if len(states) < 3 {
		t.Fatalf("事件过少：%v", states)
	}
	if states[0] != runtimeEventStateDownloading {
		t.Fatalf("首个事件 = %q，期望 %q", states[0], runtimeEventStateDownloading)
	}
	if states[len(states)-2] != runtimeEventStateCommitting {
		t.Fatalf("倒数第二个事件 = %q，期望 %q", states[len(states)-2], runtimeEventStateCommitting)
	}
	if states[len(states)-1] != runtimeEventStateCompleted {
		t.Fatalf("最后一个事件 = %q，期望 %q", states[len(states)-1], runtimeEventStateCompleted)
	}
	for index := 1; index < len(states)-2; index++ {
		if states[index] != runtimeEventStateDownloading {
			t.Fatalf("中间事件 = %q，期望 %q", states[index], runtimeEventStateDownloading)
		}
	}
	if fixture.events[0].TaskID != runtimeEventTaskPrefix+runtimeID {
		t.Fatalf("事件 taskId = %q", fixture.events[0].TaskID)
	}
}

// TestInstallRuntimeIsIdempotent 验证已安装且校验通过时不重装。
func TestInstallRuntimeIsIdempotent(t *testing.T) {
	fixture := newRuntimeInstallFixture(t, defaultRuntimeFiles())
	first, appErr := installRuntime(context.Background(), InstallRuntimeRequest{
		Component:     runtimeTestComponent,
		AcceptLicense: true,
	})
	if appErr != nil {
		t.Fatalf("首次安装失败：%v", appErr)
	}
	transfersAfterFirst := fixture.transfers
	if transfersAfterFirst == 0 {
		t.Fatal("首次安装应发生下载")
	}
	fixture.events = nil

	second, appErr := installRuntime(context.Background(), InstallRuntimeRequest{
		Component:     runtimeTestComponent,
		AcceptLicense: true,
	})
	if appErr != nil {
		t.Fatalf("重复安装失败：%v", appErr)
	}
	if !second.Duplicate {
		t.Fatal("重复安装应标记 Duplicate=true")
	}
	if second.TaskID != first.TaskID {
		t.Fatalf("taskId 变化：%q → %q", first.TaskID, second.TaskID)
	}
	if fixture.transfers != transfersAfterFirst {
		t.Fatalf("重复安装不应再下载，下载次数 %d → %d", transfersAfterFirst, fixture.transfers)
	}
	states := fixture.eventStates()
	if len(states) != 1 || states[0] != runtimeEventStateCompleted {
		t.Fatalf("跳过安装应只发 completed 事件，实际 %v", states)
	}
}

// TestInstallRuntimeQuarantinesCorruptDirectory 验证损坏目录被隔离后重装。
func TestInstallRuntimeQuarantinesCorruptDirectory(t *testing.T) {
	fixture := newRuntimeInstallFixture(t, defaultRuntimeFiles())
	if _, appErr := installRuntime(context.Background(), InstallRuntimeRequest{
		Component:     runtimeTestComponent,
		AcceptLicense: true,
	}); appErr != nil {
		t.Fatalf("首次安装失败：%v", appErr)
	}

	runtimeID := expectedRuntimeID()
	finalDir := fixture.finalDir(runtimeID)
	if writeErr := os.WriteFile(filepath.Join(finalDir, "release"), []byte("corrupted"), 0o644); writeErr != nil {
		t.Fatalf("破坏安装目录失败：%v", writeErr)
	}

	accepted, appErr := installRuntime(context.Background(), InstallRuntimeRequest{
		Component:     runtimeTestComponent,
		AcceptLicense: true,
	})
	if appErr != nil {
		t.Fatalf("重装失败：%v", appErr)
	}
	if accepted.Duplicate {
		t.Fatal("损坏后应重新安装而不是跳过")
	}
	content, readErr := os.ReadFile(filepath.Join(finalDir, "release"))
	if readErr != nil {
		t.Fatalf("读取重装文件失败：%v", readErr)
	}
	if string(content) != string(fixture.files["release"].Body) {
		t.Fatalf("重装内容 = %q", string(content))
	}

	entries, readDirErr := os.ReadDir(filepath.Join(fixture.dataRoot, "runtimes"))
	if readDirErr != nil {
		t.Fatalf("读取 runtimes 目录失败：%v", readDirErr)
	}
	foundQuarantine := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), runtimeQuarantinePrefix+runtimeID) {
			foundQuarantine = true
		}
	}
	if !foundQuarantine {
		t.Fatal("损坏目录应被移入隔离区")
	}
}

// TestInstallRuntimeCleansStagingOnFailure 验证下载失败时清理暂存目录并保留空记录。
func TestInstallRuntimeCleansStagingOnFailure(t *testing.T) {
	fixture := newRuntimeInstallFixture(t, defaultRuntimeFiles())
	fixture.failOnPath = "lib/modules"

	_, appErr := installRuntime(context.Background(), InstallRuntimeRequest{
		Component:     runtimeTestComponent,
		AcceptLicense: true,
	})
	if appErr == nil {
		t.Fatal("下载失败时应返回错误")
	}
	runtimeID := expectedRuntimeID()
	stagingDir := filepath.Join(fixture.dataRoot, "runtimes", runtimeStagingPrefix+runtimeID)
	if _, statErr := os.Stat(stagingDir); statErr == nil {
		t.Fatal("失败后暂存目录应被清理")
	}
	if _, statErr := os.Stat(fixture.finalDir(runtimeID)); statErr == nil {
		t.Fatal("失败后不应存在安装目录")
	}
	rows, listErr := installedRuntimeRows(context.Background())
	if listErr != nil {
		t.Fatalf("读取记录失败：%v", listErr)
	}
	if len(rows) != 0 {
		t.Fatalf("失败安装不应落库，实际 %d 条", len(rows))
	}
	states := fixture.eventStates()
	if len(states) == 0 || states[len(states)-1] != runtimeEventStateFailed {
		t.Fatalf("失败时应以 failed 事件结束，实际 %v", states)
	}
}

// TestInstallRuntimeRejectsManifestChecksumMismatch 验证组件清单校验失败。
func TestInstallRuntimeRejectsManifestChecksumMismatch(t *testing.T) {
	fixture := newRuntimeInstallFixture(t, defaultRuntimeFiles())
	fixture.allPayload = buildAllManifestPayload(t, []byte(`{"files":{"bin/java":{"type":"file"}}}`), runtimeTestComponent, runtimeTestVersionName)

	_, appErr := installRuntime(context.Background(), InstallRuntimeRequest{
		Component:     runtimeTestComponent,
		AcceptLicense: true,
	})
	if appErr == nil {
		t.Fatal("清单大小不符时应失败")
	}
	if appErr.Code != ErrorCodeRuntimeInstall {
		t.Fatalf("错误码 = %s", appErr.Code)
	}
	if fixture.transfers != 0 {
		t.Fatal("清单校验失败时不应下载任何文件")
	}
}

// TestInstallRuntimeRejectsFileTooLarge 验证零件清单中超过单文件上限时拒绝。
func TestInstallRuntimeRejectsFileTooLarge(t *testing.T) {
	files := map[string]fakeRuntimeFile{
		"bin/java": {Type: runtimeFileTypeFile, Body: []byte("java")},
	}
	fixture := newRuntimeInstallFixture(t, files)
	oversized := map[string]any{
		"files": map[string]any{
			"bin/java": map[string]any{
				"type": "file",
				"downloads": map[string]any{"raw": map[string]any{
					"url":  "https://launchermeta.mojang.com/v1/raw/big",
					"sha1": strings.Repeat("a", 40),
					"size": runtimeMaxFileBytes + 1,
				}},
			},
		},
	}
	payload, marshalErr := json.Marshal(oversized)
	if marshalErr != nil {
		t.Fatalf("构造清单失败：%v", marshalErr)
	}
	fixture.docPayload = payload
	fixture.allPayload = buildAllManifestPayload(t, payload, runtimeTestComponent, runtimeTestVersionName)

	_, appErr := installRuntime(context.Background(), InstallRuntimeRequest{
		Component:     runtimeTestComponent,
		AcceptLicense: true,
	})
	if appErr == nil {
		t.Fatal("超单文件上限应失败")
	}
	if appErr.Code != ErrorCodeRuntimeUnpack {
		t.Fatalf("错误码 = %s，期望 %s", appErr.Code, ErrorCodeRuntimeUnpack)
	}
	if fixture.transfers != 0 {
		t.Fatal("超限时不应下载任何文件")
	}
}

// TestInstallRuntimeRejectsTotalTooLarge 验证总字节超限时拒绝。
func TestInstallRuntimeRejectsTotalTooLarge(t *testing.T) {
	files := map[string]fakeRuntimeFile{"bin/java": {Type: runtimeFileTypeFile, Body: []byte("java")}}
	fixture := newRuntimeInstallFixture(t, files)
	entries := map[string]any{}
	for _, name := range []string{"a", "b", "c"} {
		entries[name] = map[string]any{
			"type": "file",
			"downloads": map[string]any{"raw": map[string]any{
				"url":  "https://launchermeta.mojang.com/v1/raw/" + name,
				"sha1": strings.Repeat("a", 40),
				"size": runtimeMaxFileBytes,
			}},
		}
	}
	payload, marshalErr := json.Marshal(map[string]any{"files": entries})
	if marshalErr != nil {
		t.Fatalf("构造清单失败：%v", marshalErr)
	}
	fixture.docPayload = payload
	fixture.allPayload = buildAllManifestPayload(t, payload, runtimeTestComponent, runtimeTestVersionName)

	_, appErr := installRuntime(context.Background(), InstallRuntimeRequest{
		Component:     runtimeTestComponent,
		AcceptLicense: true,
	})
	if appErr == nil {
		t.Fatal("超总字节应失败")
	}
	if appErr.Code != ErrorCodeRuntimeUnpack {
		t.Fatalf("错误码 = %s", appErr.Code)
	}
}

// TestListRuntimesMergesInstalled 验证列表合并且不触发真实探测。
func TestListRuntimesMergesInstalled(t *testing.T) {
	fixture := newRuntimeInstallFixture(t, defaultRuntimeFiles())
	if _, appErr := installRuntime(context.Background(), InstallRuntimeRequest{
		Component:     runtimeTestComponent,
		AcceptLicense: true,
	}); appErr != nil {
		t.Fatalf("安装失败：%v", appErr)
	}

	list, appErr := listRuntimesWithInstalled()
	if appErr != nil {
		t.Fatalf("列表失败：%v", appErr)
	}
	if len(list.Items) != 1 {
		t.Fatalf("列表条数 = %d，期望 1", len(list.Items))
	}
	if list.Items[0].Source != SourceDownloaded {
		t.Fatalf("来源 = %q，期望 %q", list.Items[0].Source, SourceDownloaded)
	}
	if list.SelectedRuntimeID != "" {
		t.Fatalf("未选中时 selectedRuntimeId 应为空，实际 %q", list.SelectedRuntimeID)
	}
	if list.Items[0].RuntimeID != expectedRuntimeID() {
		t.Fatalf("标识 = %q", list.Items[0].RuntimeID)
	}
	_ = fixture
}

// TestSelectRuntimeKeepsSingleSelected 验证选中唯一性。
func TestSelectRuntimeKeepsSingleSelected(t *testing.T) {
	fixture := newRuntimeInstallFixture(t, defaultRuntimeFiles())
	ctx := context.Background()
	nowMs := runtimeSelectionNowMs()
	firstID := "local-current-17.0.9"
	secondID := "local-current-21.0.5"
	for _, row := range []runtimeRow{
		{ID: firstID, Kind: SourceDownloaded, Component: "", MajorVersion: 17, FullVersion: "17.0.9", Platform: currentPlatformName(), Arch: currentArch(), Bitness: bitnessValue(currentBitness()), Vendor: VendorOpenJDK, CreatedAtMs: nowMs},
		{ID: secondID, Kind: SourceDownloaded, Component: "", MajorVersion: 21, FullVersion: "21.0.5", Platform: currentPlatformName(), Arch: currentArch(), Bitness: bitnessValue(currentBitness()), Vendor: VendorOpenJDK, CreatedAtMs: nowMs},
	} {
		createRuntimeInstallDir(t, fixture, row.ID)
		if saveErr := saveRuntime(ctx, row); saveErr != nil {
			t.Fatalf("写入记录失败：%v", saveErr)
		}
	}

	list, appErr := SelectRuntime(SelectRuntimeRequest{RuntimeID: firstID})
	if appErr != nil {
		t.Fatalf("选中失败：%v", appErr)
	}
	if list.SelectedRuntimeID != firstID {
		t.Fatalf("选中标识 = %q，期望 %q", list.SelectedRuntimeID, firstID)
	}

	if _, appErr := SelectRuntime(SelectRuntimeRequest{RuntimeID: secondID}); appErr != nil {
		t.Fatalf("二次选中失败：%v", appErr)
	}
	rows, listErr := installedRuntimeRows(ctx)
	if listErr != nil {
		t.Fatalf("读取记录失败：%v", listErr)
	}
	selectedCount := 0
	for _, row := range rows {
		if row.Selected {
			selectedCount++
			if row.ID != secondID {
				t.Fatalf("选中的是 %q，期望 %q", row.ID, secondID)
			}
		}
	}
	if selectedCount != 1 {
		t.Fatalf("选中行数 = %d，期望 1", selectedCount)
	}
	_ = fixture
}

// TestSelectRuntimeRejectsInvalidRequests 验证选择请求的拒绝路径。
func TestSelectRuntimeRejectsInvalidRequests(t *testing.T) {
	newRuntimeInstallFixture(t, defaultRuntimeFiles())
	if _, appErr := SelectRuntime(SelectRuntimeRequest{}); appErr == nil {
		t.Fatal("空标识应失败")
	} else if appErr.Code != ErrorCodeRuntimeRequirement {
		t.Fatalf("错误码 = %s", appErr.Code)
	}
	if _, appErr := SelectRuntime(SelectRuntimeRequest{RuntimeID: "missing-runtime"}); appErr == nil {
		t.Fatal("不存在的标识应失败")
	} else if appErr.Code != ErrorCodeRuntimeNotFound {
		t.Fatalf("错误码 = %s", appErr.Code)
	}
}

// TestSelectRuntimeRejectsIncompatibleUnlessForced 验证不兼容时需 force。
func TestSelectRuntimeRejectsIncompatibleUnlessForced(t *testing.T) {
	fixture := newRuntimeInstallFixture(t, defaultRuntimeFiles())
	ctx := context.Background()
	incompatibleArch := ArchAarch64
	if currentArch() == ArchAarch64 {
		incompatibleArch = ArchX64
	}
	createRuntimeInstallDir(t, fixture, "local-other-arch-21.0.5")
	if saveErr := saveRuntime(ctx, runtimeRow{
		ID:           "local-other-arch-21.0.5",
		Kind:         SourceDownloaded,
		MajorVersion: 21,
		FullVersion:  "21.0.5",
		Platform:     "windows",
		Arch:         incompatibleArch,
		Bitness:      64,
		Vendor:       VendorOpenJDK,
		CreatedAtMs:  runtimeSelectionNowMs(),
	}); saveErr != nil {
		t.Fatalf("写入记录失败：%v", saveErr)
	}

	if _, appErr := SelectRuntime(SelectRuntimeRequest{RuntimeID: "local-other-arch-21.0.5"}); appErr == nil {
		t.Fatal("不兼容且未强制时应失败")
	} else if appErr.Code != ErrorCodeRuntimeRequirement {
		t.Fatalf("错误码 = %s", appErr.Code)
	}
	list, appErr := SelectRuntime(SelectRuntimeRequest{RuntimeID: "local-other-arch-21.0.5", Force: true})
	if appErr != nil {
		t.Fatalf("强制选中失败：%v", appErr)
	}
	if list.SelectedRuntimeID != "local-other-arch-21.0.5" {
		t.Fatalf("选中标识 = %q", list.SelectedRuntimeID)
	}
}

// TestRuntimeStagingSinkWritesUnderRoot 验证暂存写入器只写根内路径。
func TestRuntimeStagingSinkWritesUnderRoot(t *testing.T) {
	root := t.TempDir()
	sink := &runtimeStagingSink{stagingRoot: root}
	writer, openErr := sink.Open(context.Background(), download.Task{TargetRelPath: "bin/java"}, 0, true)
	if openErr != nil {
		t.Fatalf("打开暂存写入器失败：%v", openErr)
	}
	if _, writeErr := writer.Write([]byte("data")); writeErr != nil {
		t.Fatalf("写入失败：%v", writeErr)
	}
	if flushErr := writer.Flush(); flushErr != nil {
		t.Fatalf("刷新失败：%v", flushErr)
	}
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatalf("关闭失败：%v", closeErr)
	}
	content, readErr := os.ReadFile(filepath.Join(root, "bin", "java"))
	if readErr != nil {
		t.Fatalf("读取暂存文件失败：%v", readErr)
	}
	if string(content) != "data" {
		t.Fatalf("内容 = %q", string(content))
	}
	if _, openErr := sink.Open(context.Background(), download.Task{TargetRelPath: "../escape"}, 0, true); openErr == nil {
		t.Fatal("越界路径应被拒绝")
	}
}

// TestUnpackTargetPath 验证解压目标路径的防护。
func TestUnpackTargetPath(t *testing.T) {
	root := t.TempDir()
	target, appErr := unpackTargetPath(root, "bin/java")
	if appErr != nil {
		t.Fatalf("合法路径失败：%v", appErr)
	}
	if !strings.HasPrefix(target, root) {
		t.Fatalf("目标不在根内：%s", target)
	}
	for _, invalid := range []string{"", "../escape", "/abs", "C:/abs"} {
		if _, appErr := unpackTargetPath(root, invalid); appErr == nil {
			t.Fatalf("非法路径 %q 应被拒绝", invalid)
		}
	}
}

// TestUnpackEntryPath 验证归档条目名归一化。
func TestUnpackEntryPath(t *testing.T) {
	relPath, appErr := unpackEntryPath("dir/")
	if appErr != nil {
		t.Fatalf("目录条目应被接受：%v", appErr)
	}
	if relPath != "dir" {
		t.Fatalf("归一化结果 = %q", relPath)
	}
	if _, appErr := unpackEntryPath("../escape.txt"); appErr == nil {
		t.Fatal("路径穿越应被拒绝")
	}
	if _, appErr := unpackEntryPath(""); appErr == nil {
		t.Fatal("空条目应被拒绝")
	}
}

// writeTestZip 生成测试归档。
func writeTestZip(t *testing.T, path string, build func(writer *zip.Writer)) {
	t.Helper()
	file, createErr := os.Create(path)
	if createErr != nil {
		t.Fatalf("创建归档失败：%v", createErr)
	}
	defer func() { _ = file.Close() }()
	writer := zip.NewWriter(file)
	build(writer)
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatalf("关闭归档失败：%v", closeErr)
	}
}

// TestUnpackZipExtractsFiles 验证正常解压与可执行位。
func TestUnpackZipExtractsFiles(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "runtime.zip")
	dest := filepath.Join(dir, "out")
	writeTestZip(t, zipPath, func(writer *zip.Writer) {
		for name, body := range map[string]string{
			"bin/java":     "java",
			"lib/modules":  "modules",
			"legal/README": "readme",
		} {
			entry, createErr := writer.Create(name)
			if createErr != nil {
				t.Fatalf("写入条目 %s 失败：%v", name, createErr)
			}
			if _, writeErr := entry.Write([]byte(body)); writeErr != nil {
				t.Fatalf("写入条目内容失败：%v", writeErr)
			}
		}
	})

	report, appErr := UnpackZip(zipPath, dest, UnpackOptions{})
	if appErr != nil {
		t.Fatalf("解压失败：%v", appErr)
	}
	if report.Files != 3 {
		t.Fatalf("文件数 = %d，期望 3", report.Files)
	}
	content, readErr := os.ReadFile(filepath.Join(dest, "bin", "java"))
	if readErr != nil {
		t.Fatalf("读取解压文件失败：%v", readErr)
	}
	if string(content) != "java" {
		t.Fatalf("内容 = %q", string(content))
	}
}

func TestUnpackArchiveExtractsTarGz(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "runtime.tar.gz")
	dest := filepath.Join(dir, "out")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gz)
	body := []byte("java")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "jdk-21/bin/java", Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	report, appErr := UnpackArchive(archivePath, dest, UnpackOptions{})
	if appErr != nil {
		t.Fatalf("tar.gz 解压失败：%v", appErr)
	}
	if report.Files != 1 {
		t.Fatalf("文件数 = %d，期望 1", report.Files)
	}
	content, err := os.ReadFile(filepath.Join(dest, "jdk-21", "bin", "java"))
	if err != nil || string(content) != "java" {
		t.Fatalf("解压内容异常：%q, %v", content, err)
	}
}

// TestUnpackZipRejectsZipSlip 验证路径穿越被拒绝。
func TestUnpackZipRejectsZipSlip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "escape.zip")
	dest := filepath.Join(dir, "out")
	writeTestZip(t, zipPath, func(writer *zip.Writer) {
		entry, _ := writer.Create("../escape.txt")
		_, _ = entry.Write([]byte("evil"))
	})
	if _, appErr := UnpackZip(zipPath, dest, UnpackOptions{}); appErr == nil {
		t.Fatal("路径穿越应被拒绝")
	} else if appErr.Code != ErrorCodeRuntimeUnpack {
		t.Fatalf("错误码 = %s", appErr.Code)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "escape.txt")); statErr == nil {
		t.Fatal("越界文件不应落盘")
	}
}

// TestUnpackZipRejectsAbsoluteEntry 验证绝对路径条目被拒绝。
func TestUnpackZipRejectsAbsoluteEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "absolute.zip")
	writeTestZip(t, zipPath, func(writer *zip.Writer) {
		entry, _ := writer.Create("/absolute.txt")
		_, _ = entry.Write([]byte("evil"))
	})
	if _, appErr := UnpackZip(zipPath, filepath.Join(dir, "out"), UnpackOptions{}); appErr == nil {
		t.Fatal("绝对路径条目应被拒绝")
	}
}

// TestUnpackZipSkipsSymlinkEntries 验证符号链接条目被跳过并记警告。
func TestUnpackZipSkipsSymlinkEntries(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "link.zip")
	dest := filepath.Join(dir, "out")
	writeTestZip(t, zipPath, func(writer *zip.Writer) {
		header := &zip.FileHeader{Name: "java", Method: zip.Store}
		header.SetMode(os.ModeSymlink | 0o777)
		entry, createErr := writer.CreateHeader(header)
		if createErr != nil {
			t.Fatalf("写入链接条目失败：%v", createErr)
		}
		_, _ = entry.Write([]byte("bin/java"))
	})
	report, appErr := UnpackZip(zipPath, dest, UnpackOptions{})
	if appErr != nil {
		t.Fatalf("链接条目不应导致失败：%v", appErr)
	}
	if report.SkippedLinks != 1 {
		t.Fatalf("跳过链接数 = %d，期望 1", report.SkippedLinks)
	}
	if len(report.Warnings) != 1 || report.Warnings[0].Code != WarningUnpackSkippedLink {
		t.Fatalf("警告 = %+v", report.Warnings)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "java")); statErr == nil {
		t.Fatal("链接条目不应落盘")
	}
}

// TestUnpackZipRejectsTooManyFiles 验证文件数上限。
func TestUnpackZipRejectsTooManyFiles(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "many.zip")
	writeTestZip(t, zipPath, func(writer *zip.Writer) {
		for _, name := range []string{"a", "b", "c"} {
			entry, _ := writer.Create(name)
			_, _ = entry.Write([]byte("x"))
		}
	})
	if _, appErr := UnpackZip(zipPath, filepath.Join(dir, "out"), UnpackOptions{MaxFiles: 2}); appErr == nil {
		t.Fatal("超文件数应被拒绝")
	} else if appErr.Code != ErrorCodeRuntimeUnpack {
		t.Fatalf("错误码 = %s", appErr.Code)
	}
}

// TestUnpackZipRejectsOversizedFile 验证单文件上限。
func TestUnpackZipRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "big.zip")
	writeTestZip(t, zipPath, func(writer *zip.Writer) {
		entry, _ := writer.Create("big.bin")
		_, _ = entry.Write([]byte("0123456789"))
	})
	if _, appErr := UnpackZip(zipPath, filepath.Join(dir, "out"), UnpackOptions{MaxFileBytes: 4}); appErr == nil {
		t.Fatal("超单文件上限应被拒绝")
	}
}

// TestUnpackZipRejectsCompressionBomb 验证压缩比上限。
func TestUnpackZipRejectsCompressionBomb(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bomb.zip")
	writeTestZip(t, zipPath, func(writer *zip.Writer) {
		entry, _ := writer.Create("bomb.bin")
		_, _ = entry.Write([]byte(strings.Repeat("A", 2<<20)))
	})
	if _, appErr := UnpackZip(zipPath, filepath.Join(dir, "out"), UnpackOptions{}); appErr == nil {
		t.Fatal("压缩比超限应被拒绝")
	} else if appErr.Code != ErrorCodeRuntimeUnpack {
		t.Fatalf("错误码 = %s", appErr.Code)
	}
}

// TestUnpackZipRejectsRelativeDestination 验证解压目标必须是绝对路径。
func TestUnpackZipRejectsRelativeDestination(t *testing.T) {
	if _, appErr := UnpackZip(filepath.Join(t.TempDir(), "missing.zip"), "relative/out", UnpackOptions{}); appErr == nil {
		t.Fatal("相对目标目录应被拒绝")
	}
}

// TestUnpackZipRejectsUnreadableArchive 验证归档不可读时的错误。
func TestUnpackZipRejectsUnreadableArchive(t *testing.T) {
	if _, appErr := UnpackZip(filepath.Join(t.TempDir(), "missing.zip"), filepath.Join(t.TempDir(), "out"), UnpackOptions{}); appErr == nil {
		t.Fatal("不存在的归档应报错")
	}
}
