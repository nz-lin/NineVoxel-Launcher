// Package settings 提供 NineVoxel Launcher 的非秘密设置持久化能力。
//
// 职责边界：
//   - schema.go：设置 key 白名单、类型、默认值与取值校验（唯一的真相来源）；
//   - store_sqlite.go：基于 SQLite 的读写实现（复用 internal/database 的连接与迁移）；
//   - service.go：面向 Wails 的服务逻辑（快照/批量更新/变更事件）。
//
// 明确**不做**的事（AGENTS.md 5.7 与卡面禁止项）：
//   - 不保存任何 Token、Cookie、密码、账号凭证、邮箱或绝对路径清单；
//   - 不做网络请求、不检查更新、不读写游戏目录。
//
// 设计前提：设置是**非秘密的用户偏好**。因此读取失败时必须回退默认值并如实上报，
// 而不是让应用无法启动；写入失败必须整批回滚，不留下「改了一半」的状态。
package settings

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 设置项 key 常量：集中定义，禁止业务代码手写 key 字符串。
//
// 前六个 key 由接口冻结清单 §8.7 冻结；第七个 `download.mirrorEnabled`
// 由决策记录 D-57 追加（P3-15），用于真正启用下载镜像；第八、第九个
// `launch.javaPath` / `launch.maxMemoryMb` 由「只管理自己的运行时」与
// 「内存上限按物理内存动态计算」两项需求追加。
const (
	// KeyAppearanceTheme 为界面主题，取值 light / dark / system，由 P1-08 消费。
	KeyAppearanceTheme = "appearance.theme"

	// KeyAppearanceLanguage 为界面语言，取值 zh-Hans / zh-Hant / en，由 P1-07 消费。
	KeyAppearanceLanguage = "appearance.language"

	// KeyWindowWidth 为窗口宽度（像素），供 P1-01 后续实现窗口恢复时读取。
	KeyWindowWidth = "window.width"

	// KeyWindowHeight 为窗口高度（像素），同上。
	KeyWindowHeight = "window.height"

	// KeyWindowMaximized 为窗口是否最大化，同上。
	KeyWindowMaximized = "window.maximized"

	// KeyUpdateChannel 为更新渠道，P1 **只读占位**：不实现更新检查与下载。
	KeyUpdateChannel = "update.channel"

	// KeyDownloadMirrorEnabled 为下载镜像开关（D-57）。
	//
	// 布尔、默认 false、可写：默认关闭是安全默认，镜像只是加速手段，
	// 打开后仍受下载模块的 host 白名单约束。该值由 internal/download 的
	// provider 读取（装配层注入），设置存储本身不感知它的业务含义。
	KeyDownloadMirrorEnabled = "download.mirrorEnabled"

	// KeyLaunchJavaPath 为用户显式指定的 Java 可执行文件路径。
	//
	// 空串表示「未指定」：此时启动器只使用自己下载安装的运行时，**不探测本机
	// 任何 Java**（不读 JAVA_HOME、不扫 PATH、不遍历常见安装根）。非空时才把
	// 它作为唯一候选交给 internal/runtime 校验并执行 `java -version`。
	//
	// 设置存储不感知路径的业务含义与有效性，只负责原样持久化；
	// 路径校验、可执行性判断与版本解析全部由 internal/runtime 负责。
	KeyLaunchJavaPath = "launch.javaPath"

	// KeyLaunchMaxMemoryMb 为全局默认最大分配内存（MiB）。
	//
	// 这里只做存储与类型校验：2048 MiB 下限是「现代 Minecraft 能启动」的实际门槛，
	// 上限给到 1 TiB 只是防垃圾输入——**真实可用上限由界面按本机物理内存动态计算**，
	// 存储层不知道也不该知道运行它的是哪台机器。
	KeyLaunchMaxMemoryMb = "launch.maxMemoryMb"
)

// 各设置项的合法取值。
const (
	// ThemeLight 等为 `appearance.theme` 的合法取值。
	ThemeLight  = "light"
	ThemeDark   = "dark"
	ThemeSystem = "system"

	// LocaleZhHans 等为 `appearance.language` 的合法取值。
	LocaleZhHans = "zh-Hans"
	LocaleZhHant = "zh-Hant"
	LocaleEn     = "en"

	// ChannelStable 等为 `update.channel` 的合法取值。
	ChannelStable  = "stable"
	ChannelBeta    = "beta"
	ChannelNightly = "nightly"
)

