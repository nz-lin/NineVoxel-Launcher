// 本文件实现设置服务。
//
// 职责：
//   - 启动时打开存储并执行迁移，失败时降级为内存存储（保证应用仍可启动）；
//   - 提供快照读取（前端初始化与「事件丢失后恢复」的权威数据源）；
//   - 提供批量更新：先逐 key 校验（白名单+类型+范围），全部通过才落盘，
//     落盘成功后再发 `settings.changed.v1`（先持久化再发事件）。
//
// 关键顺序约束（《事件与状态机规范》）：**先落盘，再发事件**。
// 反过来会让前端收到「已变更」但磁盘尚未写入，崩溃后状态与界面不一致。
package settings

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"example.com/ninevoxel/launcher/internal/events"
)

// Service 是设置服务本体。
//
// 与 Wails 服务包装（launcher/settingsservice.go）分离：本类型不依赖 Wails，
// 便于单元测试与未来复用；包装层只负责把方法暴露给前端绑定。
type Service struct {
	// store 为底层存储实现（SQLite 或内存降级）。
	store Store

	// emitter 为事件发射器，复用 internal/events（不得自建第二套）。
	emitter *events.Emitter

	// mu 保护并发读写：Wails 可能从多个 goroutine 调用服务方法。
	mu sync.RWMutex

	// snapshot 为内存缓存的最新快照，避免每次读都快查数据库。
	// 写入成功后同步更新，读取失败时保持上一次成功值或默认值。
	snapshot map[string]any

	// enabled 表示是否处于持久化模式（false 表示降级为仅本次会话生效）。
	enabled bool

	// pathStatus 记录数据库路径来源类别，供前端如实展示（不含绝对路径）。
	pathStatus string

	// initErr 记录初始化失败的稳定错误码，供前端展示原因。
	initErr string
}

// Snapshot 是返回给前端的设置快照。
//
// 字段使用 lowerCamelCase，与前端 `services/settings.ts` 的类型逐字段对应；
// 同时带上持久化状态，使界面能如实说明「重启后是否仍然有效」。
type SettingsSnapshot struct {
	// Theme 对应 `appearance.theme`。
	Theme string `json:"theme"`

	// Language 对应 `appearance.language`。
	Language string `json:"language"`

	// WindowWidth 对应 `window.width`。
	WindowWidth int `json:"windowWidth"`

	// WindowHeight 对应 `window.height`。
	WindowHeight int `json:"windowHeight"`

	// WindowMaximized 对应 `window.maximized`。
	WindowMaximized bool `json:"windowMaximized"`

	// UpdateChannel 对应 `update.channel`（只读占位）。
	UpdateChannel string `json:"updateChannel"`

	// DownloadMirrorEnabled 对应 `download.mirrorEnabled`（D-57 / D-54）。
	//
	// 该值决定游戏内容下载与第三方 Java 运行时（Adoptium）是否改走国内镜像；
	// Mojang 官方 Java 运行时组件不读它。界面需要用真实值回显开关，
	// 否则「保存后刷新回到关闭」会让用户误以为没生效，而下载其实已走镜像。
	DownloadMirrorEnabled bool `json:"downloadMirrorEnabled"`

	// LaunchJavaPath 对应 `launch.javaPath`：用户显式指定的 Java 可执行文件路径。
	//
	// 空串表示「未指定」——此时启动器只使用自己下载安装的运行时，不探测本机
	// 任何 Java。非空时是 internal/runtime 探测的唯一候选来源。
	LaunchJavaPath string `json:"launchJavaPath"`

	// LaunchMaxMemoryMb 对应 `launch.maxMemoryMb`：全局默认最大分配内存（MiB）。
	//
	// 存储层只保证 2048 MiB .. 1 TiB 的闭区间；真实可用上限由界面按本机
	// 物理内存动态计算，因此该值可能大于当前机器的内存（换机器后回显旧值）。
	LaunchMaxMemoryMb int `json:"launchMaxMemoryMb"`

	// UpdatedAtMs 为最近一次写入时间（UTC Unix 毫秒）；从未写入时为 0。
	UpdatedAtMs int64 `json:"updatedAtMs"`

	// Enabled 表示当前是否处于持久化模式。
	// false 时界面必须提示「本次运行仅保存在内存」，不得显示「重启后仍然有效」。
	Enabled bool `json:"enabled"`

	// StorageKind 为存储来源类别：sqlite / memory。
	StorageKind string `json:"storageKind"`

	// PathKind 为数据库目录来源类别：userConfig / exeDir / unavailable。
	PathKind string `json:"pathKind"`

	// InitErrorCode 为初始化失败的稳定错误码；成功时为空。
	InitErrorCode string `json:"initErrorCode"`
}

