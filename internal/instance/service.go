// Package instance persists isolated Minecraft instances and coordinates installation.
package instance

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/database"
	"example.com/ninevoxel/launcher/internal/download"
	"example.com/ninevoxel/launcher/internal/launch"
	"example.com/ninevoxel/launcher/internal/loader"
	"example.com/ninevoxel/launcher/internal/platform"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Config contains editable instance settings; file paths and state are backend-owned.
type Config struct {
	Name          string `json:"name"`
	MCVersion     string `json:"mcVersion"`
	Loader        string `json:"loader"`
	LoaderVersion string `json:"loaderVersion"`
	Partition     string `json:"partition"`
	Icon          string `json:"icon"`

	// MemoryMode 决定内存取值来源；MemoryMB 仅在 Mode 为 custom 时生效。
	//
	// 旧实现用 `MemoryMB == 0` 表达「跟随全局」，无法区分「未设置」与
	// 「显式清空」。显式模式让意图可表达（见 configmode.go）。
	MemoryMode launch.MemoryMode `json:"memoryMode"`
	MemoryMB   int               `json:"memoryMb"`

	// JVMMode 决定是否使用实例自己的 JVM 参数。
	//
	// custom + 空串 = 显式「不加额外参数」，与「跟随全局」语义不同。
	JVMMode launch.JVMMode `json:"jvmMode"`
	JVMArgs string         `json:"jvmArgs"`

	Description string `json:"description"`
	IsFavorite  bool   `json:"isFavorite"`

	// JavaMode 决定 Java 可执行文件的来源；两个字段仅在对应模式下生效。
	JavaMode            launch.JavaMode `json:"javaMode"`
	CustomJavaPath      string          `json:"customJavaPath"`
	CustomJavaRuntimeID string          `json:"customJavaRuntimeId"`
}

type Instance struct {
	Config
	ID                   string  `json:"id"`
	FolderPath           string  `json:"folderPath"`
	JavaVersion          string  `json:"javaVersion"`
	InstallationState    string  `json:"installationState"`
	InstallationProgress float64 `json:"installationProgress"`
	InstallationError    string  `json:"installationError"`
	InstallTaskID        string  `json:"installTaskId"`
	VersionID            string  `json:"versionId"`
	CreatedAtMS          int64   `json:"createdAtMs"`
}

type List struct {
	Items              []Instance `json:"items"`
	SelectedInstanceID string     `json:"selectedInstanceId"`
}

// defaultInstanceGroup 是所有实例默认落入的分组。
const defaultInstanceGroup = "默认分组"

// legacyDefaultPartition 是旧版本代码使用的默认分区名，启动时迁移到默认分组。
const legacyDefaultPartition = "默认分区"

// InstanceGroup 描述一个实例分组及其实例数量。
type InstanceGroup struct {
	Name          string `json:"name"`
	IsDefault     bool   `json:"isDefault"`
	InstanceCount int    `json:"instanceCount"`
	CreatedAtMs   int64  `json:"createdAtMs"`
}

// InstanceGroupList 是分组列表与默认分组名的组合。
type InstanceGroupList struct {
	Items        []InstanceGroup `json:"items"`
	DefaultGroup string          `json:"defaultGroup"`
}

// CreateInstanceGroupRequest 是创建分组的请求。
type CreateInstanceGroupRequest struct {
	Name string `json:"name"`
}

// DeleteInstanceGroupRequest 是删除分组的请求。
//
// 分组内仍有实例时必须提供 MoveTo，后端在同一事务里迁移实例后再删除分组。
type DeleteInstanceGroupRequest struct {
	Name   string `json:"name"`
	MoveTo string `json:"moveTo"`
}

type IDRequest struct {
	InstanceID string `json:"instanceId"`
}
type UpdateRequest struct {
	Config
	InstanceID string `json:"instanceId"`
}

// ChangeLoaderRequest switches an installed instance to another loader release.
type ChangeLoaderRequest struct {
	InstanceID    string `json:"instanceId"`
	Loader        string `json:"loader"`
	LoaderVersion string `json:"loaderVersion"`
}

type InstanceService struct {
	// installLocks 按安装身份串行化安装：同一身份并发安装会互相破坏核心目录。
	installLocks *installLockTable
	mu           sync.Mutex
	db           *sql.DB
	root         string
	ctx          context.Context
	cancel       context.CancelFunc
	workers      sync.WaitGroup
	closed       bool
	// Installer injection keeps persistence tests offline.
	installer func(context.Context, Instance) error
	// installCancels / installDone 用于在删除实例时停止正在进行的安装。
	installCancels map[string]context.CancelFunc
	installDone    map[string]chan struct{}
}

