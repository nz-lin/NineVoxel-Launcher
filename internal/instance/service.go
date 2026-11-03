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
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Config contains editable instance settings; file paths and state are backend-owned.
type Config struct {
	Name           string `json:"name"`
	MCVersion      string `json:"mcVersion"`
	Loader         string `json:"loader"`
	Partition      string `json:"partition"`
	Icon           string `json:"icon"`
	MemoryMB       int    `json:"memoryMb"`
	JVMArgs        string `json:"jvmArgs"`
	Description    string `json:"description"`
	IsFavorite     bool   `json:"isFavorite"`
	CustomJavaPath string `json:"customJavaPath"`
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

type InstanceService struct {
	mu      sync.Mutex
	db      *sql.DB
	root    string
	ctx     context.Context
	cancel  context.CancelFunc
	workers sync.WaitGroup
	closed  bool
	// Installer injection keeps persistence tests offline.
	installer func(context.Context, Instance) error
	// installCancels / installDone 用于在删除实例时停止正在进行的安装。
	installCancels map[string]context.CancelFunc
	installDone    map[string]chan struct{}
}

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
	if c.Loader != "" && c.Loader != "vanilla" {
		return c, errors.New("当前仅支持原版 Minecraft 安装")
	}
	c.Loader = "vanilla"
	if c.MemoryMB < 512 || c.MemoryMB > 1048576 {
		return c, errors.New("实例内存必须介于 512 MB 和 1048576 MB 之间")
	}
	if len(c.JVMArgs) > 8192 || len(c.Description) > 4096 || len(c.Partition) > 240 || len(c.Icon) > 128 {
		return c, errors.New("实例设置内容过长")
	}
	if c.Partition == "" {
		c.Partition = defaultInstanceGroup
	}
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
	return result, nil
}

func (s *InstanceService) getLocked(id string) (Instance, error) {
	var raw string
	if err := s.db.QueryRow(`SELECT config_json FROM instances WHERE id = ?`, id).Scan(&raw); err != nil {
		return Instance{}, errors.New("实例不存在")
	}
	var item Instance
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return item, errors.New("实例配置损坏")
	}
	return item, nil
}

func (s *InstanceService) saveLocked(item Instance) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return errors.New("实例配置无法保存")
	}
	_, err = s.db.Exec(`UPDATE instances SET config_json=?,updated_at_ms=? WHERE id=?`, string(raw), time.Now().UnixMilli(), item.ID)
	if err != nil {
		return errors.New("保存实例失败")
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
		id  string
		raw string
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
		partition := strings.TrimSpace(item.Partition)
		if partition == "" || partition == legacyDefaultPartition {
			partition = defaultInstanceGroup
		}
		groups[partition] = struct{}{}
		if item.Partition == partition {
			continue
		}
		item.Config.Partition = partition
		encoded, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			rows.Close()
			return errors.New("实例配置无法保存")
		}
		updates = append(updates, pendingUpdate{id: id, raw: string(encoded)})
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		rows.Close()
		return errors.New("读取实例分组失败")
	}
	rows.Close()

	for _, update := range updates {
		if _, err := s.db.Exec(
			`UPDATE instances SET config_json=?,updated_at_ms=? WHERE id=?`,
			update.raw, nowMs, update.id,
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

	tx, err := s.db.Begin()
	if err != nil {
		return InstanceGroupList{}, errors.New("删除分组失败")
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, config_json FROM instances`)
	if err != nil {
		return InstanceGroupList{}, errors.New("读取实例分组失败")
	}
	type pendingUpdate struct {
		id  string
		raw string
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
		partition := strings.TrimSpace(item.Partition)
		if partition == "" {
			partition = defaultInstanceGroup
		}
		if partition != name {
			continue
		}
		item.Config.Partition = moveTo
		encoded, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			rows.Close()
			return InstanceGroupList{}, errors.New("实例配置无法保存")
		}
		updates = append(updates, pendingUpdate{id: id, raw: string(encoded)})
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		rows.Close()
		return InstanceGroupList{}, errors.New("读取实例分组失败")
	}
	rows.Close()
	nowMs := time.Now().UnixMilli()
	for _, update := range updates {
		if _, err := tx.Exec(`UPDATE instances SET config_json=?,updated_at_ms=? WHERE id=?`, update.raw, nowMs, update.id); err != nil {
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
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Instance{}, errors.New("创建实例目录失败")
	}
	raw, _ := json.Marshal(item)
	tx, err := s.db.Begin()
	if err != nil {
		return Instance{}, errors.New("保存实例失败")
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO instances(id,config_json,selected,created_at_ms,updated_at_ms) VALUES(?,?,0,?,?)`, id, string(raw), item.CreatedAtMS, item.CreatedAtMS)
	if err == nil {
		_, err = tx.Exec(`UPDATE instances SET selected=1 WHERE id=? AND NOT EXISTS(SELECT 1 FROM instances WHERE selected=1)`, id)
	}
	if err != nil {
		return Instance{}, errors.New("保存实例失败")
	}
	if err := tx.Commit(); err != nil {
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
	if c.MCVersion != item.MCVersion || c.Loader != item.Loader {
		return Instance{}, errors.New("修改游戏版本需创建新实例")
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

// DeleteInstance removes the record and archives user files for recovery.
//
// 正在安装的实例会先取消下载并等待安装协程退出，再归档目录；
// 这样用户不必等安装完成就能移除实例。
func (s *InstanceService) DeleteInstance(req IDRequest) (List, error) {
	s.mu.Lock()
	if err := s.openLocked(); err != nil {
		s.mu.Unlock()
		return List{}, err
	}
	item, err := s.getLocked(req.InstanceID)
	if err != nil {
		s.mu.Unlock()
		return List{}, err
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
			s.mu.Unlock()
			return List{}, err
		}
	}
	list, deleteErr := s.deleteInstanceLocked(item)
	s.mu.Unlock()
	return list, deleteErr
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
	trash, err := managedPath(s.root, "instances/.trash/"+item.ID+fmt.Sprint("-", time.Now().UnixNano()))
	if err != nil {
		return List{}, err
	}
	moved := false
	if _, err := os.Stat(source); err == nil {
		if err := os.MkdirAll(filepath.Dir(trash), 0755); err != nil {
			return List{}, errors.New("归档实例失败")
		}
		if err := os.Rename(source, trash); err != nil {
			return List{}, errors.New("归档实例失败")
		}
		moved = true
	} else if !os.IsNotExist(err) {
		return List{}, errors.New("无法访问实例目录")
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
	app := application.Get()
	if app == nil {
		return errors.New("桌面应用尚未就绪")
	}
	if err := app.Browser.OpenFile(dir); err != nil {
		return errors.New("无法打开实例目录")
	}
	return nil
}
