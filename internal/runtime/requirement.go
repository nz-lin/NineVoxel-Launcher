// 本文件实现「按版本元数据解析 Java 需求」。
//
// 唯一输入是版本 JSON 的 `javaVersion` 段（`majorVersion` / `component`），
// 唯一输出是 `RuntimeRequirement`。规则：
//
//   - 元数据明确声明时 `source=metadata`；
//   - 元数据缺失或 majorVersion ≤ 0 时 `source=unknown`、`major=0`，
//     并附带 `JAVA_VERSION_UNKNOWN` 提示，**绝不**按 Minecraft 版本号猜 Java 版本
//     （D-1 / D-39，见 doc.go 的说明）；
//   - `platform` / `arch` / `bitness` 只描述当前进程环境，不参与「版本 → Java」推断。
//
// 本文件读取 `<DataRoot>/versions/<id>/<id>.json`。本地不存在时通过
// `internal/version` 的 EnsureVersionMetadata 按需获取一次（与版本详情页同一套
// 下载与 sha1 校验规则），再重试读取；本文件自身不写盘、不直接发起网络请求。
package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/version"
)

// JavaVersionMetadata 是版本元数据里与 Java 相关的部分。
//
// 字段为 0 / 空串表示元数据没有声明；调用方必须把这种状态转成
// `source=unknown`，而不是猜一个默认值。
type JavaVersionMetadata struct {
	// MajorVersion 为元数据声明的主版本号；未声明为 0。
	MajorVersion int
	// Component 为官方组件名（例如 `java-runtime-gamma`）；未声明为空串。
	Component string
}

// ParseJavaVersionMetadata 从版本 JSON 文本中解析 `javaVersion` 段。
//
// 参数 raw 为 `<id>.json` 的完整内容。
// 结构非法时返回 `VERSION_METADATA_001`；`javaVersion` 缺失不算错误——
// 它表示「元数据没有声明」，由 `BuildRuntimeRequirement` 转成 unknown。
func ParseJavaVersionMetadata(raw []byte) (JavaVersionMetadata, *AppError) {
	var document struct {
		JavaVersion *struct {
			MajorVersion int    `json:"majorVersion"`
			Component    string `json:"component"`
		} `json:"javaVersion"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return JavaVersionMetadata{}, newInputError(ErrorCodeVersionMetadata, messageMetadataMalformed)
	}
	if document.JavaVersion == nil {
		return JavaVersionMetadata{}, nil
	}
	return JavaVersionMetadata{
		MajorVersion: document.JavaVersion.MajorVersion,
		Component:    strings.TrimSpace(document.JavaVersion.Component),
	}, nil
}

// BuildRuntimeRequirement 把元数据映射为运行时需求（纯函数）。
//
// 公开导出是为了让测试直接验证映射规则，不依赖真实版本目录；
// 生产路径由 `ResolveRuntimeRequirement` 调用。
func BuildRuntimeRequirement(meta JavaVersionMetadata) RuntimeRequirement {
	requirement := RuntimeRequirement{
		Major:     meta.MajorVersion,
		Component: meta.Component,
		Platform:  currentPlatformName(),
		Arch:      currentArch(),
		Bitness:   currentBitness(),
	}
	if meta.MajorVersion <= 0 {
		// 元数据没有声明：明确告诉界面「未知」，不做任何猜测。
		requirement.Source = RequirementSourceUnknown
		requirement.Warnings = []RuntimeProbeWarning{{
			Code:    WarningJavaVersionUnknown,
			Message: messageRequirementUnknown,
		}}
		return requirement
	}
	requirement.Source = RequirementSourceMetadata
	return requirement
}

// ResolveRuntimeRequirement 读取某个版本的元数据并解析 Java 需求。
//
// 参数 req.VersionID 为 Minecraft 版本 id（例如 `1.21.4`）。
// 返回 `source=unknown` 时不算错误：需求未知是正常状态，界面据此提示手动选择。
// 只有「版本 id 为空」「元数据不可读」「元数据非法」「数据根不可用」才返回错误。
func ResolveRuntimeRequirement(req RuntimeRequirementRequest) (RuntimeRequirement, *AppError) {
	versionID := strings.TrimSpace(req.VersionID)
	if versionID == "" {
		return RuntimeRequirement{}, newInputError(ErrorCodeVersionMetadata, messageVersionIDRequired)
	}

	root, appErr := runtimeDataRoot()
	if appErr != nil {
		return RuntimeRequirement{}, appErr
	}

	relativePath, relErr := artifact.VersionRelPath(versionID, versionID+".json")
	if relErr != nil {
		// 版本 id 不能安全地拼进路径（含分隔符、`..` 等）：按元数据不可读处理。
		return RuntimeRequirement{}, newInputError(ErrorCodeVersionMetadata, messageMetadataUnreadable)
	}

	raw, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(relativePath)))
	if readErr != nil {
		// 本地没有版本 JSON：先按「版本详情」同一套规则按需获取一次再重试。
		// 不这样做的话，用户从未打开过该版本详情时，解析 Java 需求必然失败
		// （历史上就是 VERSION_METADATA_001）。
		if ensureErr := version.EnsureVersionMetadata(versionID); ensureErr != nil {
			return RuntimeRequirement{}, newInputError(ErrorCodeVersionMetadata, messageMetadataUnreadable)
		}
		raw, readErr = os.ReadFile(filepath.Join(root, filepath.FromSlash(relativePath)))
		if readErr != nil {
			return RuntimeRequirement{}, newInputError(ErrorCodeVersionMetadata, messageMetadataUnreadable)
		}
	}

	meta, parseErr := ParseJavaVersionMetadata(raw)
	if parseErr != nil {
		return RuntimeRequirement{}, parseErr
	}

	return BuildRuntimeRequirement(meta), nil
}

// runtimeDataRoot 解析数据根；测试可替换为临时目录。
//
// 生产实现直接复用 `internal/artifact` 的权威解析（便携版 data 优先、
// 回退用户数据目录），避免在 runtime 包里复制一份目录优先级规则。
var runtimeDataRoot = func() (string, *AppError) {
	root, err := artifact.DataRoot()
	if err != nil {
		// 显式判空后返回：artifact.DataRoot 返回 *artifact.Error，
		// 直接上抛会把 typed nil 装进接口。
		return "", newSystemError(ErrorCodeVersionMetadata, messageDataRootUnavailable, true)
	}
	return root, nil
}