// 数值型设置项的取值范围。
//
// 上下限来自卡面 A 节：窗口宽度 800–7680、高度 600–4320。
// 这里的常量同时供 Go 侧校验与前端提示使用（前端另有一份，二者需保持一致）。
const (
	// MinWindowWidth 与 MaxWindowWidth 为 window.width 的闭区间。
	MinWindowWidth = 800
	MaxWindowWidth = 7680

	// MinWindowHeight 与 MaxWindowHeight 为 window.height 的闭区间。
	MinWindowHeight = 600
	MaxWindowHeight = 4320
)

// 全局默认最大分配内存的存储层范围（MiB）。
//
// 下限 2048 是 Minecraft 能启动的实际门槛；上限 1 TiB 只用于挡住垃圾输入，
// 真实的可用上限由界面按物理内存计算——存储层不感知运行机器的容量。
const (
	// MinLaunchMemoryMb 与 MaxLaunchMemoryMb 为 launch.maxMemoryMb 的闭区间。
	MinLaunchMemoryMb = 2048
	MaxLaunchMemoryMb = 1024 * 1024
)

// ===== 设置项错误码（接口冻结清单 §8.3）=====

// ErrorCodeConfigInvalid 表示设置项不在白名单内或值类型/取值不符。
//
// 由接口冻结清单 §8.3 冻结为 `APP_CONFIG_002`；出现该错误时**整批不落盘**。
const ErrorCodeConfigInvalid = "APP_CONFIG_002"

// ErrorCodeDatabaseOpen 表示数据库打开失败（非迁移失败）。
//
// 由接口冻结清单 §8.3 冻结为 `DB_OPEN_001`。
const ErrorCodeDatabaseOpen = "DB_OPEN_001"

// ErrorCodePathInvalid 表示数据目录不可用。
//
// 由接口冻结清单 §8.3 冻结为 `PATH_INVALID_001`。
const ErrorCodePathInvalid = "PATH_INVALID_001"

// SettingKind 描述设置项的值类型。
//
// 类型用于 Go 边界二次校验：前端传入的 `map[string]any` 可能被伪造，
// 不能只依赖前端的类型约束。
type SettingKind int

const (
	// KindString 表示取值为受限字符串（必须命中 allowed 列表）。
	KindString SettingKind = iota

	// KindInt 表示取值为闭区间整数。
	KindInt

	// KindBool 表示取值为布尔值。
	KindBool

	// KindPath 表示取值为任意字符串（例如文件路径）。
	//
	// 与 KindString 的区别：不受 allowed 列表约束；空串是合法值，表示「未设置」。
	// 长度受 maxPathLen 约束，避免超长内容写进设置表。
	KindPath
)

// String 返回类型的中文说明，便于错误信息可读。
func (k SettingKind) String() string {
	switch k {
	case KindString:
		return "字符串"
	case KindInt:
		return "整数"
	case KindBool:
		return "布尔值"
	case KindPath:
		return "路径"
	default:
		return "未知类型"
	}
}

// Schema 描述单个设置项的完整契约。
//
// 一个 Schema 同时承载「默认值、类型、取值约束、说明」，避免同一信息散落在多处。
type Schema struct {
	// Key 为设置项标识，必须与常量一致。
	Key string `json:"key"`

	// Kind 为值类型。
	Kind SettingKind `json:"-"`

	// Default 为默认值。类型必须与 Kind 匹配（string / int / bool）。
	Default any `json:"default"`

	// AllowedStrings 为 KindString 时的合法取值列表。
	AllowedStrings []string `json:"allowedStrings,omitempty"`

	// MinInt 与 MaxInt 为 KindInt 时的闭区间（含端点）。
	MinInt int `json:"minInt,omitempty"`
	MaxInt int `json:"maxInt,omitempty"`

	// ReadOnly 表示该 key 可读但不可写（P1 的 update.channel 属于此类）。
	ReadOnly bool `json:"readOnly"`

	// 说明：为每个 key 写中文用途说明，供后续维护与前端提示复用。
	Description string `json:"description"`
}

