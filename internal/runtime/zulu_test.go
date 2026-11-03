package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

const zuluTestUUID = "f2caf8b1-ed9c-4405-9145-06f58c4fc129"

// TestFetchZuluReleaseSelectsPlainJdk 验证从多个 latest 变体中挑选普通 JDK。
func TestFetchZuluReleaseSelectsPlainJdk(t *testing.T) {
	checksum := strings.Repeat("a", 64)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasPrefix(req.URL.Path, "/metadata/v1/zulu/packages/") && req.URL.Path == "/metadata/v1/zulu/packages/":
			if got := req.URL.Query().Get("java_version"); got != "21" {
				t.Fatalf("java_version = %q，期望 21", got)
			}
			body, _ := json.Marshal([]map[string]any{
				{
					"package_uuid": "fx-uuid",
					"name":         "zulu21.52.203-ca-fx-jdk21.0.12.1-macosx_aarch64.tar.gz",
					"product":      "zulu",
					"java_version": []int{21, 0, 12, 1},
				},
				{
					"package_uuid": zuluTestUUID,
					"name":         "zulu21.54.109-ca-jdk21.0.13.0.3-macosx_aarch64.tar.gz",
					"product":      "zulu",
					"java_version": []int{21, 0, 13, 0, 3},
				},
			})
			return adoptiumTestResponse(req, http.StatusOK, body), nil
		case req.Method == http.MethodHead:
			response := adoptiumTestResponse(req, http.StatusOK, nil)
			response.ContentLength = 205779381
			return response, nil
		case req.URL.Path == "/metadata/v1/zulu/packages/"+zuluTestUUID:
			body, _ := json.Marshal(map[string]any{
				"package_uuid": zuluTestUUID,
				"name":         "zulu21.54.109-ca-jdk21.0.13.0.3-macosx_aarch64.tar.gz",
				"download_url": "https://cdn.azul.com/zulu/bin/zulu21.54.109-ca-jdk21.0.13.0.3-macosx_aarch64.tar.gz",
				"sha256_hash":  checksum,
				"size":         205779000,
				"java_version": []int{21, 0, 13, 0, 3},
			})
			return adoptiumTestResponse(req, http.StatusOK, body), nil
		default:
			return adoptiumTestResponse(req, http.StatusNotFound, nil), nil
		}
	})}

	release, appErr := fetchZuluRelease(context.Background(), client, 21, "darwin", "arm64")
	if appErr != nil {
		t.Fatalf("获取 Zulu 版本失败：%v", appErr)
	}
	if release.Major != 21 {
		t.Errorf("主版本 = %d，期望 21", release.Major)
	}
	if release.SHA256 != checksum {
		t.Errorf("SHA-256 = %q，期望 %q", release.SHA256, checksum)
	}
	if release.SizeBytes != 205779381 {
		t.Errorf("大小 = %d，期望 205779381（HEAD 的真实 Content-Length）", release.SizeBytes)
	}
	if !strings.Contains(release.FileName, "-ca-jdk") || strings.Contains(release.FileName, "-fx-") {
		t.Errorf("文件名 = %q，期望普通 JDK 包", release.FileName)
	}
	if release.FullVersion != "21.0.13.0.3" {
		t.Errorf("完整版本 = %q，期望 21.0.13.0.3", release.FullVersion)
	}
	if release.ExecutableRel != "bin/java" {
		t.Errorf("可执行文件 = %q，期望 bin/java", release.ExecutableRel)
	}
}

// TestFetchZuluReleaseRejectsBadChecksum 验证校验值非法时中止。
func TestFetchZuluReleaseRejectsBadChecksum(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/metadata/v1/zulu/packages/" {
			body, _ := json.Marshal([]map[string]any{
				{"package_uuid": zuluTestUUID, "name": "zulu21.54.109-ca-jdk21.0.13.0.3-macosx_aarch64.tar.gz", "product": "zulu"},
			})
			return adoptiumTestResponse(req, http.StatusOK, body), nil
		}
		body, _ := json.Marshal(map[string]any{
			"download_url": "https://cdn.azul.com/zulu/bin/zulu21.54.109-ca-jdk21.0.13.0.3-macosx_aarch64.tar.gz",
			"sha256_hash":  "not-a-checksum",
			"size":         1,
		})
		return adoptiumTestResponse(req, http.StatusOK, body), nil
	})}

	_, appErr := fetchZuluRelease(context.Background(), client, 21, "darwin", "arm64")
	requireCode(t, appErr, ErrorCodeRuntimeSource)
}

// TestFetchZuluReleaseRejectsUnsupportedPlatform 验证不支持的平台组合被拒绝。
func TestFetchZuluReleaseRejectsUnsupportedPlatform(t *testing.T) {
	_, appErr := fetchZuluRelease(context.Background(), &http.Client{}, 21, "plan9", "mips64")
	requireCode(t, appErr, ErrorCodeRuntimeRequirement)
}

