package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// 运行时租约：某个版本正在被游戏进程使用。
//
// 语义与「锁」不同：锁保护的是短时间的文件操作，租约保护的是整个游戏运行期。
// 租约未释放时不得删除或替换对应版本目录，也不得重建核心。

const (
	// leaseFileName 是租约表的文件名，与版本索引同目录。
	leaseFileName = "leases.json"
	// leaseSchemaVersion 是租约表结构版本。
	leaseSchemaVersion = 1
	// leaseOrphanGrace 是「无法确认进程状态」时保留租约的时间。
	//
	// 读取进程启动时间失败的平台或权限受限场景下，宁可晚一点回收，也不要
	// 误判为残留而删除正在使用的版本。
	leaseOrphanGrace = 10 * time.Minute
)

// LeaseRecord 是一条租约的持久化记录。
type LeaseRecord struct {
	// VersionID 是被占用的版本目录名。
	VersionID string `json:"versionId"`
	// PID 是游戏进程的操作系统进程号。
	//
	// **不能单独用它判断进程存活**：PID 会被操作系统复用。必须结合
	// ProcessStartMs 一起比对。
	PID uint32 `json:"pid"`
	// ProcessStartMs 是进程的启动时间（UTC 毫秒），用于识别 PID 复用。
	ProcessStartMs int64 `json:"processStartMs"`
	// AcquiredAtMs 是租约登记时间。
	AcquiredAtMs int64 `json:"acquiredAtMs"`
	// LaunchID 是本次启动的标识，仅用于日志关联。
	LaunchID string `json:"launchId,omitempty"`
}

// leaseTable 是租约表的持久化结构。
type leaseTable struct {
	SchemaVersion int           `json:"schemaVersion"`
	Leases        []LeaseRecord `json:"leases"`
}

// Lease 是已登记的运行时租约句柄。
//
// 调用方必须在游戏进程退出后调用 Release；重复调用是安全的。
type Lease interface {
	// VersionID 返回被占用的版本目录名。
	VersionID() string
	// Release 释放租约。可安全重复调用。
	Release()
}

// leaseHandle 是 Lease 的实现。
type leaseHandle struct {
	manager   *LeaseManager
	versionID string
	once      sync.Once
}

// VersionID 返回被占用的版本目录名。
func (h *leaseHandle) VersionID() string { return h.versionID }

// Release 释放租约。
func (h *leaseHandle) Release() {
	h.once.Do(func() { h.manager.release(h.versionID) })
}

// LeaseManager 管理运行时租约。
//
// 租约同时保存在内存与磁盘：内存用于快速判定，磁盘用于启动器崩溃后的恢复。
type LeaseManager struct {
	root string
	mu   sync.Mutex
	live map[string]LeaseRecord
}

// NewLeaseManager 创建租约管理器。
//
// 参数 root 为核心根目录。创建时不会清理既有租约——恢复由 Recover 显式完成，
// 因为判定残留需要读取系统进程信息，不应隐藏在构造过程中。
func NewLeaseManager(root string) *LeaseManager {
	return &LeaseManager{root: root, live: map[string]LeaseRecord{}}
}

// Acquire 登记一条运行时租约。
//
// 参数 versionID 为版本目录名；record 至少需带 PID 与进程启动时间。
//
// **必须在持有 Global 读锁期间调用**：这样重建取得 Global 写锁后，可以保证
// 不会再有新的租约登记，也能安全地检查现存租约。
func (m *LeaseManager) Acquire(versionID string, record LeaseRecord) (Lease, error) {
	if versionID == "" {
		return nil, errors.New("租约的版本标识为空")
	}
	record.VersionID = versionID
	if record.AcquiredAtMs == 0 {
		record.AcquiredAtMs = time.Now().UTC().UnixMilli()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.live[versionID]; exists {
		// 同一版本已有租约：可能是上次启动遗留但未被回收，也可能是并发启动。
		// 无法区分时保守拒绝，由调用方提示用户。
		return nil, errors.New("该版本已有运行中的游戏，请先退出")
	}
	m.live[versionID] = record
	if err := m.persistLocked(); err != nil {
		delete(m.live, versionID)
		return nil, err
	}
	return &leaseHandle{manager: m, versionID: versionID}, nil
}

// release 释放指定版本的租约。
func (m *LeaseManager) release(versionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.live[versionID]; !exists {
		return
	}
	delete(m.live, versionID)
	// 落盘失败只影响崩溃恢复的精度，不影响本次释放语义。
	_ = m.persistLocked()
}

// Active 返回指定版本是否仍持有租约。
func (m *LeaseManager) Active(versionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.live[versionID]
	return exists
}

// ReadActiveLeases 直接读取磁盘上的租约表，返回版本 ID 集合。
//
// 与 Active 的区别：Active 读内存快照（由本进程登记或 Recover 填充），
// 而本函数每次都读磁盘。**跨包只读查询必须用它**：实例侧从不登记租约，
// 它的内存快照永远是空的，用 Active 会误判为「没有游戏在运行」。
//
// 参数 root 为核心根。文件不存在时返回空集合；读取失败返回错误，
// 调用方应据此保守处理（按「仍在使用」处理）。
func ReadActiveLeases(root string) (map[string]struct{}, error) {
	absolute, resolveErr := artifact.ResolveInsideRoot(root, indexDirName+"/"+leaseFileName)
	if resolveErr != nil {
		return nil, errors.New("租约表路径无效")
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, errors.New("无法读取租约表")
	}
	if len(content) == 0 || len(content) > maxIndexBytes {
		return nil, errors.New("租约表大小异常")
	}

	var table leaseTable
	if unmarshalErr := json.Unmarshal(content, &table); unmarshalErr != nil {
		return nil, errors.New("租约表格式损坏")
	}
	if table.SchemaVersion != leaseSchemaVersion {
		return nil, errors.New("租约表版本不受支持")
	}

	result := make(map[string]struct{}, len(table.Leases))
	for _, record := range table.Leases {
		if record.VersionID != "" {
			result[record.VersionID] = struct{}{}
		}
	}
	return result, nil
}

// ActiveVersions 返回全部被占用的版本，按字典序排列。
func (m *LeaseManager) ActiveVersions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]string, 0, len(m.live))
	for versionID := range m.live {
		result = append(result, versionID)
	}
	sort.Strings(result)
	return result
}