var openInstanceDirectory = func(target string) error { return platform.Current.Directory.OpenDirectory(target) }

func NewWailsService() application.Service { return application.NewService(&InstanceService{}) }

func (s *InstanceService) openLocked() error {
	if s.closed {
		return errors.New("实例服务已关闭")
	}
	if s.db != nil {
		return nil
	}
	if s.root == "" {
		root, err := artifact.DataRoot()
		if err != nil {
			return errors.New("无法访问实例数据目录")
		}
		s.root = root
	}
	// 安装锁表按服务实例创建：核心版本由所有实例共用，同一安装身份的并发
	// 安装必须串行化，否则会同时写 versions/<id>/ 并互相破坏。
	if s.installLocks == nil {
		s.installLocks = newInstallLockTable()
	}
	if err := os.MkdirAll(s.root, 0755); err != nil {
		return errors.New("无法创建实例数据目录")
	}
	db, err := database.Open(filepath.Join(s.root, "app.db"))
	if err != nil {
		return errors.New("无法打开实例数据库")
	}
	if _, err = database.Migrate(context.Background(), db); err != nil {
		db.Close()
		return errors.New("实例数据库迁移失败")
	}
	s.db = db
	s.ctx, s.cancel = context.WithCancel(context.Background())
	if err := s.reconcileLocked(); err != nil {
		db.Close()
		s.db = nil
		s.cancel()
		return err
	}
	items, err := s.listLocked()
	if err != nil {
		db.Close()
		s.db = nil
		s.cancel()
		return err
	}
	for _, item := range items.Items {
		if item.InstallationState == "installing" {
			item.InstallationState = "failed"
			item.InstallationError = "上次安装已中断，请重试安装。"
			if err := s.saveLocked(item); err != nil {
				db.Close()
				s.db = nil
				s.cancel()
				return err
			}
		}
	}
	if err := s.ensureGroupsLocked(); err != nil {
		db.Close()
		s.db = nil
		s.cancel()
		return err
	}
	return nil
}

func (s *InstanceService) ServiceShutdown() error {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.workers.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func validConfig(c Config) (Config, error) {
	c.Name = strings.TrimSpace(c.Name)
	c.MCVersion = strings.TrimSpace(c.MCVersion)
	if c.Name == "" || len([]rune(c.Name)) > 80 {
		return c, errors.New("实例名称需要为 1–80 个字符")
	}
	version, err := artifact.ValidateRelativePath(c.MCVersion)
	if err != nil || version != c.MCVersion || strings.ContainsAny(version, "/\\:") {
		return c, errors.New("请选择有效的 Minecraft 版本")
	}
	if strings.TrimSpace(c.Loader) == "" {
		c.Loader = string(loader.KindVanilla)
	}
	kind, loaderErr := loader.ParseKind(c.Loader)
	if loaderErr != nil {
		return c, errors.New("请选择有效的游戏核心或模组加载器")
	}
	c.Loader = string(kind)
	c.LoaderVersion = strings.TrimSpace(c.LoaderVersion)
	if kind == loader.KindVanilla {
		c.LoaderVersion = ""
	} else if _, versionErr := loader.VersionID(kind, c.MCVersion, c.LoaderVersion); versionErr != nil {
		return c, versionErr
	}
	// 三种覆盖模式的校验与归一化：模式决定取值是否参与启动，
	// 非当前模式的字段会被清空，避免存储里出现误导性的组合。
	if modeErr := applyModes(&c); modeErr != nil {
		return c, modeErr
	}
	if len(c.JVMArgs) > 8192 || len(c.Description) > 4096 || len(c.Partition) > 240 || len(c.Icon) > 128 || len(c.CustomJavaPath) > 4096 || len(c.CustomJavaRuntimeID) > 256 {
		return c, errors.New("实例设置内容过长")
	}
	c.Partition = normalizedPartition(c.Partition)
	if c.Icon == "" {
		c.Icon = "🟩"
	}
	return c, nil
}

func (s *InstanceService) listLocked() (List, error) {
	result := List{Items: []Instance{}}
	rows, err := s.db.Query(`SELECT config_json, selected FROM instances ORDER BY created_at_ms DESC, id`)
	if err != nil {
		return result, errors.New("读取实例列表失败")
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var selected int
		var item Instance
		if err := rows.Scan(&raw, &selected); err != nil {
			return result, errors.New("读取实例失败")
		}
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return result, errors.New("实例配置损坏")
		}
		result.Items = append(result.Items, item)
		if selected != 0 {
			result.SelectedInstanceID = item.ID
		}
	}
	if err := rows.Err(); err != nil {
		return result, errors.New("读取实例列表失败")
	}

	// 派生「实际可启动状态」：核心版本被删除（例如另一个实例卸载加载器时
	// 误删）后，清单里仍写着 installed，但启动必然失败。这里按核心现状修正
	// 返回给界面的状态，让用户看到「需要重新安装」而不是一个装好的实例。
	//
	// 只改返回值、不回写存储：列表是只读操作，回写会让一次查询变成写事务，
	// 也会把「核心暂时不可访问」误记成「实例未安装」。
	for index := range result.Items {
		result.Items[index] = s.withActualInstallState(result.Items[index])
	}
	return result, nil
}

