package loader

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode"

	"example.com/ninevoxel/launcher/internal/artifact"
)

const profileLibrarySHA1MaxBytes = 4 << 10

type profileLibraryDocument struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	SHA1      string `json:"sha1"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Downloads *struct {
		Artifact *profileArtifactDocument `json:"artifact"`
	} `json:"downloads"`
}

type profileArtifactDocument struct {
	Path   string `json:"path"`
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	URL    string `json:"url"`
}

// normalizeProfileLibraries converts the top-level Maven fields used by
// Fabric and Quilt profiles into the standard downloads.artifact shape used by
// the installer. Other profile fields are preserved verbatim.
func normalizeProfileLibraries(ctx context.Context, root string, kind Kind, minecraftVersion string, body []byte) ([]byte, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, errors.New("加载器安装配置格式不正确。")
	}
	rawLibraries, ok := document["libraries"]
	if !ok {
		return nil, errors.New("加载器安装配置缺少依赖库列表。")
	}
	var libraries []json.RawMessage
	if err := json.Unmarshal(rawLibraries, &libraries); err != nil || len(libraries) == 0 {
		return nil, errors.New("加载器安装配置的依赖库列表无效。")
	}
	for index, raw := range libraries {
		normalized, err := normalizeProfileLibrary(ctx, root, kind, minecraftVersion, raw)
		if err != nil {
			var library profileLibraryDocument
			_ = json.Unmarshal(raw, &library)
			name := truncateProfileLibraryName(library.Name)
			if name == "" {
				return nil, err
			}
			return nil, fmt.Errorf("库「%s」%w", name, err)
		}
		libraries[index] = normalized
	}
	encodedLibraries, err := json.Marshal(libraries)
	if err != nil {
		return nil, errors.New("加载器依赖库信息无法保存。")
	}
	document["libraries"] = encodedLibraries
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, errors.New("加载器安装配置无法保存。")
	}
	return encoded, nil
}

func normalizeProfileLibrary(ctx context.Context, root string, kind Kind, minecraftVersion string, raw json.RawMessage) (json.RawMessage, error) {
	var library profileLibraryDocument
	if err := json.Unmarshal(raw, &library); err != nil {
		return nil, errors.New("的元数据格式不正确。")
	}
	if strings.TrimSpace(library.Name) == "" {
		return nil, errors.New("缺少 Maven 坐标。")
	}
	relativePath, err := mavenArtifactPath(library.Name)
	if err != nil {
		return nil, err
	}

	var current profileArtifactDocument
	if library.Downloads != nil && library.Downloads.Artifact != nil {
		current = *library.Downloads.Artifact
	}
	artifactURL := strings.TrimSpace(current.URL)
	if artifactURL == "" {
		artifactURL, err = resolveMavenArtifactURL(library.URL, relativePath)
		if err != nil {
			return nil, err
		}
	}
	if err := validateMetadataURL(artifactURL); err != nil {
		return nil, errors.New("的下载地址不在允许的来源列表中。")
	}

	sha1 := firstNonEmpty(current.SHA1, library.SHA1)
	if !validProfileSHA1(sha1) {
		sha1, err = fetchProfileLibrarySHA1(ctx, root, kind, minecraftVersion, artifactURL)
		if err != nil {
			return nil, err
		}
	}
	size := current.Size
	if size <= 0 {
		size = library.Size
	}
	if size <= 0 {
		size, err = probeMetadataFileSize(ctx, artifactURL)
		if err != nil {
			return nil, errors.New("无法获取文件大小，请检查网络后重试。")
		}
	}

	normalizedArtifact := profileArtifactDocument{
		Path:   relativePath,
		SHA1:   strings.ToLower(strings.TrimSpace(sha1)),
		SHA256: strings.ToLower(firstNonEmpty(current.SHA256, library.SHA256)),
		Size:   size,
		URL:    artifactURL,
	}
	encodedArtifact, err := json.Marshal(normalizedArtifact)
	if err != nil {
		return nil, errors.New("的下载信息无法保存。")
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, errors.New("的元数据格式不正确。")
	}
	downloads := make(map[string]json.RawMessage)
	if existing, ok := object["downloads"]; ok {
		_ = json.Unmarshal(existing, &downloads)
		if downloads == nil {
			downloads = make(map[string]json.RawMessage)
		}
	}
	downloads["artifact"] = encodedArtifact
	encodedDownloads, err := json.Marshal(downloads)
	if err != nil {
		return nil, errors.New("的下载信息无法保存。")
	}
	object["downloads"] = encodedDownloads
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, errors.New("的元数据无法保存。")
	}
	return encoded, nil
}

func profileLibrariesReady(libraries []json.RawMessage) bool {
	if len(libraries) == 0 {
		return false
	}
	for _, raw := range libraries {
		var library profileLibraryDocument
		if json.Unmarshal(raw, &library) != nil || library.Downloads == nil || library.Downloads.Artifact == nil {
			return false
		}
		entry := library.Downloads.Artifact
		if library.Name == "" || entry.Size <= 0 || !validProfileSHA1(entry.SHA1) || validateMetadataURL(entry.URL) != nil {
			return false
		}
		normalized, err := artifact.ValidateRelativePath(entry.Path)
		if err != nil || normalized != entry.Path {
			return false
		}
	}
	return true
}

func mavenArtifactPath(coordinate string) (string, error) {
	value := strings.TrimSpace(coordinate)
	extension := "jar"
	if separator := strings.LastIndex(value, "@"); separator > 0 {
		extension = value[separator+1:]
		value = value[:separator]
	}
	parts := strings.Split(value, ":")
	if len(parts) < 3 || len(parts) > 4 {
		return "", errors.New("的 Maven 坐标格式不正确。")
	}
	for _, part := range append(parts, extension) {
		if !validMavenPart(part) {
			return "", errors.New("的 Maven 坐标包含不支持的字符。")
		}
	}
	group, artifactID, version := parts[0], parts[1], parts[2]
	fileName := artifactID + "-" + version
	if len(parts) == 4 {
		fileName += "-" + parts[3]
	}
	fileName += "." + extension
	relative := path.Join(strings.ReplaceAll(group, ".", "/"), artifactID, version, fileName)
	normalized, err := artifact.ValidateRelativePath(relative)
	if err != nil || normalized != relative {
		return "", errors.New("的 Maven 坐标无法生成安全路径。")
	}
	return relative, nil
}

func validMavenPart(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || strings.ContainsRune("._+-", char) {
			continue
		}
		return false
	}
	return true
}

func resolveMavenArtifactURL(base, relative string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" {
		return "", errors.New("缺少可用的 Maven 下载地址。")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Maven 下载地址包含不支持的参数。")
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	resolved := parsed.ResolveReference(&url.URL{Path: relative})
	if err := validateMetadataURL(resolved.String()); err != nil {
		return "", errors.New("的下载地址不在允许的来源列表中。")
	}
	return resolved.String(), nil
}

func fetchProfileLibrarySHA1(ctx context.Context, root string, kind Kind, minecraftVersion, artifactURL string) (string, error) {
	body, _, err := fetchMetadataDocument(
		ctx, root, kind, minecraftVersion, "profile-library-sha1",
		artifactURL+".sha1", profileLibrarySHA1MaxBytes,
	)
	if err != nil {
		return "", errors.New("缺少 SHA-1 且无法从官方 Maven 获取。")
	}
	sha1, err := parseSHA1(body)
	if err != nil {
		return "", errors.New("的官方 SHA-1 格式不正确。")
	}
	return sha1, nil
}

func validProfileSHA1(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func truncateProfileLibraryName(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 160 {
		return value[:160] + "…"
	}
	return value
}
