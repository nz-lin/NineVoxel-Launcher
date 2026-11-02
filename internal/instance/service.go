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
		c.Partition = "默认分区"
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
func (s *InstanceService) DeleteInstance(req IDRequest) (List, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return List{}, err
	}
	item, err := s.getLocked(req.InstanceID)
	if err != nil {
		return List{}, err
	}
	if item.InstallationState == "installing" {
		return List{}, errors.New("请等待安装结束后再移除实例")
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
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		install := s.installer
		if install == nil {
			install = s.install
		}
		err := install(s.ctx, item)
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
