package instance

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errInstanceNotFound = errors.New("实例不存在")

func (s *InstanceService) indexedItemLocked(id string) (Instance, error) {
	var raw string
	err := s.db.QueryRow(`SELECT config_json FROM instances WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Instance{}, errInstanceNotFound
	}
	if err != nil {
		return Instance{}, errors.New("读取实例索引失败")
	}
	var item Instance
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return Instance{}, errors.New("实例索引损坏")
	}
	if item.ID != id || !validateInstanceID(id) {
		return Instance{}, errors.New("实例索引标识无效")
	}
	config, err := validConfig(item.Config)
	if err != nil {
		return Instance{}, err
	}
	item.Config = config
	item.FolderPath = activeGameRel(id)
	return item, nil
}

func (s *InstanceService) upsertIndexLocked(item Instance) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return errors.New("实例配置无法保存")
	}
	nowMS := time.Now().UnixMilli()
	createdMS := item.CreatedAtMS
	if createdMS <= 0 {
		createdMS = nowMS
	}
	_, err = s.db.Exec(
		`INSERT INTO instances(id,config_json,selected,created_at_ms,updated_at_ms) VALUES(?,?,0,?,?)
		 ON CONFLICT(id) DO UPDATE SET config_json=excluded.config_json,created_at_ms=excluded.created_at_ms,updated_at_ms=excluded.updated_at_ms
		 WHERE instances.config_json<>excluded.config_json OR instances.created_at_ms<>excluded.created_at_ms`,
		item.ID, string(raw), createdMS, nowMS,
	)
	if err != nil {
		return errors.New("保存实例索引失败")
	}
	return nil
}

func (s *InstanceService) removeIndexLocked(id string) error {
	if _, err := s.db.Exec(`DELETE FROM instances WHERE id=?`, id); err != nil {
		return errors.New("删除实例索引失败")
	}
	return nil
}

func (s *InstanceService) ensureSelectionLocked() error {
	_, err := s.db.Exec(
		`UPDATE instances SET selected=1
		 WHERE id=(SELECT id FROM instances ORDER BY created_at_ms DESC,id LIMIT 1)
		 AND NOT EXISTS(SELECT 1 FROM instances WHERE selected=1)`,
	)
	if err != nil {
		return errors.New("选择默认实例失败")
	}
	return nil
}

func (s *InstanceService) writeItemManifestLocked(item Instance) error {
	return s.writeItemManifestStateLocked(item, 0)
}

func (s *InstanceService) writeItemManifestStateLocked(item Instance, trashedAtMS int64) error {
	if !validateInstanceID(item.ID) {
		return errors.New("实例目录无效")
	}
	item.FolderPath = activeGameRel(item.ID)
	game, err := s.gameDir(item)
	if err != nil {
		return err
	}
	return writeInstanceManifest(filepath.Dir(game), manifestFromInstance(item, trashedAtMS))
}

// reconcileLocked treats instance.json as the source of truth and rebuilds the
// SQLite index from it. Existing database rows only bootstrap manifests during
// the one-time migration from older launcher versions.
func (s *InstanceService) reconcileLocked() error {
	root, err := managedPath(s.root, instancesRootRel)
	if os.IsNotExist(err) {
		if mkdirErr := os.MkdirAll(root, 0755); mkdirErr != nil {
			return errors.New("创建实例根目录失败")
		}
		return s.removeMissingIndexesLocked(nil)
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		if mkdirErr := os.MkdirAll(root, 0755); mkdirErr != nil {
			return errors.New("创建实例根目录失败")
		}
		return s.removeMissingIndexesLocked(nil)
	}
	if err != nil {
		return errors.New("读取实例目录失败")
	}
	present := make(map[string]bool)
	for _, entry := range entries {
		name := entry.Name()
		if name == ".trash" || !entry.IsDir() || !validateInstanceID(name) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		directory := filepath.Join(root, name)
		manifest, readErr := readInstanceManifest(directory)
		switch {
		case errors.Is(readErr, errManifestNotFound):
			if indexed, indexErr := s.indexedItemLocked(name); indexErr == nil {
				manifest = manifestFromInstance(indexed, 0)
			} else if recovered, recoverErr := recoverManifest(directory, name, 0, 0); recoverErr == nil {
				manifest = recovered
			} else {
				present[name] = true
				continue
			}
		case readErr != nil:
			// A damaged manifest is user data. Keep it untouched and retain any
			// existing index row so a later repair does not lose the instance ID.
			present[name] = true
			continue
		}
		if manifest.ID != name {
			present[name] = true
			continue
		}
		if readErr != nil || manifest.needsRewrite {
			if writeErr := writeInstanceManifest(directory, manifest); writeErr != nil {
				present[name] = true
				continue
			}
			manifest.needsRewrite = false
		}
		item := manifest.toInstance(activeGameRel(name))
		if item.CreatedAtMS <= 0 {
			if info, statErr := os.Stat(directory); statErr == nil {
				item.CreatedAtMS = info.ModTime().UnixMilli()
			}
		}
		if err := s.upsertIndexLocked(item); err != nil {
			return err
		}
		present[name] = true
	}
	return s.removeMissingIndexesLocked(present)
}

func (s *InstanceService) removeMissingIndexesLocked(present map[string]bool) error {
	rows, err := s.db.Query(`SELECT id FROM instances`)
	if err != nil {
		return errors.New("读取实例索引失败")
	}
	missing := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return errors.New("读取实例索引失败")
		}
		if !present[id] {
			missing = append(missing, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return errors.New("读取实例索引失败")
	}
	rows.Close()
	for _, id := range missing {
		if err := s.removeIndexLocked(id); err != nil {
			return err
		}
	}
	return s.ensureSelectionLocked()
}

func normalizedPartition(value string) string {
	partition := strings.TrimSpace(value)
	if partition == "" || partition == legacyDefaultPartition {
		return defaultInstanceGroup
	}
	return partition
}
