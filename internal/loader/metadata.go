package loader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
)

const (
	metadataTTL            = 6 * time.Hour
	metadataMaxListBytes   = 4 << 20
	metadataMaxSmallBytes  = 512 << 10
	metadataRequestTimeout = 20 * time.Second
	metadataMaxRedirects   = 3
)

var (
	metadataHTTPClientFactory = newMetadataHTTPClient
	metadataNow               = time.Now
)

var metadataAllowedHosts = map[string]bool{
	"meta.fabricmc.net":        true,
	"meta.quiltmc.org":         true,
	"maven.fabricmc.net":       true,
	"maven.quiltmc.org":        true,
	"files.minecraftforge.net": true,
	"maven.minecraftforge.net": true,
	"maven.neoforged.net":      true,
}

func newMetadataHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConnsPerHost:   2,
	}
	return &http.Client{
		Timeout:       metadataRequestTimeout,
		Transport:     transport,
		CheckRedirect: checkMetadataRedirect,
	}
}

func checkMetadataRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > metadataMaxRedirects {
		return errors.New("too many redirects")
	}
	if err := validateMetadataURL(req.URL.String()); err != nil {
		return err
	}
	return nil
}

func validateMetadataURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" {
		return errors.New("invalid metadata URL")
	}
	if !metadataAllowedHosts[strings.ToLower(parsed.Hostname())] {
		return errors.New("metadata host is not allowed")
	}
	return nil
}

// ListVersions queries the official loader catalog and falls back to a cached
// response when the network is unavailable.
func ListVersions(ctx context.Context, req VersionListRequest) (VersionList, *Error) {
	kind, err := ParseKind(req.Loader)
	if err != nil {
		return VersionList{}, inputError(err.Error())
	}
	minecraftVersion, err := validateMinecraftVersion(req.MinecraftVersion)
	if err != nil {
		return VersionList{}, inputError(err.Error())
	}
	result := VersionList{Loader: kind, MinecraftVersion: minecraftVersion, Versions: []Version{}}
	if kind == KindVanilla {
		return result, nil
	}
	root, rootErr := artifact.DataRoot()
	if rootErr != nil {
		// The cache is optional for catalog reads. Keep the official request
		// usable even when the launcher cannot resolve a writable data root.
		root = ""
	}

	var versions []Version
	var stale bool
	switch kind {
	case KindFabric, KindQuilt:
		versions, stale, err = listProfileLoaderVersions(ctx, root, kind, minecraftVersion)
	case KindForge:
		versions, stale, err = listForgeVersions(ctx, root, minecraftVersion)
	case KindNeoForge:
		versions, stale, err = listNeoForgeVersions(ctx, root, minecraftVersion)
	default:
		err = fmt.Errorf("unsupported loader")
	}
	if err != nil {
		return VersionList{}, metadataError(err.Error(), true)
	}
	result.Versions = versions
	result.Stale = stale
	for _, version := range versions {
		if version.Recommended {
			result.Recommended = version.Version
			break
		}
	}
	return result, nil
}

type profileLoaderEntry struct {
	Version string `json:"version"`
	Stable  *bool  `json:"stable"`
	Loader  *struct {
		Version string `json:"version"`
		Stable  *bool  `json:"stable"`
	} `json:"loader"`
}

func listProfileLoaderVersions(ctx context.Context, root string, kind Kind, minecraftVersion string) ([]Version, bool, error) {
	base := "https://meta.fabricmc.net/v2/versions/loader/"
	if kind == KindQuilt {
		base = "https://meta.quiltmc.org/v3/versions/loader/"
	}
	endpoint := base + url.PathEscape(minecraftVersion)
	body, stale, err := fetchMetadataDocument(ctx, root, kind, minecraftVersion, "catalog", endpoint, metadataMaxListBytes)
	if err != nil {
		return nil, false, err
	}
	var entries []profileLoaderEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, false, errors.New("加载器版本列表格式不正确。")
	}
	return parseProfileLoaderVersions(entries), stale, nil
}

func parseProfileLoaderVersions(entries []profileLoaderEntry) []Version {
	versions := make([]Version, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		rawVersion := entry.Version
		stableFlag := entry.Stable
		if entry.Loader != nil && strings.TrimSpace(entry.Loader.Version) != "" {
			rawVersion = entry.Loader.Version
			stableFlag = entry.Loader.Stable
		}
		version, err := validateLoaderVersion(rawVersion)
		if err != nil || seen[version] {
			continue
		}
		seen[version] = true
		stable := stableLoaderVersion(version)
		// Fabric Meta currently marks only the newest release as stable and
		// reports older non-prerelease releases as false. A version without a
		// prerelease suffix is still a stable release, so never demote it.
		if stableFlag != nil && *stableFlag {
			stable = true
		}
		versions = append(versions, Version{Version: version, Stable: stable})
	}
	sortVersions(versions)
	markRecommended(versions)
	return versions
}

