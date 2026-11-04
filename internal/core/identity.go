package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// Kind 是加载器类型。与 loader.Kind 保持一致，但本包不反向依赖 loader，
// 避免 core 与 loader 之间形成环。
type Kind string

const (
	// KindVanilla 表示原版。
	KindVanilla Kind = "vanilla"
	// KindForge 表示 Forge。
	KindForge Kind = "forge"
	// KindNeoForge 表示 NeoForge。
	KindNeoForge Kind = "neoforge"
	// KindFabric 表示 Fabric。
	KindFabric Kind = "fabric"
	// KindQuilt 表示 Quilt。
	KindQuilt Kind = "quilt"
)

// recipeVersion 是安装流程语义的版本号。
//
// 当安装步骤的含义发生变化（例如清单需要记录新的资源类别）而加载器产物本身
// 不变时，递增该值即可让既有产物被判定为「配方不同」，从而触发重新安装。
const recipeVersion = 1

// CorePlatform 描述产物所属的操作系统与架构。
//
// natives 与部分加载器产物和平台强相关，不能跨平台复用。
type CorePlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// CurrentPlatform 返回当前进程的平台标识。
//
// OS 取 Go 的 runtime.GOOS（windows / darwin / linux），
// Arch 归一化为 x64 / arm64 / 其它原值。
func CurrentPlatform() CorePlatform {
	return CorePlatform{OS: runtime.GOOS, Arch: normalizeArch(runtime.GOARCH)}
}

// normalizeArch 把 Go 的架构名归一化为产物命名常用的写法。
func normalizeArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	case "386":
		return "x86"
	default:
		return goarch
	}
}

// Identity 唯一标识「加载器 + Minecraft 版本 + 加载器版本」组合。
//
// 它是安装去重与并发锁的键。**不使用版本目录名做键**：Forge / NeoForge 的
// 官方安装器可能生成与推导值不同的目录名，用目录名做键会让同一组合拿到
// 不同的锁，进而并发写同一目录。
type Identity struct {
	LoaderKind       Kind
	MinecraftVersion string
	LoaderVersion    string
}

// NewIdentity 构造并校验一个安装身份。
//
// 三段都必须是不含路径分隔符的安全标识；Minecraft 版本另需通过
// artifact.ValidateRelativePath，避免 `../` 之类的值进入路径拼接。
func NewIdentity(kind Kind, minecraftVersion, loaderVersion string) (Identity, error) {
	kind = Kind(strings.TrimSpace(string(kind)))
	if !kind.valid() {
		return Identity{}, errors.New("加载器类型无效")
	}
	mc := strings.TrimSpace(minecraftVersion)
	if mc == "" || strings.ContainsAny(mc, `/\:`) {
		return Identity{}, errors.New("Minecraft 版本标识无效")
	}
	if normalized, appErr := artifact.ValidateRelativePath(mc); appErr != nil || normalized != mc {
		return Identity{}, errors.New("Minecraft 版本标识无效")
	}

	loader := strings.TrimSpace(loaderVersion)
	if kind == KindVanilla {
		// 原版没有加载器版本，强制归一化，避免同一版本出现两种身份。
		loader = ""
	}
	if loader != "" && strings.ContainsAny(loader, `/\:`) {
		return Identity{}, errors.New("加载器版本标识无效")
	}

	return Identity{LoaderKind: kind, MinecraftVersion: mc, LoaderVersion: loader}, nil
}

// valid 判断加载器类型是否为已知取值。
func (k Kind) valid() bool {
	switch k {
	case KindVanilla, KindForge, KindNeoForge, KindFabric, KindQuilt:
		return true
	default:
		return false
	}
}

// String 返回稳定键，形如 `forge:26.3:66.1.0` 或 `vanilla:26.3`。
//
// 由于各段都不允许包含冒号，该键可以安全地用冒号切回三段。
func (i Identity) String() string {
	if i.LoaderVersion == "" {
		return string(i.LoaderKind) + ":" + i.MinecraftVersion
	}
	return string(i.LoaderKind) + ":" + i.MinecraftVersion + ":" + i.LoaderVersion
}

// IsZero 判断身份是否为零值。
func (i Identity) IsZero() bool {
	return i.LoaderKind == "" && i.MinecraftVersion == "" && i.LoaderVersion == ""
}

// Recipe 描述「用什么配方安装」，用于判断既有产物能否复用。
//
// 同一加载器版本可能因安装器更新、profile 元数据变化或平台差异而产生不同
// 产物。仅比对逻辑身份会把旧产物误判为当前配方的结果，因此指纹参与判断。
type Recipe struct {
	Identity        Identity
	InstallerSHA256 string // Forge / NeoForge 安装器哈希；profile 类加载器为空
	ProfileSHA256   string // Fabric / Quilt profile JSON 哈希；安装器类为空
	Platform        CorePlatform
	Version         int
}

// Fingerprint 返回配方指纹。
//
// 使用规范化 JSON 而不是拼接裸字符串：后者在字段含分隔符时会产生歧义，
// 不同配方可能映射到同一指纹。
func (r Recipe) Fingerprint() string {
	payload := struct {
		Identity        string       `json:"identity"`
		InstallerSHA256 string       `json:"installerSha256"`
		ProfileSHA256   string       `json:"profileSha256"`
		Platform        CorePlatform `json:"platform"`
		Version         int          `json:"version"`
	}{
		Identity:        r.Identity.String(),
		InstallerSHA256: strings.ToLower(r.InstallerSHA256),
		ProfileSHA256:   strings.ToLower(r.ProfileSHA256),
		Platform:        r.Platform,
		Version:         r.Version,
	}
	// json.Marshal 对结构体字段顺序稳定，且这里全是字符串与整数，不会失败。
	encoded, err := json.Marshal(payload)
	if err != nil {
		// 兜底：理论上不可达，退化为直接哈希身份，避免返回空指纹被误判为相等。
		encoded = []byte(r.Identity.String())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
