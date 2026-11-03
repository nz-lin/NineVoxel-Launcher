package loader

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// Kind identifies a supported Minecraft installation core.
type Kind string

const (
	KindVanilla  Kind = "vanilla"
	KindFabric   Kind = "fabric"
	KindQuilt    Kind = "quilt"
	KindForge    Kind = "forge"
	KindNeoForge Kind = "neoforge"
)

// Error is the stable, frontend-safe error shape used by the loader service.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

const (
	ErrorCodeInput       = "LOADER_INPUT_001"
	ErrorCodeMetadata    = "LOADER_METADATA_001"
	ErrorCodeUnavailable = "LOADER_UNAVAILABLE_001"
	ErrorCodeProfile     = "LOADER_PROFILE_001"
	ErrorCodeInstaller   = "LOADER_INSTALLER_001"
	ErrorCodeCache       = "LOADER_CACHE_001"
)

func inputError(message string) *Error {
	return &Error{Code: ErrorCodeInput, Message: message}
}

func metadataError(message string, retryable bool) *Error {
	return &Error{Code: ErrorCodeMetadata, Message: message, Retryable: retryable}
}

func unavailableError(message string) *Error {
	return &Error{Code: ErrorCodeUnavailable, Message: message}
}

func profileError(message string, retryable bool) *Error {
	return &Error{Code: ErrorCodeProfile, Message: message, Retryable: retryable}
}

func installerError(message string, retryable bool) *Error {
	return &Error{Code: ErrorCodeInstaller, Message: message, Retryable: retryable}
}

func cacheError(message string) *Error {
	return &Error{Code: ErrorCodeCache, Message: message}
}

// Version is one selectable loader release.
type Version struct {
	Version     string `json:"version"`
	Stable      bool   `json:"stable"`
	Recommended bool   `json:"recommended"`
}

// VersionList is the frontend-safe result of a loader catalog query.
type VersionList struct {
	Loader           Kind      `json:"loader"`
	MinecraftVersion string    `json:"minecraftVersion"`
	Versions         []Version `json:"versions"`
	Recommended      string    `json:"recommended"`
	Stale            bool      `json:"stale"`
}

// VersionListRequest asks for loader releases compatible with one Minecraft version.
type VersionListRequest struct {
	Loader           string `json:"loader"`
	MinecraftVersion string `json:"minecraftVersion"`
}

// ParseKind normalizes and validates a loader identifier.
func ParseKind(raw string) (Kind, error) {
	switch Kind(strings.ToLower(strings.TrimSpace(raw))) {
	case KindVanilla:
		return KindVanilla, nil
	case KindFabric:
		return KindFabric, nil
	case KindQuilt:
		return KindQuilt, nil
	case KindForge:
		return KindForge, nil
	case KindNeoForge:
		return KindNeoForge, nil
	default:
		return "", inputError("请选择有效的游戏核心或模组加载器。")
	}
}

func validateMinecraftVersion(version string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > 128 {
		return "", inputError("请选择有效的 Minecraft 版本。")
	}
	normalized, pathErr := artifact.ValidateRelativePath(version)
	if pathErr != nil || normalized != version || strings.ContainsAny(version, `/\\:`) {
		return "", inputError("请选择有效的 Minecraft 版本。")
	}
	return version, nil
}

func validateLoaderVersion(version string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > 128 {
		return "", inputError("请选择有效的加载器版本。")
	}
	for _, char := range version {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || strings.ContainsRune("._+-", char) {
			continue
		}
		return "", inputError("加载器版本包含不支持的字符。")
	}
	return version, nil
}

// VersionID returns the deterministic version directory name for profile-based loaders.
func VersionID(kind Kind, minecraftVersion, loaderVersion string) (string, error) {
	mc, err := validateMinecraftVersion(minecraftVersion)
	if err != nil {
		return "", err
	}
	if kind == KindVanilla {
		return mc, nil
	}
	version, err := validateLoaderVersion(loaderVersion)
	if err != nil {
		return "", err
	}
	switch kind {
	case KindFabric:
		return "fabric-loader-" + version + "-" + mc, nil
	case KindQuilt:
		return "quilt-loader-" + version + "-" + mc, nil
	case KindForge:
		return mc + "-forge-" + version, nil
	case KindNeoForge:
		if mc == "1.20.1" {
			return mc + "-forge-" + version, nil
		}
		return "neoforge-" + version, nil
	default:
		return "", inputError("请选择有效的游戏核心或模组加载器。")
	}
}

func compareVersionsDescending(left, right string) int {
	leftParts := versionParts(left)
	rightParts := versionParts(right)
	limit := len(leftParts)
	if len(rightParts) < limit {
		limit = len(rightParts)
	}
	for index := 0; index < limit; index++ {
		leftPart, rightPart := leftParts[index], rightParts[index]
		leftNumber, leftNumeric := numericPart(leftPart)
		rightNumber, rightNumeric := numericPart(rightPart)
		if leftNumeric && rightNumeric {
			if leftNumber > rightNumber {
				return -1
			}
			if leftNumber < rightNumber {
				return 1
			}
			continue
		}
		if leftPart > rightPart {
			return -1
		}
		if leftPart < rightPart {
			return 1
		}
	}
	if len(leftParts) > len(rightParts) {
		return -1
	}
	if len(leftParts) < len(rightParts) {
		return 1
	}
	return 0
}

func versionParts(version string) []string {
	parts := make([]string, 0, 8)
	start := 0
	var previous rune
	for index, char := range version {
		if index == 0 {
			previous = char
			continue
		}
		if (unicode.IsDigit(char) && !unicode.IsDigit(previous)) ||
			(!unicode.IsDigit(char) && unicode.IsDigit(previous)) {
			parts = append(parts, version[start:index])
			start = index
		}
		previous = char
	}
	if start < len(version) {
		parts = append(parts, version[start:])
	}
	return parts
}

func numericPart(part string) (int64, bool) {
	if part == "" {
		return 0, false
	}
	for _, char := range part {
		if !unicode.IsDigit(char) {
			return 0, false
		}
	}
	number, err := strconv.ParseInt(part, 10, 64)
	if err != nil {
		return 0, false
	}
	return number, true
}

func stableLoaderVersion(version string) bool {
	lower := strings.ToLower(version)
	return !strings.Contains(lower, "beta") && !strings.Contains(lower, "alpha") &&
		!strings.Contains(lower, "snapshot") && !strings.Contains(lower, "pre")
}

func canonicalVersionKey(kind Kind, minecraftVersion, loaderVersion string) string {
	return fmt.Sprintf("%s|%s|%s", kind, minecraftVersion, loaderVersion)
}