// 存储来源类别常量。
const (
	// StorageKindSQLite 表示使用 SQLite 持久化。
	StorageKindSQLite = "sqlite"

	// StorageKindMemory 表示降级为内存存储（仅本次会话生效）。
	StorageKindMemory = "memory"
)

// UpdateRequest 是批量更新请求。
//
// 使用 `map[string]any` 而不是固定字段：白名单 key 会随阶段增加，
// 用映射可避免每加一个 key 就改一次 DTO；合法性由 ValidateChange 统一把关。
type UpdateRequest struct {
	// Changes 为 key → 目标值；未出现的 key 保持不变。
	Changes map[string]any `json:"changes"`
}

// NewService 创建设置服务。
//
// 参数 emitter 为事件发射器（通常由 package main 创建并注入）；
// 参数 ctx 用于初始化存储（打开数据库 + 迁移）。
//
// 初始化失败**不返回错误**：设置是辅助功能，数据库不可用时应用仍必须能启动，
// 因此降级为内存存储并把错误码记录到快照中，由界面如实提示用户。
func NewService(ctx context.Context, emitter *events.Emitter) *Service {
	service := &Service{
		emitter:  emitter,
		snapshot: Defaults(),
	}

	store, status, err := openStore(ctx)
	if err != nil {
		// 降级：保留内存存储，把稳定错误码写进快照。
		service.store = NewMemoryStore()
		service.enabled = false
		service.pathStatus = status.Kind
		service.initErr = classifyErrorCode(err)

		log.Printf("设置持久化不可用，已降级为仅本次会话生效：code=%s err=%v", service.initErr, err)
		return service
	}

	service.store = store
	service.enabled = true
	service.pathStatus = status.Kind

	// 首次加载：把数据库中的值收敛为合法快照；读取失败则保持默认值并记录错误。
	stored, loadErr := store.Load(ctx)
	if loadErr != nil {
		service.initErr = classifyErrorCode(loadErr)
		log.Printf("读取已保存设置失败，使用默认值：code=%s err=%v", service.initErr, loadErr)
		return service
	}

	service.snapshot = NormalizeSnapshot(stored)
	return service
}

// openStore 打开存储并返回路径状态。
//
// 拆成独立函数便于测试替换：测试可直接构造 Service 并注入 MemoryStore。
func openStore(ctx context.Context) (Store, DatabasePathStatus, error) {
	status := ResolveDatabasePath()
	if status.Path == "" {
		return nil, status, errors.New(ErrorCodePathInvalid + ": 数据目录不可用")
	}

	store, err := OpenSQLiteStore(ctx)
	if err != nil {
		return nil, status, err
	}
	return store, status, nil
}

// classifyErrorCode 从错误文本中提取稳定错误码。
//
// 错误码由各层在错误消息前缀中携带（例如 `DB_OPEN_001: ...`）；
// 这里按已知前缀匹配，匹配不到时回退 APP_UNKNOWN_001（冻结清单 §8.1）。
func classifyErrorCode(err error) string {
	if err == nil {
		return ""
	}

	known := []string{
		ErrorCodePathInvalid,
		ErrorCodeDatabaseOpen,
		ErrorCodeConfigInvalid,
		"DB_MIGRATION_001",
	}

	message := err.Error()
	for _, code := range known {
		if containsPrefix(message, code) {
			return code
		}
	}
	return "APP_UNKNOWN_001"
}

// containsPrefix 判断文本是否以指定错误码开头（允许前导空白）。
func containsPrefix(message string, code string) bool {
	if len(message) < len(code) {
		return false
	}
	return message[:len(code)] == code
}