// snapshot 返回当前租约的副本，供重建前检查。
func (m *LeaseManager) snapshot() []LeaseRecord {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]LeaseRecord, 0, len(m.live))
	for _, record := range m.live {
		result = append(result, record)
	}
	return result
}

// persistLocked 把内存中的租约写入磁盘。调用方必须持有 m.mu。
func (m *LeaseManager) persistLocked() error {
	absolute, resolveErr := artifact.ResolveInsideRoot(m.root, indexDirName+"/"+leaseFileName)
	if resolveErr != nil {
		return errors.New("租约表路径无效")
	}
	if mkErr := os.MkdirAll(filepath.Dir(absolute), 0o755); mkErr != nil {
		return errors.New("无法创建租约表目录")
	}

	table := leaseTable{SchemaVersion: leaseSchemaVersion, Leases: make([]LeaseRecord, 0, len(m.live))}
	for _, record := range m.live {
		table.Leases = append(table.Leases, record)
	}
	sort.Slice(table.Leases, func(left, right int) bool {
		return table.Leases[left].VersionID < table.Leases[right].VersionID
	})

	encoded, marshalErr := json.MarshalIndent(table, "", "  ")
	if marshalErr != nil {
		return errors.New("租约表无法编码")
	}
	temp, createErr := os.CreateTemp(filepath.Dir(absolute), ".leases-*.json")
	if createErr != nil {
		return errors.New("无法创建租约表临时文件")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if _, writeErr := temp.Write(encoded); writeErr != nil {
		temp.Close()
		return errors.New("无法写入租约表")
	}
	if syncErr := temp.Sync(); syncErr != nil {
		temp.Close()
		return errors.New("无法同步租约表")
	}
	if closeErr := temp.Close(); closeErr != nil {
		return errors.New("无法关闭租约表临时文件")
	}
	if renameErr := os.Rename(tempPath, absolute); renameErr != nil {
		return errors.New("无法提交租约表")
	}
	return nil
}

// RecoverReport 是一次租约恢复的结果。
type RecoverReport struct {
	// Kept 是被判定为仍有效的租约。
	Kept []string
	// Cleared 是被判定为残留并清除的租约。
	Cleared []string
	// Uncertain 是无法判定、按保守策略保留的租约。
	Uncertain []string
}

// Recover 读取磁盘上的租约表并清理残留记录。
//
// 判定规则：
//   - 进程不存在 → 残留，清除；
//   - 进程存在但启动时间不符 → PID 已被复用，残留，清除；
//   - 进程存在且启动时间相符 → 仍有效，保留；
//   - 无法读取进程信息 → 保守保留，超过宽限期后清除。
//
// 该函数应在应用启动时、允许任何启动或安装之前调用一次。
func (m *LeaseManager) Recover(nowMs int64) (RecoverReport, error) {
	absolute, resolveErr := artifact.ResolveInsideRoot(m.root, indexDirName+"/"+leaseFileName)
	if resolveErr != nil {
		return RecoverReport{}, errors.New("租约表路径无效")
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		if os.IsNotExist(err) {
			return RecoverReport{}, nil
		}
		return RecoverReport{}, errors.New("无法读取租约表")
	}
	if len(content) == 0 || len(content) > maxIndexBytes {
		return RecoverReport{}, errors.New("租约表大小异常")
	}

	var table leaseTable
	if unmarshalErr := json.Unmarshal(content, &table); unmarshalErr != nil {
		return RecoverReport{}, errors.New("租约表格式损坏")
	}
	if table.SchemaVersion != leaseSchemaVersion {
		return RecoverReport{}, errors.New("租约表版本不受支持")
	}

	report := RecoverReport{}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.live = map[string]LeaseRecord{}
	for _, record := range table.Leases {
		state, startMs := processStartTime(record.PID)
		switch state {
		case processGone:
			report.Cleared = append(report.Cleared, record.VersionID)
		case processAlive:
			if startMs == record.ProcessStartMs {
				m.live[record.VersionID] = record
				report.Kept = append(report.Kept, record.VersionID)
			} else {
				// 启动时间不符：PID 已被别的进程复用。
				report.Cleared = append(report.Cleared, record.VersionID)
			}
		default:
			if nowMs-record.AcquiredAtMs > int64(leaseOrphanGrace/time.Millisecond) {
				report.Cleared = append(report.Cleared, record.VersionID)
			} else {
				m.live[record.VersionID] = record
				report.Uncertain = append(report.Uncertain, record.VersionID)
			}
		}
	}

	if persistErr := m.persistLocked(); persistErr != nil {
		return report, persistErr
	}
	return report, nil
}