// withActualInstallState 用核心现状修正实例的安装状态。
//
// 只处理「清单说已安装、但核心里找不到对应版本」这一种情况：其余状态
// （安装中、失败、未安装）由各自的流程维护，不应在这里覆盖。
func (s *InstanceService) withActualInstallState(item Instance) Instance {
	if item.InstallationState != "installed" {
		return item
	}
	versionID := strings.TrimSpace(item.VersionID)
	if versionID == "" {
		versionID = strings.TrimSpace(item.MCVersion)
	}
	if coreVersionInstalled(s.root, versionID) {
		return item
	}
	item.InstallationState = "not_installed"
	item.InstallationProgress = 0
	return item
}

func (s *InstanceService) getLocked(id string) (Instance, error) {
	if !validateInstanceID(id) {
		return Instance{}, errInstanceNotFound
	}
	directory, err := managedPath(s.root, activeInstanceRel(id))
	if err != nil {
		return Instance{}, err
	}
	manifest, err := readInstanceManifest(directory)
	if errors.Is(err, errManifestNotFound) {
		return Instance{}, errInstanceNotFound
	}
	if err != nil {
		return Instance{}, err
	}
	if manifest.ID != id {
		return Instance{}, errors.New("实例清单标识不匹配")
	}
	return manifest.toInstance(activeGameRel(id)), nil
}

func (s *InstanceService) saveLocked(item Instance) error {
	item.FolderPath = activeGameRel(item.ID)
	if err := s.writeItemManifestLocked(item); err != nil {
		return err
	}
	if err := s.upsertIndexLocked(item); err != nil {
		return errors.New("实例已写入目录，但索引更新失败")
	}
	return nil
}

// ensureGroupsLocked 初始化默认分组，并把旧实例的默认分区与已有分区回填为分组。
//
// 迁移是幂等的：每次打开服务都会执行，但只有在发现缺失分组或旧默认名时才写库。
func (s *InstanceService) ensureGroupsLocked() error {
	nowMs := time.Now().UnixMilli()
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO instance_groups(name,is_default,created_at_ms,updated_at_ms) VALUES(?,1,?,?)`,
		defaultInstanceGroup, nowMs, nowMs,
	); err != nil {
		return errors.New("初始化默认分组失败")
	}

	rows, err := s.db.Query(`SELECT id, config_json FROM instances`)
	if err != nil {
		return errors.New("读取实例分组失败")
	}
	type pendingUpdate struct {
		item Instance
		raw  string
	}
	updates := make([]pendingUpdate, 0)
	groups := map[string]struct{}{defaultInstanceGroup: {}}
	for rows.Next() {
		var id, raw string
		if scanErr := rows.Scan(&id, &raw); scanErr != nil {
			rows.Close()
			return errors.New("读取实例分组失败")
		}
		var item Instance
		if unmarshalErr := json.Unmarshal([]byte(raw), &item); unmarshalErr != nil {
			rows.Close()
			return errors.New("实例配置损坏")
		}
		partition := normalizedPartition(item.Partition)
		groups[partition] = struct{}{}
		if item.Partition == partition {
			continue
		}
		item.Config.Partition = partition
		item.FolderPath = activeGameRel(id)
		encoded, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			rows.Close()
			return errors.New("实例配置无法保存")
		}
		updates = append(updates, pendingUpdate{item: item, raw: string(encoded)})
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		rows.Close()
		return errors.New("读取实例分组失败")
	}
	rows.Close()

	for _, update := range updates {
		if err := s.writeItemManifestLocked(update.item); err != nil {
			return errors.New("迁移实例分组失败")
		}
		if _, err := s.db.Exec(
			`UPDATE instances SET config_json=?,updated_at_ms=? WHERE id=?`,
			update.raw, nowMs, update.item.ID,
		); err != nil {
			return errors.New("迁移实例分组失败")
		}
	}
	for name := range groups {
		if _, err := s.db.Exec(
			`INSERT OR IGNORE INTO instance_groups(name,is_default,created_at_ms,updated_at_ms) VALUES(?,0,?,?)`,
			name, nowMs, nowMs,
		); err != nil {
			return errors.New("初始化实例分组失败")
		}
	}
	return nil
}

// groupExistsLocked 判断分组是否存在。
func (s *InstanceService) groupExistsLocked(name string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM instance_groups WHERE name=?`, name).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("读取实例分组失败")
	}
	return true, nil
}