// Snapshot 返回当前设置的完整快照。
//
// 该方法是前端恢复状态的**权威来源**：事件可能丢失或重复，
// 因此任何需要确定设置值的时候都应调用本方法而不是等事件。
// 返回值为内存缓存读取，不做磁盘 IO，可安全频繁调用。
func (s *Service) Snapshot() (SettingsSnapshot, error) {
	if s == nil {
		return SettingsSnapshot{}, errors.New("APP_UNKNOWN_001: 设置服务未初始化")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.buildSnapshotLocked(), nil
}

// buildSnapshotLocked 由内部快照映射组装对外结构。
//
// 调用前提：已持有 s.mu（读锁或写锁）。
// 该函数逐字段做类型断言并带回退值：即使内存映射被意外写脏，
// 对外结构也始终是类型正确的六个值。
func (s *Service) buildSnapshotLocked() SettingsSnapshot {
	result := SettingsSnapshot{
		Theme:                 stringValue(s.snapshot, KeyAppearanceTheme, ThemeSystem),
		Language:              stringValue(s.snapshot, KeyAppearanceLanguage, LocaleZhHans),
		WindowWidth:           intValue(s.snapshot, KeyWindowWidth, 1120),
		WindowHeight:          intValue(s.snapshot, KeyWindowHeight, 760),
		WindowMaximized:       boolValue(s.snapshot, KeyWindowMaximized, false),
		UpdateChannel:         stringValue(s.snapshot, KeyUpdateChannel, ChannelStable),
		DownloadMirrorEnabled: boolValue(s.snapshot, KeyDownloadMirrorEnabled, false),
		LaunchJavaPath:        stringValue(s.snapshot, KeyLaunchJavaPath, ""),
		LaunchMaxMemoryMb:     intValue(s.snapshot, KeyLaunchMaxMemoryMb, 8192),
		Enabled:               s.enabled,
		PathKind:              s.pathStatus,
		InitErrorCode:         s.initErr,
	}

	if s.enabled {
		result.StorageKind = StorageKindSQLite
	} else {
		result.StorageKind = StorageKindMemory
	}

	return result
}

// Update 按 key 批量更新设置项并返回更新后的快照。
//
// 校验流程（卡面 D 节要求 Go 边界二次校验）：
//  1. 逐 key 查白名单；未知 key 或只读 key 直接拒绝；
//  2. 逐 key 校验类型与取值/范围；
//  3. **全部通过后**才落盘——任何一个不合法都不写入，保证整批原子性；
//  4. 落盘成功后更新缓存并发射 `settings.changed.v1`。
//
// 失败时返回 *ValidationError（已带 APP_CONFIG_002），前端可直接按字段高亮。
func (s *Service) Update(req UpdateRequest) (SettingsSnapshot, error) {
	if s == nil {
		return SettingsSnapshot{}, errors.New("APP_UNKNOWN_001: 设置服务未初始化")
	}

	// 空请求是合法的空操作：直接返回当前快照，不写盘也不发事件。
	if len(req.Changes) == 0 {
		return s.Snapshot()
	}

	// 第一步：先整体校验，任一失败立即返回，不做任何写入。
	for key, value := range req.Changes {
		if err := ValidateChange(key, value); err != nil {
			return SettingsSnapshot{}, err
		}
	}

	s.mu.Lock()
	updatedAtMs := nowMillis()

	// 第二步：落盘。失败时不改内存缓存，保证「内存与磁盘一致」。
	if err := s.store.Save(context.Background(), req.Changes, updatedAtMs); err != nil {
		s.mu.Unlock()
		return SettingsSnapshot{}, err
	}

	// 第三步：更新内存缓存（只写已通过校验的 key）。
	for key, value := range req.Changes {
		if definition, ok := SchemaByKey(key); ok && definition.Kind == KindInt {
			if number, ok := toInt(value); ok {
				s.snapshot[key] = number
				continue
			}
		}
		s.snapshot[key] = value
	}

	snapshot := s.buildSnapshotLocked()
	keys := make([]string, 0, len(req.Changes))
	for key := range req.Changes {
		keys = append(keys, key)
	}
	s.mu.Unlock()

	// 第四步：落盘成功后才发事件。
	// 顺序不可颠倒：先发事件再落盘会让前端在崩溃后看到「已变更但实际没保存」。
	s.emitSettingsChanged(keys, updatedAtMs)

	return snapshot, nil
}

// emitSettingsChanged 发射 `settings.changed.v1`。
//
// 参数 keys 为本次被修改的 key 列表；updatedAtMs 为写入完成时间。
// 发射失败只记录日志：设置已成功落盘，事件丢失由前端查 Snapshot 兜底
// （这正是《事件与状态机规范》要求的恢复方式）。
func (s *Service) emitSettingsChanged(keys []string, updatedAtMs int64) {
	if s == nil || s.emitter == nil || len(keys) == 0 {
		return
	}

	envelope, err := events.Emit(s.emitter, events.EventSettingsChanged, events.SettingsChangedPayload{
		Keys:      keys,
		UpdatedAt: updatedAtMs,
	})
	if err != nil {
		// 事件是辅助通知：失败不影响已完成的持久化结果。
		log.Printf("设置变更事件发射失败：name=%q eventId=%q err=%v",
			events.EventSettingsChanged, envelope.EventID, err)
	}
}

// SnapshotOrDefault 返回一份全默认值的快照。
//
// 供包装层在服务未初始化或读取失败时兜底使用：设置页应始终能展示六个合法值，
// 而不是把「服务不可用」渲染成整页失败。
func SnapshotOrDefault() SettingsSnapshot {
	return SettingsSnapshot{
		Theme:             ThemeSystem,
		Language:          LocaleZhHans,
		WindowWidth:       1120,
		WindowHeight:      760,
		WindowMaximized:   false,
		UpdateChannel:     ChannelStable,
		LaunchJavaPath:    "",
		LaunchMaxMemoryMb: 8192,
		Enabled:           false,
		StorageKind:       StorageKindMemory,
		PathKind:          PathKindUnavailable,
	}
}

// BoolSetting 读取当前设置快照中的布尔项。
//
// 参数 key 为设置项标识，fallback 为缺失、未初始化或类型不符时的回退值。
// 用途：装配层把它包成 Provider 注入其它模块（例如下载镜像开关 D-57），
// 使那些模块无需 import settings、也无需自己开数据库读同一个 key。
// 返回值为内存缓存读取，不做磁盘 IO，可安全在每次下载前调用。
func (s *Service) BoolSetting(key string, fallback bool) bool {
	if s == nil {
		return fallback
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return boolValue(s.snapshot, key, fallback)
}

// StringSetting 读取当前设置快照中的字符串项。
//
// 参数 key 为设置项标识，fallback 为缺失、未初始化或类型不符时的回退值。
// 用途与 BoolSetting 相同：装配层把它包成 Provider 注入其它模块
// （例如用户显式指定的 Java 路径），使那些模块无需 import settings。
// 返回值为内存缓存读取，不做磁盘 IO。
func (s *Service) StringSetting(key, fallback string) string {
	if s == nil {
		return fallback
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return stringValue(s.snapshot, key, fallback)
}

// Enabled 返回当前是否处于持久化模式。
//
// 供装配层与前端提示使用：false 表示设置只在本次会话内有效。
func (s *Service) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// Close 释放存储资源。
//
// 供装配层在应用关闭时调用；失败不影响退出流程。
func (s *Service) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close()
}

// stringValue 从映射取字符串字段，缺失或类型不符时回退默认值。
func stringValue(source map[string]any, key string, fallback string) string {
	if value, ok := source[key].(string); ok && value != "" {
		return value
	}
	return fallback
}

// intValue 从映射取整数字段，缺失或类型不符时回退默认值。
func intValue(source map[string]any, key string, fallback int) int {
	if value, ok := source[key]; ok {
		if number, ok := toInt(value); ok {
			return number
		}
	}
	return fallback
}

// boolValue 从映射取布尔字段，缺失或类型不符时回退默认值。
func boolValue(source map[string]any, key string, fallback bool) bool {
	if value, ok := source[key].(bool); ok {
		return value
	}
	return fallback
}

// FormatValidationError 把校验错误转成「错误码 + 可读原因」的结构化形式。
//
// 供 Wails 包装层返回给前端；不是 *ValidationError 时按未知错误处理。
func FormatValidationError(err error) (code string, retryable bool, message string) {
	if err == nil {
		return "", false, ""
	}

	var validation ValidationError
	if errors.As(err, &validation) {
		// 校验失败属于用户输入问题：修正后可以重试，因此 retryable=true。
		return ErrorCodeConfigInvalid, true, fmt.Sprintf("%s：%s", validation.Reason, validation.Key)
	}

	return classifyErrorCode(err), true, err.Error()
}