type forgePromotions map[string]string

func parseForgePromotions(body []byte) (forgePromotions, error) {
	var document struct {
		Promos json.RawMessage `json:"promos"`
	}
	if err := json.Unmarshal(body, &document); err == nil && document.Promos != nil {
		var promotions map[string]string
		if err := json.Unmarshal(document.Promos, &promotions); err != nil || promotions == nil {
			return nil, errors.New("Forge 推荐版本信息格式不正确。")
		}
		return forgePromotions(promotions), nil
	}
	var legacy forgePromotions
	if err := json.Unmarshal(body, &legacy); err == nil {
		return legacy, nil
	}
	return nil, errors.New("Forge 推荐版本信息格式不正确。")
}

type mavenMetadata struct {
	Versions []string `xml:"versioning>versions>version"`
}

func listForgeVersions(ctx context.Context, root string, minecraftVersion string) ([]Version, bool, error) {
	promotionsBody, promotionsStale, promotionsErr := fetchMetadataDocument(
		ctx, root, KindForge, minecraftVersion, "promotions",
		"https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json",
		metadataMaxSmallBytes,
	)
	var promotions forgePromotions
	if promotionsErr == nil {
		promotions, promotionsErr = parseForgePromotions(promotionsBody)
	}
	metadataBody, metadataStale, metadataErr := fetchMetadataDocument(
		ctx, root, KindForge, minecraftVersion, "maven-metadata",
		"https://maven.minecraftforge.net/net/minecraftforge/forge/maven-metadata.xml",
		metadataMaxListBytes,
	)
	if metadataErr != nil && promotionsErr != nil {
		return nil, false, errors.New("无法获取 Forge 官方版本列表，请检查网络后重试。")
	}
	versionsByValue := make(map[string]Version)
	if metadataErr == nil {
		var metadata mavenMetadata
		if err := xml.Unmarshal(metadataBody, &metadata); err != nil {
			return nil, false, errors.New("Forge 官方版本列表格式不正确。")
		}
		prefix := minecraftVersion + "-"
		for _, coordinate := range metadata.Versions {
			if !strings.HasPrefix(coordinate, prefix) {
				continue
			}
			version, err := validateLoaderVersion(strings.TrimPrefix(coordinate, prefix))
			if err != nil {
				continue
			}
			versionsByValue[version] = Version{Version: version, Stable: stableLoaderVersion(version)}
		}
	}
	recommended := promotions[minecraftVersion+"-recommended"]
	latest := promotions[minecraftVersion+"-latest"]
	for _, version := range []string{recommended, latest} {
		if version == "" {
			continue
		}
		if _, exists := versionsByValue[version]; !exists && stableLoaderVersion(version) {
			versionsByValue[version] = Version{Version: version, Stable: true}
		}
	}
	versions := make([]Version, 0, len(versionsByValue))
	for _, version := range versionsByValue {
		if version.Version == recommended {
			version.Recommended = true
		}
		versions = append(versions, version)
	}
	sortVersions(versions)
	if len(versions) > 0 && recommended == "" {
		markRecommended(versions)
	}
	return versions, promotionsStale || metadataStale, nil
}

func listNeoForgeVersions(ctx context.Context, root string, minecraftVersion string) ([]Version, bool, error) {
	groupPath, prefix, displayPrefix, supported := neoForgeCatalog(minecraftVersion)
	if !supported {
		return []Version{}, false, nil
	}
	endpoint := "https://maven.neoforged.net/releases/net/neoforged/" + groupPath + "/maven-metadata.xml"
	body, stale, err := fetchMetadataDocument(ctx, root, KindNeoForge, minecraftVersion, "maven-metadata", endpoint, metadataMaxListBytes)
	if err != nil {
		return nil, false, errors.New("无法获取 NeoForge 官方版本列表，请检查网络后重试。")
	}
	var metadata mavenMetadata
	if err := xml.Unmarshal(body, &metadata); err != nil {
		return nil, false, errors.New("NeoForge 官方版本列表格式不正确。")
	}
	versionsByValue := make(map[string]Version)
	for _, coordinate := range metadata.Versions {
		if !strings.HasPrefix(coordinate, prefix) {
			continue
		}
		version, err := validateLoaderVersion(strings.TrimPrefix(coordinate, displayPrefix))
		if err != nil {
			continue
		}
		versionsByValue[version] = Version{Version: version, Stable: stableLoaderVersion(version)}
	}
	versions := make([]Version, 0, len(versionsByValue))
	for _, version := range versionsByValue {
		versions = append(versions, version)
	}
	sortVersions(versions)
	markRecommended(versions)
	return versions, stale, nil
}