// schemaDefinitions 是九个设置项的完整定义，顺序即展示顺序。
//
// 该列表是**唯一的真相来源**：白名单、默认值、取值校验全部从这里派生，
// 不允许在其他文件里再写一遍同样的约束。
var schemaDefinitions = []Schema{
	{
		Key:            KeyAppearanceTheme,
		Kind:           KindString,
		Default:        ThemeSystem,
		AllowedStrings: []string{ThemeLight, ThemeDark, ThemeSystem},
		Description:    "界面主题，由主题模块消费；system 表示跟随系统。",
	},
	{
		Key:            KeyAppearanceLanguage,
		Kind:           KindString,
		Default:        LocaleZhHans,
		AllowedStrings: []string{LocaleZhHans, LocaleZhHant, LocaleEn},
		Description:    "界面语言，由多语言模块消费。",
	},
	{
		Key:         KeyWindowWidth,
		Kind:        KindInt,
		Default:     1120,
		MinInt:      MinWindowWidth,
		MaxInt:      MaxWindowWidth,
		Description: "窗口宽度（像素）。P1 只存储，窗口恢复由 P1-01 后续实现读取。",
	},
	{
		Key:         KeyWindowHeight,
		Kind:        KindInt,
		Default:     760,
		MinInt:      MinWindowHeight,
		MaxInt:      MaxWindowHeight,
		Description: "窗口高度（像素）。P1 只存储，窗口恢复由 P1-01 后续实现读取。",
	},
	{
		Key:         KeyWindowMaximized,
		Kind:        KindBool,
		Default:     false,
		Description: "窗口是否最大化。P1 只存储，窗口恢复由 P1-01 后续实现读取。",
	},
	{
		Key:            KeyUpdateChannel,
		Kind:           KindString,
		Default:        ChannelStable,
		AllowedStrings: []string{ChannelStable, ChannelBeta, ChannelNightly},
		ReadOnly:       true,
		Description:    "更新渠道，P1 只读占位：更新检查与下载属 P8，当前不实现。",
	},
	{
		Key:         KeyDownloadMirrorEnabled,
		Kind:        KindBool,
		Default:     false,
		Description: "下载镜像开关（D-57）：默认关闭；打开后下载优先尝试镜像，失败回退官方源，校验不降级。",
	},
	{
		Key:         KeyLaunchJavaPath,
		Kind:        KindPath,
		Default:     "",
		Description: "用户显式指定的 Java 可执行文件路径；空串表示只用启动器自己安装的运行时。",
	},
	{
		Key:         KeyLaunchMaxMemoryMb,
		Kind:        KindInt,
		Default:     8192,
		MinInt:      MinLaunchMemoryMb,
		MaxInt:      MaxLaunchMemoryMb,
		Description: "全局默认最大分配内存（MiB）；真实上限由界面按物理内存动态计算。",
	},
}

// SchemaByKey 返回指定 key 的契约定义。
//
// 参数 key 为设置项标识；返回定义与是否存在标志。
// 第二个返回值用于白名单判定：false 即表示该 key 不在白名单内，必须拒绝。
func SchemaByKey(key string) (Schema, bool) {
	for _, item := range schemaDefinitions {
		if item.Key == key {
			return item, true
		}
	}
	return Schema{}, false
}

// AllSchemas 返回全部设置项的契约定义（副本，调用方修改不影响内部状态）。
//
// 前端设置页据此渲染九个分组并读取默认值，避免前端再维护一份默认值表。
func AllSchemas() []Schema {
	result := make([]Schema, len(schemaDefinitions))
	copy(result, schemaDefinitions)
	return result
}

// Defaults 返回全部设置项的默认值快照。
//
// 用于首次启动（数据库为空）以及读取失败时的安全回退。
func Defaults() map[string]any {
	result := make(map[string]any, len(schemaDefinitions))
	for _, item := range schemaDefinitions {
		result[item.Key] = item.Default
	}
	return result
}

// 设置项校验错误相关的文案常量。
const (
	// messageUnknownKey 用于白名单外 key 的错误说明。
	messageUnknownKey = "设置项不在白名单内"

	// messageReadOnly 用于只读设置项的错误说明。
	messageReadOnly = "该设置项为只读占位，P1 不允许写入"

	// messageTypeMismatch 用于类型不符的错误说明。
	messageTypeMismatch = "设置项的值类型不符，期望 %s，实际 %T"

	// messageValueNotAllowed 用于取值不在允许列表的错误说明。
	messageValueNotAllowed = "设置项的取值不在允许列表内：%v"

	// messageOutOfRange 用于整数越界的错误说明。
	messageOutOfRange = "设置项的取值超出范围 %d–%d，实际 %d"

	// maxPathLen 是路径型设置项的长度上限。
	//
	// 4096 字节远超任何真实可执行文件路径，只用于挡住明显的垃圾输入，
	// 不承担「路径是否有效」的判断——那是 internal/runtime 的职责。
	maxPathLen = 4096

	// messageTextTooLong 用于字符串超长的错误说明。
	messageTextTooLong = "设置项的内容过长，上限 %d 字节"
)

// ValidationError 表示一次设置项校验失败。
//
// 携带 key 与具体原因，便于前端按字段高亮（而不是只给一条全局错误）。
type ValidationError struct {
	// Key 为校验失败的设置项。
	Key string

	// Reason 为面向用户的中文原因（已脱敏，不含路径与凭证）。
	Reason string
}