// requireGroupLocked 要求分组已存在，避免写入悬空分组名。
func (s *InstanceService) requireGroupLocked(name string) error {
	exists, err := s.groupExistsLocked(name)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("请选择有效的实例分组")
	}
	return nil
}

// groupCountsLocked 统计每个分组内的实例数量。
func (s *InstanceService) groupCountsLocked() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT config_json FROM instances`)
	if err != nil {
		return nil, errors.New("读取实例分组失败")
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var raw string
		if scanErr := rows.Scan(&raw); scanErr != nil {
			return nil, errors.New("读取实例分组失败")
		}
		var item Instance
		if unmarshalErr := json.Unmarshal([]byte(raw), &item); unmarshalErr != nil {
			return nil, errors.New("实例配置损坏")
		}
		partition := strings.TrimSpace(item.Partition)
		if partition == "" {
			partition = defaultInstanceGroup
		}
		counts[partition]++
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, errors.New("读取实例分组失败")
	}
	return counts, nil
}

// listGroupsLocked 返回分组列表与默认分组名。
func (s *InstanceService) listGroupsLocked() (InstanceGroupList, error) {
	counts, err := s.groupCountsLocked()
	if err != nil {
		return InstanceGroupList{}, err
	}
	rows, err := s.db.Query(
		`SELECT name,is_default,created_at_ms FROM instance_groups ORDER BY is_default DESC,created_at_ms,name`,
	)
	if err != nil {
		return InstanceGroupList{}, errors.New("读取实例分组失败")
	}
	defer rows.Close()
	result := InstanceGroupList{Items: []InstanceGroup{}, DefaultGroup: defaultInstanceGroup}
	for rows.Next() {
		var item InstanceGroup
		var isDefault int
		if scanErr := rows.Scan(&item.Name, &isDefault, &item.CreatedAtMs); scanErr != nil {
			return InstanceGroupList{}, errors.New("读取实例分组失败")
		}
		item.IsDefault = isDefault != 0
		item.InstanceCount = counts[item.Name]
		if item.IsDefault {
			result.DefaultGroup = item.Name
		}
		result.Items = append(result.Items, item)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return InstanceGroupList{}, errors.New("读取实例分组失败")
	}
	return result, nil
}

// ListInstanceGroups 返回所有实例分组。
func (s *InstanceService) ListInstanceGroups() (InstanceGroupList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return InstanceGroupList{}, err
	}
	if err := s.reconcileLocked(); err != nil {
		return InstanceGroupList{}, err
	}
	if err := s.ensureGroupsLocked(); err != nil {
		return InstanceGroupList{}, err
	}
	return s.listGroupsLocked()
}

// CreateInstanceGroup 创建一个新的实例分组。
func (s *InstanceService) CreateInstanceGroup(req CreateInstanceGroupRequest) (InstanceGroupList, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 80 {
		return InstanceGroupList{}, errors.New("分组名称需要为 1–80 个字符")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return InstanceGroupList{}, err
	}
	exists, err := s.groupExistsLocked(name)
	if err != nil {
		return InstanceGroupList{}, err
	}
	if exists {
		return InstanceGroupList{}, errors.New("该分组已存在")
	}
	nowMs := time.Now().UnixMilli()
	if _, err := s.db.Exec(
		`INSERT INTO instance_groups(name,is_default,created_at_ms,updated_at_ms) VALUES(?,0,?,?)`,
		name, nowMs, nowMs,
	); err != nil {
		return InstanceGroupList{}, errors.New("创建分组失败")
	}
	return s.listGroupsLocked()
}

// DeleteInstanceGroup 删除分组；非空分组必须把实例迁移到目标分组。
func (s *InstanceService) DeleteInstanceGroup(req DeleteInstanceGroupRequest) (InstanceGroupList, error) {
	name := strings.TrimSpace(req.Name)
	moveTo := strings.TrimSpace(req.MoveTo)
	if name == "" {
		return InstanceGroupList{}, errors.New("请选择要删除的分组")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return InstanceGroupList{}, err
	}
	var isDefault int
	err := s.db.QueryRow(`SELECT is_default FROM instance_groups WHERE name=?`, name).Scan(&isDefault)
	if errors.Is(err, sql.ErrNoRows) {
		return InstanceGroupList{}, errors.New("分组不存在")
	}
	if err != nil {
		return InstanceGroupList{}, errors.New("读取实例分组失败")
	}
	if isDefault != 0 {
		return InstanceGroupList{}, errors.New("默认分组不能删除")
	}
	counts, err := s.groupCountsLocked()
	if err != nil {
		return InstanceGroupList{}, err
	}
	if counts[name] == 0 {
		if _, err := s.db.Exec(`DELETE FROM instance_groups WHERE name=?`, name); err != nil {
			return InstanceGroupList{}, errors.New("删除分组失败")
		}
		return s.listGroupsLocked()
	}
	if moveTo == "" {
		return InstanceGroupList{}, errors.New("分组内仍有实例，请先移动到其他分组")
	}
	if moveTo == name {
		return InstanceGroupList{}, errors.New("不能把实例移动到正在删除的分组")
	}
	if err := s.requireGroupLocked(moveTo); err != nil {
		return InstanceGroupList{}, err
	}

	rows, err := s.db.Query(`SELECT id, config_json FROM instances`)
	if err != nil {
		return InstanceGroupList{}, errors.New("读取实例分组失败")
	}
	type pendingUpdate struct {
		item Instance
		raw  string
	}
	updates := make([]pendingUpdate, 0)
	for rows.Next() {
		var id, raw string
		if scanErr := rows.Scan(&id, &raw); scanErr != nil {
			rows.Close()
			return InstanceGroupList{}, errors.New("读取实例分组失败")
		}
		var item Instance
		if unmarshalErr := json.Unmarshal([]byte(raw), &item); unmarshalErr != nil {
			rows.Close()
			return InstanceGroupList{}, errors.New("实例配置损坏")
		}
		partition := normalizedPartition(item.Partition)
		if partition != name {
			continue
		}
		item.Config.Partition = moveTo
		item.FolderPath = activeGameRel(id)
		encoded, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			rows.Close()
			return InstanceGroupList{}, errors.New("实例配置无法保存")
		}
		updates = append(updates, pendingUpdate{item: item, raw: string(encoded)})
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		rows.Close()
		return InstanceGroupList{}, errors.New("读取实例分组失败")
	}
	rows.Close()
	for _, update := range updates {
		if err := s.writeItemManifestLocked(update.item); err != nil {
			return InstanceGroupList{}, errors.New("迁移实例分组失败")
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return InstanceGroupList{}, errors.New("删除分组失败")
	}
	defer tx.Rollback()
	nowMs := time.Now().UnixMilli()
	for _, update := range updates {
		if _, err := tx.Exec(`UPDATE instances SET config_json=?,updated_at_ms=? WHERE id=?`, update.raw, nowMs, update.item.ID); err != nil {
			return InstanceGroupList{}, errors.New("迁移实例分组失败")
		}
	}
	if _, err := tx.Exec(`DELETE FROM instance_groups WHERE name=?`, name); err != nil {
		return InstanceGroupList{}, errors.New("删除分组失败")
	}
	if err := tx.Commit(); err != nil {
		return InstanceGroupList{}, errors.New("删除分组失败")
	}
	return s.listGroupsLocked()
}

func (s *InstanceService) ListInstances() (List, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return List{}, err
	}
	if err := s.reconcileLocked(); err != nil {
		return List{}, err
	}
	return s.listLocked()
}

func (s *InstanceService) CreateInstance(c Config) (Instance, error) {
	c, err := validConfig(c)
	if err != nil {
		return Instance{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return Instance{}, err
	}
	if err := s.requireGroupLocked(c.Partition); err != nil {
		return Instance{}, err
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return Instance{}, errors.New("创建实例标识失败")
	}
	id := "inst-" + hex.EncodeToString(token)
	item := Instance{Config: c, ID: id, FolderPath: "instances/" + id + "/.minecraft", InstallationState: "not_installed", CreatedAtMS: time.Now().UnixMilli()}
	dir, err := s.gameDir(item)
	if err != nil {
		return Instance{}, err
	}
	instanceDir := filepath.Dir(dir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Instance{}, errors.New("创建实例目录失败")
	}
	if err := writeInstanceManifest(instanceDir, manifestFromInstance(item, 0)); err != nil {
		_ = os.RemoveAll(instanceDir)
		return Instance{}, err
	}
	raw, err := json.Marshal(item)
	if err != nil {
		_ = os.RemoveAll(instanceDir)
		return Instance{}, errors.New("实例配置无法保存")
	}
	tx, err := s.db.Begin()
	if err != nil {
		_ = os.RemoveAll(instanceDir)
		return Instance{}, errors.New("保存实例失败")
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO instances(id,config_json,selected,created_at_ms,updated_at_ms) VALUES(?,?,0,?,?)`, id, string(raw), item.CreatedAtMS, item.CreatedAtMS)
	if err == nil {
		_, err = tx.Exec(`UPDATE instances SET selected=1 WHERE id=? AND NOT EXISTS(SELECT 1 FROM instances WHERE selected=1)`, id)
	}
	if err != nil {
		_ = os.RemoveAll(instanceDir)
		return Instance{}, errors.New("保存实例失败")
	}
	if err := tx.Commit(); err != nil {
		_ = os.RemoveAll(instanceDir)
		return Instance{}, errors.New("保存实例失败")
	}
	return item, nil
}