// TestResolveRuntimeSource 验证来源注册表与未知发行版拒绝。
func TestResolveRuntimeSource(t *testing.T) {
	for _, vendor := range []string{"", "adoptium", "zulu"} {
		spec, appErr := resolveRuntimeSource(vendor)
		if appErr != nil {
			t.Fatalf("来源 %q 解析失败：%v", vendor, appErr)
		}
		if spec.Name == "" {
			t.Fatalf("来源 %q 的 Name 为空", vendor)
		}
	}

	_, appErr := resolveRuntimeSource("unknown-vendor")
	requireCode(t, appErr, ErrorCodeRuntimeSource)
}

// TestValidateZuluDownloadURL 验证归档地址 host 白名单。
func TestValidateZuluDownloadURL(t *testing.T) {
	cases := []struct {
		url  string
		ok   bool
		name string
	}{
		{url: "https://cdn.azul.com/zulu/bin/a.tar.gz", ok: true, name: "CDN"},
		{url: "https://api.azul.com/zulu/bin/a.tar.gz", ok: true, name: "API"},
		{url: "https://example.com/a.tar.gz", ok: false, name: "白名单外"},
		{url: "http://cdn.azul.com/a.tar.gz", ok: false, name: "非 HTTPS"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			appErr := validateZuluDownloadURL(testCase.url)
			if testCase.ok && appErr != nil {
				t.Fatalf("期望通过，实际失败：%v", appErr)
			}
			if !testCase.ok && appErr == nil {
				t.Fatal("期望拒绝，实际通过")
			}
		})
	}
}

// TestZuluVersionString 验证版本数组拼接。
func TestZuluVersionString(t *testing.T) {
	if got := zuluVersionString([]int{21, 0, 13, 0, 3}); got != "21.0.13.0.3" {
		t.Fatalf("版本串 = %q", got)
	}
	if got := zuluVersionString(nil); got != "" {
		t.Fatalf("空数组应返回空串，实际 %q", got)
	}
}

// TestFetchZuluLtsVersions 验证 Zulu 的 LTS 列表来自 support_term=lts 查询。
func TestFetchZuluLtsVersions(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.Query().Get("support_term"); got != "lts" {
			t.Fatalf("support_term = %q，期望 lts", got)
		}
		body, _ := json.Marshal([]map[string]any{
			{"name": "zulu25.0.1-ca-jdk25.0.1-macosx_aarch64.tar.gz", "product": "zulu", "java_version": []int{25, 0, 1}},
			{"name": "zulu21.54.109-ca-jdk21.0.13.0.3-macosx_aarch64.tar.gz", "product": "zulu", "java_version": []int{21, 0, 13, 0, 3}},
			{"name": "zulu21.52.203-ca-fx-jdk21.0.12.1-macosx_aarch64.tar.gz", "product": "zulu", "java_version": []int{21, 0, 12, 1}},
			{"name": "zulu17.60.19-ca-jdk17.0.17-macosx_aarch64.tar.gz", "product": "zulu", "java_version": []int{17, 0, 17}},
			{"name": "zulu11.86.19-ca-jdk11.0.29-macosx_aarch64.tar.gz", "product": "zulu", "java_version": []int{11, 0, 29}},
			{"name": "zulu8.92.0.21-ca-jdk8.0.482-macosx_aarch64.tar.gz", "product": "zulu", "java_version": []int{8, 0, 482}},
		})
		return adoptiumTestResponse(req, http.StatusOK, body), nil
	})}

	versions, appErr := fetchZuluLtsVersions(context.Background(), client, "darwin", "arm64")
	if appErr != nil {
		t.Fatalf("获取 Zulu LTS 失败：%v", appErr)
	}
	want := []int{25, 21, 17, 11, 8}
	if fmt.Sprint(versions.Versions) != fmt.Sprint(want) {
		t.Fatalf("LTS 列表 = %v，期望 %v", versions.Versions, want)
	}
	if versions.MostRecentLts != 25 {
		t.Fatalf("最新 LTS = %d，期望 25", versions.MostRecentLts)
	}
}

// TestRuntimeHomeDirectory 验证 macOS 的 Contents/Home 会被提升为运行时根。
func TestRuntimeHomeDirectory(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "Contents", "Home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("创建测试目录失败：%v", err)
	}
	got := runtimeHomeDirectory(root)
	if goruntime.GOOS == "darwin" {
		if got != home {
			t.Fatalf("运行时根 = %q，期望 %q", got, home)
		}
		return
	}
	if got != root {
		t.Fatalf("非 macOS 不应提升目录，实际 %q", got)
	}
}