// neoForgeCatalog maps a Minecraft version to the official Maven coordinate
// space. NeoForge's first release line (1.20.1) used net.neoforged:forge;
// subsequent releases use the independent YY.PATCH versioning scheme.
func neoForgeCatalog(minecraftVersion string) (groupPath, prefix, displayPrefix string, supported bool) {
	if minecraftVersion == "1.20.1" {
		return "forge", "1.20.1-47.1.", "1.20.1-", true
	}
	parts := strings.Split(minecraftVersion, ".")
	if len(parts) < 2 {
		return "", "", "", false
	}
	numbers := make([]int, len(parts))
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return "", "", "", false
		}
		numbers[index] = number
	}
	if numbers[0] == 0 {
		return "", "", "", false
	}

	// Minecraft 1.x used NeoForge's minor.patch line (1.21.1 -> 21.1.x).
	// Newer year-based releases use the complete Minecraft version as the
	// NeoForge prefix (26.3 -> 26.3.x), while the loader version itself keeps
	// the full coordinate used by the installer artifact.
	if numbers[0] == 1 {
		minor := numbers[1]
		if minor < 20 {
			return "", "", "", false
		}
		patch := 0
		if len(numbers) > 2 {
			patch = numbers[2]
		}
		if minor == 20 && patch < 2 {
			return "", "", "", false
		}
		if len(parts) == 2 {
			prefix = fmt.Sprintf("%d.0.", minor)
		} else {
			prefix = strings.Join(parts[1:], ".") + "."
		}
	} else {
		prefix = minecraftVersion + "."
	}
	return "neoforge", prefix, "", true
}

func sortVersions(versions []Version) {
	sort.SliceStable(versions, func(left, right int) bool {
		if versions[left].Stable != versions[right].Stable {
			return versions[left].Stable
		}
		comparison := compareVersionsDescending(versions[left].Version, versions[right].Version)
		if comparison != 0 {
			return comparison < 0
		}
		return versions[left].Version < versions[right].Version
	})
}

func markRecommended(versions []Version) {
	for index := range versions {
		if versions[index].Stable {
			versions[index].Recommended = true
			return
		}
	}
	if len(versions) > 0 {
		versions[0].Recommended = true
	}
}

func fetchMetadataDocument(ctx context.Context, root string, kind Kind, minecraftVersion, name, endpoint string, maxBytes int64) ([]byte, bool, error) {
	if err := validateMetadataURL(endpoint); err != nil {
		return nil, false, errors.New("加载器元数据地址不受信任。")
	}
	cacheAbs := ""
	if strings.TrimSpace(root) != "" {
		if cacheRel, cacheRelErr := metadataCacheRel(kind, minecraftVersion, endpoint); cacheRelErr == nil {
			if resolved, resolveErr := artifact.ResolveInsideRoot(root, cacheRel); resolveErr == nil {
				cacheAbs = resolved
			}
		}
	}
	if cacheAbs != "" {
		if info, statErr := os.Stat(cacheAbs); statErr == nil && info.Size() > 0 && metadataNow().Sub(info.ModTime()) <= metadataTTL {
			body, readErr := os.ReadFile(cacheAbs)
			if readErr == nil && int64(len(body)) <= maxBytes {
				return body, false, nil
			}
		}
	}

	body, fetchErr := fetchMetadataBytes(ctx, endpoint, maxBytes)
	if fetchErr != nil {
		if cacheAbs != "" {
			if cached, readErr := os.ReadFile(cacheAbs); readErr == nil && len(cached) > 0 && int64(len(cached)) <= maxBytes {
				return cached, true, nil
			}
		}
		return nil, false, fetchErr
	}
	if cacheAbs != "" {
		// Cache failures must not make a successful official response fail.
		_ = writeMetadataCache(cacheAbs, body)
	}
	return body, false, nil
}

