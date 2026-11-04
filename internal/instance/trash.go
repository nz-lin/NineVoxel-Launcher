package instance

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TrashItem describes an archived instance without exposing its host path to
// the frontend. TrashID is an opaque, validated directory name.
type TrashItem struct {
	TrashID      string `json:"trashId"`
	InstanceID   string `json:"instanceId"`
	Name         string `json:"name"`
	MCVersion    string `json:"mcVersion"`
	Loader       string `json:"loader"`
	Icon         string `json:"icon"`
	DeletedAtMS  int64  `json:"deletedAtMs"`
	SizeBytes    int64  `json:"sizeBytes"`
	Restorable   bool   `json:"restorable"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

type TrashList struct {
	Items []TrashItem `json:"items"`
}

type TrashIDRequest struct {
	TrashID string `json:"trashId"`
}

func (s *InstanceService) ListInstanceTrash() (TrashList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return TrashList{}, err
	}
	return s.listTrashLocked()
}

func (s *InstanceService) RestoreInstanceFromTrash(req TrashIDRequest) (List, error) {
	trashID := strings.TrimSpace(req.TrashID)
	if !validateTrashID(trashID) {
		return List{}, errors.New("回收站条目无效")
	}
	instanceID := instanceIDFromTrashID(trashID)
	if instanceID == "" {
		return List{}, errors.New("回收站条目无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return List{}, err
	}
	source, err := managedPath(s.root, trashInstanceRel(trashID))
	if err != nil {
		return List{}, err
	}
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return List{}, errors.New("回收站条目不存在")
	}
	if err != nil {
		return List{}, errors.New("无法访问回收站条目")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return List{}, errors.New("回收站条目无效")
	}
	manifest, err := s.trashManifestLocked(source, instanceID, trashDeletedAt(trashID, source))
	if err != nil {
		return List{}, err
	}
	if manifest.ID != instanceID {
		return List{}, errors.New("回收站条目与实例清单不一致")
	}
	destination, err := managedPath(s.root, activeInstanceRel(instanceID))
	if err != nil {
		return List{}, err
	}
	if _, err := os.Lstat(destination); err == nil {
		return List{}, errors.New("原实例目录已存在，无法还原；请先移动或删除该目录")
	} else if !os.IsNotExist(err) {
		return List{}, errors.New("无法访问原实例目录")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return List{}, errors.New("创建实例目录失败")
	}
	item := manifest.toInstance(activeGameRel(instanceID))
	if err := writeInstanceManifest(source, manifestFromInstance(item, 0)); err != nil {
		return List{}, errors.New("准备还原实例失败")
	}
	if err := os.Rename(source, destination); err != nil {
		_ = writeInstanceManifest(source, manifestFromInstance(item, manifest.TrashedAtMS))
		return List{}, errors.New("还原实例目录失败")
	}
	if err := s.upsertIndexLocked(item); err != nil {
		_ = writeInstanceManifest(destination, manifestFromInstance(item, manifest.TrashedAtMS))
		_ = os.Rename(destination, source)
		return List{}, err
	}
	if err := s.ensureSelectionLocked(); err != nil {
		_ = s.removeIndexLocked(item.ID)
		_ = writeInstanceManifest(destination, manifestFromInstance(item, manifest.TrashedAtMS))
		_ = os.Rename(destination, source)
		return List{}, err
	}
	return s.listLocked()
}

func (s *InstanceService) DeleteInstancePermanently(req TrashIDRequest) (TrashList, error) {
	trashID := strings.TrimSpace(req.TrashID)
	if !validateTrashID(trashID) || instanceIDFromTrashID(trashID) == "" {
		return TrashList{}, errors.New("回收站条目无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return TrashList{}, err
	}
	directory, err := managedPath(s.root, trashInstanceRel(trashID))
	if err != nil {
		return TrashList{}, err
	}
	info, err := os.Lstat(directory)
	if os.IsNotExist(err) {
		return s.listTrashLocked()
	}
	if err != nil {
		return TrashList{}, errors.New("无法访问回收站条目")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return TrashList{}, errors.New("回收站条目无效")
	}
	if err := os.RemoveAll(directory); err != nil {
		return TrashList{}, errors.New("永久删除失败，文件可能正在被占用")
	}
	// 永久删除后回收不再被任何实例（含回收站）引用的核心版本。
	// 失败不阻断删除结果：清理是附加收益，不该让用户看到「删除失败」。
	s.reclaimUnusedCoreSilently()
	return s.listTrashLocked()
}

func (s *InstanceService) EmptyInstanceTrash() (TrashList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return TrashList{}, err
	}
	root, err := managedPath(s.root, trashRootRel)
	if err != nil {
		return TrashList{}, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return TrashList{Items: []TrashItem{}}, nil
	}
	if err != nil {
		return TrashList{}, errors.New("读取回收站失败")
	}
	failed := 0
	skipped := 0
	for _, entry := range entries {
		name := entry.Name()
		if !validateTrashID(name) || instanceIDFromTrashID(name) == "" || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			skipped++
			continue
		}
		directory := filepath.Join(root, name)
		if err := os.RemoveAll(directory); err != nil {
			failed++
		}
	}
	list, listErr := s.listTrashLocked()
	if listErr != nil {
		return TrashList{}, listErr
	}
	if failed > 0 {
		return list, fmt.Errorf("清空回收站失败，%d 个条目的文件可能正在被占用", failed)
	}
	s.reclaimUnusedCoreSilently()
	if skipped > 0 {
		return list, fmt.Errorf("回收站中有 %d 个无法识别的条目，未自动删除", skipped)
	}
	return list, nil
}

// reclaimUnusedCoreSilently 在回收站变更后顺带清理核心。
//
// 刻意吞掉错误：调用方刚完成的是「删除实例」这类用户可见操作，核心清理只是
// 附带的磁盘回收。把清理失败包装成操作失败会误导用户重复删除，而真正的
// 清理入口（设置页）会如实报告原因。
func (s *InstanceService) reclaimUnusedCoreSilently() {
	_, _ = s.CleanupUnusedCore()
}

func (s *InstanceService) listTrashLocked() (TrashList, error) {
	result := TrashList{Items: []TrashItem{}}
	root, err := managedPath(s.root, trashRootRel)
	if err != nil {
		return result, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, errors.New("读取回收站失败")
	}
	for _, entry := range entries {
		name := entry.Name()
		if !validateTrashID(name) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		instanceID := instanceIDFromTrashID(name)
		if instanceID == "" {
			continue
		}
		directory := filepath.Join(root, name)
		item := TrashItem{
			TrashID:     name,
			InstanceID:  instanceID,
			Loader:      "vanilla",
			DeletedAtMS: trashDeletedAt(name, directory),
		}
		manifest, manifestErr := s.trashManifestLocked(directory, instanceID, item.DeletedAtMS)
		if manifestErr != nil {
			item.ErrorMessage = manifestErr.Error()
			item.SizeBytes = directorySize(directory)
			result.Items = append(result.Items, item)
			continue
		}
		if manifest.ID != instanceID {
			item.ErrorMessage = "回收站条目与实例清单不一致"
			item.SizeBytes = directorySize(directory)
			result.Items = append(result.Items, item)
			continue
		}
		item.SizeBytes = directorySize(directory)
		item.Name = manifest.Name
		item.MCVersion = manifest.MCVersion
		item.Loader = manifest.Loader
		item.Icon = manifest.Icon
		if manifest.TrashedAtMS > 0 {
			item.DeletedAtMS = manifest.TrashedAtMS
		}
		item.Restorable = true
		result.Items = append(result.Items, item)
	}
	sort.SliceStable(result.Items, func(left, right int) bool {
		if result.Items[left].DeletedAtMS == result.Items[right].DeletedAtMS {
			return result.Items[left].TrashID > result.Items[right].TrashID
		}
		return result.Items[left].DeletedAtMS > result.Items[right].DeletedAtMS
	})
	return result, nil
}

func (s *InstanceService) trashManifestLocked(directory, instanceID string, deletedAtMS int64) (instanceManifest, error) {
	manifest, err := readInstanceManifest(directory)
	if err == nil {
		return manifest, nil
	}
	if !errors.Is(err, errManifestNotFound) {
		return instanceManifest{}, err
	}
	recovered, err := recoverManifest(directory, instanceID, 0, deletedAtMS)
	if err != nil {
		return instanceManifest{}, errors.New("实例清单缺失，且无法从游戏目录恢复")
	}
	if err := writeInstanceManifest(directory, recovered); err != nil {
		return instanceManifest{}, errors.New("恢复实例清单失败")
	}
	return recovered, nil
}

func directorySize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry == nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}