func (s *InstanceService) UpdateInstance(req UpdateRequest) (Instance, error) {
	c, err := validConfig(req.Config)
	if err != nil {
		return Instance{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return Instance{}, err
	}
	item, err := s.getLocked(req.InstanceID)
	if err != nil {
		return Instance{}, err
	}
	if err := s.requireGroupLocked(c.Partition); err != nil {
		return Instance{}, err
	}
	if c.MCVersion != item.MCVersion || c.Loader != item.Loader || c.LoaderVersion != item.LoaderVersion {
		return Instance{}, errors.New("修改游戏版本或加载器请使用对应管理操作")
	}
	item.Config = c
	if err := s.saveLocked(item); err != nil {
		return Instance{}, err
	}
	return item, nil
}

func (s *InstanceService) SelectInstance(req IDRequest) (List, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return List{}, err
	}
	if _, err := s.getLocked(req.InstanceID); err != nil {
		return List{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return List{}, errors.New("选择实例失败")
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE instances SET selected=0 WHERE selected=1`); err == nil {
		_, err = tx.Exec(`UPDATE instances SET selected=1 WHERE id=?`, req.InstanceID)
	}
	if err != nil {
		return List{}, errors.New("选择实例失败")
	}
	if err := tx.Commit(); err != nil {
		return List{}, errors.New("选择实例失败")
	}
	return s.listLocked()
}

// DeleteInstance moves the instance and its files into the instance recycle bin.
//
// 正在安装的实例会先取消下载并等待安装协程退出，再归档目录；
// 这样用户不必等安装完成就能移除实例。
func (s *InstanceService) DeleteInstance(req IDRequest) (List, error) {
	s.mu.Lock()
	if err := s.openLocked(); err != nil {
		s.mu.Unlock()
		return List{}, err
	}
	if err := s.reconcileLocked(); err != nil {
		s.mu.Unlock()
		return List{}, err
	}
	item, err := s.getLocked(req.InstanceID)
	if err != nil {
		if errors.Is(err, errInstanceNotFound) {
			list, listErr := s.listLocked()
			s.mu.Unlock()
			return list, listErr
		}
		if indexed, indexErr := s.indexedItemLocked(req.InstanceID); indexErr == nil {
			item = indexed
		} else {
			s.mu.Unlock()
			return List{}, err
		}
	}
	cancel := s.installCancels[item.ID]
	done := s.installDone[item.ID]
	if item.InstallationState == "installing" || done != nil {
		s.mu.Unlock()
		// 先取消该实例的下载计划，再取消安装上下文，避免协程卡在轮询里。
		_ = download.CancelDownloadOperation(item.ID)
		if cancel != nil {
			cancel()
		}
		if done != nil {
			select {
			case <-done:
			case <-time.After(20 * time.Second):
			}
		}
		s.mu.Lock()
		if err := s.openLocked(); err != nil {
			s.mu.Unlock()
			return List{}, err
		}
		item, err = s.getLocked(req.InstanceID)
		if err != nil {
			indexed, indexErr := s.indexedItemLocked(req.InstanceID)
			if indexErr != nil {
				s.mu.Unlock()
				return List{}, err
			}
			item = indexed
		}
	}
	list, deleteErr := s.deleteInstanceLocked(item)
	s.mu.Unlock()
	return list, deleteErr
}

// describeRenameFailure 把目录改名失败翻译成可操作的说明。
//
// Windows 上「Access is denied」有两种完全不同的成因，处置方式也不同：
//
//	目录被占用   有进程把它或它的子目录当作工作目录 / 持有句柄
//	权限不足     目录的 ACL 不允许改名
//
// 不能只看错误文本——两者都是 Access is denied。用「能否在目录内写文件」
// 区分：能写说明 ACL 没问题，问题在占用。实测中把占用误报成「没有写入
// 权限」会让用户去查文件权限，而实际只要关掉游戏。
func describeRenameFailure(target string, err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "cross-device link"),
		strings.Contains(text, "not same device"):
		return "回收站与实例不在同一磁盘卷"
	case strings.Contains(text, "being used by another process"),
		strings.Contains(text, "used by another process"),
		strings.Contains(text, "resource busy"),
		strings.Contains(text, "device or resource busy"):
		return "实例目录仍被占用，请先关闭游戏再删除"
	case strings.Contains(text, "access is denied"),
		strings.Contains(text, "permission denied"):
		if directoryWritable(target) {
			return "实例目录被占用（通常是游戏尚未完全退出），请关闭游戏后重试"
		}
		return "没有该实例目录的写入权限"
	default:
		return "无法移动实例目录"
	}
}

// directoryWritable 判断目录本身是否可写。
//
// 只用于区分「占用」与「权限」：写入一个临时探针文件即可，不修改任何
// 既有内容。探针创建失败或删除失败都视为不可写。
func directoryWritable(directory string) bool {
	probe, err := os.CreateTemp(directory, ".archive-probe-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	if removeErr := os.Remove(name); removeErr != nil {
		return false
	}
	return true
}

// renameWithRetry 带退避重试地改名目录。
//
// Windows 上杀毒软件扫描、索引服务会在短时间内持有句柄，这类占用通常
// 几十到几百毫秒后释放。重试几次可以避免「明明没在玩游戏却删不掉」。
// 长期占用（游戏仍在运行）重试也无效，由调用方给出占用提示。
func renameWithRetry(source, target string) error {
	const attempts = 5
	delay := 80 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := os.Rename(source, target); err == nil {
			return nil
		} else {
			lastErr = err
		}
		// 最后一次失败后不再等待。
		if attempt == attempts-1 {
			break
		}
		time.Sleep(delay)
		delay *= 2
	}
	return lastErr
}

// deleteInstanceLocked 执行实际的归档与删除；调用方必须持有 s.mu。
func (s *InstanceService) deleteInstanceLocked(item Instance) (List, error) {
	if item.InstallationState == "installing" {
		return List{}, errors.New("正在取消安装，请稍后重试")
	}
	game, err := s.gameDir(item)
	if err != nil {
		return List{}, err
	}
	source := filepath.Dir(game)
	// 运行中的游戏会把实例目录锁住（Windows 上目录不能改名）：
	// 进程的工作目录落在 `<实例>/.minecraft`，句柄未释放前改名必然失败。
	// 提前检查租约，把「删到一半失败」变成「一开始就说清楚」。
	if item.VersionID != "" && s.versionInUseByRunningGame(item.VersionID) {
		return List{}, errors.New("该实例的游戏仍在运行，请先退出游戏后再删除")
	}
	trashedAtMS := time.Now().UnixMilli()
	trash, err := managedPath(s.root, trashRootRel+"/"+item.ID+fmt.Sprint("-", time.Now().UnixNano()))
	if err != nil {
		return List{}, err
	}
	moved := false
	marked := false
	if _, err := os.Stat(source); err == nil {
		if err := os.MkdirAll(filepath.Dir(trash), 0755); err != nil {
			return List{}, fmt.Errorf("归档实例失败：无法创建回收站目录（%w）", err)
		}
		if err := s.writeItemManifestStateLocked(item, trashedAtMS); err == nil {
			marked = true
		}
		if err := renameWithRetry(source, trash); err != nil {
			if marked {
				_ = s.writeItemManifestStateLocked(item, 0)
			}
			// 带上底层原因：Access is denied 既可能是占用也可能是权限，
			// 只回一句「归档失败」会让用户查错方向。
			return List{}, fmt.Errorf("归档实例失败：%s（%w）", describeRenameFailure(source, err), err)
		}
		moved = true
	} else if !os.IsNotExist(err) {
		return List{}, fmt.Errorf("无法访问实例目录（%w）", err)
	}
	tx, err := s.db.Begin()
	if err == nil {
		_, err = tx.Exec(`DELETE FROM instances WHERE id=?`, item.ID)
		if err == nil {
			_, err = tx.Exec(`UPDATE instances SET selected=1 WHERE id=(SELECT id FROM instances ORDER BY created_at_ms DESC LIMIT 1) AND NOT EXISTS(SELECT 1 FROM instances WHERE selected=1)`)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
	if err != nil {
		if moved {
			_ = os.Rename(trash, source)
			if marked {
				_ = s.writeItemManifestStateLocked(item, 0)
			}
		}
		return List{}, errors.New("移除实例失败")
	}
	return s.listLocked()
}

func (s *InstanceService) InstallInstance(req IDRequest) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return Instance{}, err
	}
	item, err := s.getLocked(req.InstanceID)
	if err != nil {
		return Instance{}, err
	}
	return s.startInstallLocked(item)
}

// startInstallLocked marks an instance as installing and starts its worker.
// The caller must hold s.mu and must have completed path validation.
func (s *InstanceService) startInstallLocked(item Instance) (Instance, error) {
	if item.InstallationState == "installing" {
		return item, nil
	}
	if _, err := s.gameDir(item); err != nil {
		return Instance{}, err
	}
	item.InstallationState = "installing"
	item.InstallationError = ""
	item.InstallationProgress = 0
	item.InstallTaskID = ""
	if err := s.saveLocked(item); err != nil {
		return Instance{}, err
	}
	if s.installCancels == nil {
		s.installCancels = make(map[string]context.CancelFunc)
		s.installDone = make(map[string]chan struct{})
	}
	installCtx, cancelInstall := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.installCancels[item.ID] = cancelInstall
	s.installDone[item.ID] = done
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer func() {
			close(done)
			cancelInstall()
			s.mu.Lock()
			delete(s.installCancels, item.ID)
			delete(s.installDone, item.ID)
			s.mu.Unlock()
		}()
		install := s.installer
		if install == nil {
			install = s.install
		}
		err := install(installCtx, item)
		s.mu.Lock()
		defer s.mu.Unlock()
		latest, readErr := s.getLocked(item.ID)
		if readErr != nil {
			return
		}
		if err != nil {
			latest.InstallationState = "failed"
			latest.InstallationError = err.Error()
		} else {
			latest.InstallationState = "installed"
			latest.InstallationError = ""
			latest.InstallationProgress = 1
		}
		if saveErr := s.saveLocked(latest); saveErr != nil {
			return
		}
	}()
	return item, nil
}

func (s *InstanceService) gameDir(item Instance) (string, error) {
	if len(item.ID) != 37 || !strings.HasPrefix(item.ID, "inst-") {
		return "", errors.New("实例目录无效")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(item.ID, "inst-")); err != nil {
		return "", errors.New("实例目录无效")
	}
	expected := "instances/" + item.ID + "/.minecraft"
	if item.FolderPath != expected {
		return "", errors.New("实例目录无效")
	}
	return managedPath(s.root, expected)
}

// managedPath rejects links in existing ancestors, including missing leaf paths.
func managedPath(root, relative string) (string, error) {
	normalized, err := artifact.ValidateRelativePath(relative)
	if err != nil {
		return "", errors.New("实例路径越界")
	}
	candidate := root
	for _, part := range strings.Split(normalized, "/") {
		candidate = filepath.Join(candidate, part)
		info, err := os.Lstat(candidate)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("实例目录不允许符号链接")
		}
		if err != nil && !os.IsNotExist(err) {
			return "", errors.New("无法访问实例目录")
		}
	}
	return candidate, nil
}

// OpenInstanceFolder resolves the backend-owned game directory before opening it.
func (s *InstanceService) OpenInstanceFolder(req IDRequest) error {
	s.mu.Lock()
	if err := s.openLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	item, err := s.getLocked(req.InstanceID)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	dir, err := s.gameDir(item)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		return errors.New("实例目录不存在")
	}
	if err := openInstanceDirectory(dir); err != nil {
		return errors.New("无法打开实例目录")
	}
	return nil
}