func fetchMetadataBytes(ctx context.Context, endpoint string, maxBytes int64) ([]byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, metadataRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("加载器元数据请求无法创建。")
	}
	request.Header.Set("Accept", "application/json, application/xml, text/plain")
	request.Header.Set("User-Agent", "NineVoxel-Launcher")
	response, err := metadataHTTPClientFactory().Do(request)
	if err != nil {
		return nil, errors.New("无法连接加载器官方服务，请检查网络后重试。")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("加载器官方服务返回异常状态 %d。", response.StatusCode)
	}
	if response.ContentLength > maxBytes {
		return nil, errors.New("加载器官方响应超出允许大小。")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, errors.New("读取加载器官方响应失败。")
	}
	if int64(len(body)) > maxBytes {
		return nil, errors.New("加载器官方响应超出允许大小。")
	}
	if len(body) == 0 {
		return nil, errors.New("加载器官方响应为空。")
	}
	return body, nil
}

const metadataMaxArtifactSize = int64(1 << 30)

// probeMetadataFileSize resolves the size of an official artifact without
// downloading its body. A HEAD request is preferred; servers that reject HEAD
// fall back to a one-byte range request and Content-Range.
func probeMetadataFileSize(ctx context.Context, endpoint string) (int64, error) {
	if err := validateMetadataURL(endpoint); err != nil {
		return 0, errors.New("加载器文件地址不受信任。")
	}
	requestCtx, cancel := context.WithTimeout(ctx, metadataRequestTimeout)
	defer cancel()

	head, err := http.NewRequestWithContext(requestCtx, http.MethodHead, endpoint, nil)
	if err != nil {
		return 0, errors.New("无法创建加载器文件大小探测请求。")
	}
	head.Header.Set("User-Agent", "NineVoxel-Launcher")
	response, err := metadataHTTPClientFactory().Do(head)
	if err != nil {
		return 0, errors.New("无法连接加载器官方文件服务。")
	}
	size := response.ContentLength
	status := response.StatusCode
	response.Body.Close()
	if status == http.StatusOK && size > 0 {
		return validateProbedSize(size)
	}
	if status != http.StatusOK && status != http.StatusForbidden &&
		status != http.StatusMethodNotAllowed && status != http.StatusNotImplemented {
		return 0, fmt.Errorf("加载器文件服务返回异常状态 %d。", status)
	}

	get, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, errors.New("无法创建加载器文件大小探测请求。")
	}
	get.Header.Set("Range", "bytes=0-0")
	get.Header.Set("User-Agent", "NineVoxel-Launcher")
	response, err = metadataHTTPClientFactory().Do(get)
	if err != nil {
		return 0, errors.New("无法连接加载器官方文件服务。")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusPartialContent {
		if total := parseContentRangeTotal(response.Header.Get("Content-Range")); total > 0 {
			return validateProbedSize(total)
		}
		return 0, errors.New("加载器文件服务未返回文件大小。")
	}
	if response.StatusCode == http.StatusOK && response.ContentLength > 0 {
		return validateProbedSize(response.ContentLength)
	}
	return 0, fmt.Errorf("加载器文件服务返回异常状态 %d。", response.StatusCode)
}

func validateProbedSize(size int64) (int64, error) {
	if size <= 0 || size > metadataMaxArtifactSize {
		return 0, errors.New("加载器文件大小超出允许范围。")
	}
	return size, nil
}

func parseContentRangeTotal(raw string) int64 {
	_, total, found := strings.Cut(strings.TrimSpace(raw), "/")
	if !found {
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(total), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func metadataCacheRel(kind Kind, minecraftVersion, endpoint string) (string, error) {
	sum := sha256.Sum256([]byte(string(kind) + "\x00" + minecraftVersion + "\x00" + endpoint))
	name := hex.EncodeToString(sum[:]) + ".data"
	relative := filepath.ToSlash(filepath.Join("artifacts", "metadata", "loaders", string(kind), name))
	normalized, err := artifact.ValidateRelativePath(relative)
	if err != nil {
		return "", errors.New("加载器元数据缓存路径无效。")
	}
	return normalized, nil
}

func writeMetadataCache(absolute string, body []byte) error {
	directory := filepath.Dir(absolute)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return errors.New("创建加载器元数据缓存目录失败。")
	}
	temp, err := os.CreateTemp(directory, ".loader-metadata-*")
	if err != nil {
		return errors.New("创建加载器元数据缓存失败。")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(body); err != nil {
		temp.Close()
		return errors.New("写入加载器元数据缓存失败。")
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return errors.New("同步加载器元数据缓存失败。")
	}
	if err := temp.Close(); err != nil {
		return errors.New("关闭加载器元数据缓存失败。")
	}
	if err := os.Chmod(tempPath, 0644); err != nil {
		return errors.New("设置加载器元数据缓存权限失败。")
	}
	if err := os.Rename(tempPath, absolute); err != nil {
		return errors.New("提交加载器元数据缓存失败。")
	}
	return nil
}