// Error 实现 error 接口，输出稳定错误码 + key + 原因。
//
// 前缀固定为 ErrorCodeConfigInvalid，前端可直接按错误码分支处理。
func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s（%s）", ErrorCodeConfigInvalid, e.Reason, e.Key)
}

// ValidateChange 校验单个设置项的写入值。
//
// 校验顺序：白名单 → 只读 → 类型 → 取值/范围。任一失败返回 *ValidationError。
// 参数 key 为设置项标识；value 为待写入值（可能来自 JSON 反序列化的 any）。
func ValidateChange(key string, value any) error {
	definition, ok := SchemaByKey(key)
	if !ok {
		return ValidationError{Key: key, Reason: messageUnknownKey}
	}

	if definition.ReadOnly {
		return ValidationError{Key: key, Reason: messageReadOnly}
	}

	switch definition.Kind {
	case KindString:
		text, ok := value.(string)
		if !ok {
			return ValidationError{
				Key:    key,
				Reason: fmt.Sprintf(messageTypeMismatch, definition.Kind.String(), value),
			}
		}
		for _, allowed := range definition.AllowedStrings {
			if text == allowed {
				return nil
			}
		}
		return ValidationError{
			Key:    key,
			Reason: fmt.Sprintf(messageValueNotAllowed, definition.AllowedStrings),
		}

	case KindInt:
		number, ok := toInt(value)
		if !ok {
			return ValidationError{
				Key:    key,
				Reason: fmt.Sprintf(messageTypeMismatch, definition.Kind.String(), value),
			}
		}
		if number < definition.MinInt || number > definition.MaxInt {
			return ValidationError{
				Key:    key,
				Reason: fmt.Sprintf(messageOutOfRange, definition.MinInt, definition.MaxInt, number),
			}
		}
		return nil

	case KindBool:
		if _, ok := value.(bool); !ok {
			return ValidationError{
				Key:    key,
				Reason: fmt.Sprintf(messageTypeMismatch, definition.Kind.String(), value),
			}
		}
		return nil

	case KindPath:
		text, ok := value.(string)
		if !ok {
			return ValidationError{
				Key:    key,
				Reason: fmt.Sprintf(messageTypeMismatch, definition.Kind.String(), value),
			}
		}
		// 空串表示「未指定」，是合法值：此时启动器只用自己安装的运行时。
		if len(text) > maxPathLen {
			return ValidationError{
				Key:    key,
				Reason: fmt.Sprintf(messageTextTooLong, maxPathLen),
			}
		}
		return nil

	default:
		return ValidationError{Key: key, Reason: "设置项类型定义无效"}
	}
}

// toInt 把多种数值形态统一转换为 int。
//
// JSON 反序列化到 `any` 时数字默认是 float64；Wails 绑定也可能传入
// int / int64 / json.Number。这里统一收口，避免每种来源各写一份转换。
// 非整数值（例如 1120.5）一律视为类型不符，不做静默取整。
func toInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		// 只接受整数值的浮点：1.0 允许，1.5 拒绝。
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

// NormalizeSnapshot 把任意来源的设置值收敛为完整且类型正确的快照。
//
// 规则：逐 key 取白名单定义，缺失或类型/取值不合法的项一律回退默认值。
// 这样即使数据库里存在历史脏数据（旧版本写入、手工改动），
// 前端拿到的快照也永远是九个合法值，不需要在界面里再写一套兜底。
//
// 参数 stored 为从存储读取到的原始键值对。
func NormalizeSnapshot(stored map[string]any) map[string]any {
	result := Defaults()

	for key, value := range stored {
		definition, ok := SchemaByKey(key)
		if !ok {
			// 白名单外的历史数据直接忽略：不回写、不报错、不展示。
			continue
		}

		// 只读项不接受存储值覆盖：始终展示默认占位值。
		if definition.ReadOnly {
			continue
		}

		if err := ValidateChange(key, value); err != nil {
			// 脏数据回退默认值；不向上抛错，避免一条坏记录让整页打不开。
			continue
		}

		// 整数统一为 int，避免 float64(1120) 序列化成 1120.0 传给前端。
		if definition.Kind == KindInt {
			if number, ok := toInt(value); ok {
				result[key] = number
				continue
			}
		}
		result[key] = value
	}

	return result
}

// IsKnownKey 判断 key 是否在白名单内。
func IsKnownKey(key string) bool {
	_, ok := SchemaByKey(strings.TrimSpace(key))
	return ok
}
