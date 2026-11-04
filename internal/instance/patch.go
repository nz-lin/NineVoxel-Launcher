package instance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/launch"
)

// 本文件提供实例级「版本声明覆盖」的后端接口。
//
// 覆盖只允许作用于实例当前正在使用的版本：允许写别的版本会出现「补丁指向
// 一个实例根本不启动的版本」这种悬空配置，用户改了却看不到任何效果。
// 将来若要支持「切换版本时保留覆盖」，再按需放开。

// VersionPatchDocument 是实例补丁的读取结果。
type VersionPatchDocument struct {
	InstanceID string `json:"instanceId"`
	VersionID  string `json:"versionId"`
	Exists     bool   `json:"exists"`
	Content    string `json:"content"`
}

// SaveVersionPatchRequest 是保存补丁的入参。
type SaveVersionPatchRequest struct {
	InstanceID string `json:"instanceId"`
	Content    string `json:"content"`
}

// ReadVersionPatch 读取实例当前的版本声明覆盖。
func (s *InstanceService) ReadVersionPatch(req IDRequest) (VersionPatchDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return VersionPatchDocument{}, err
	}
	item, err := s.getLocked(req.InstanceID)
	if err != nil {
		return VersionPatchDocument{}, err
	}
	versionID := launchVersionIDOf(item)
	document := VersionPatchDocument{InstanceID: item.ID, VersionID: versionID}
	if versionID == "" {
		return document, nil
	}
	patchDir, dirErr := s.patchDir(item.ID)
	if dirErr != nil {
		return VersionPatchDocument{}, dirErr
	}
	content, exists, readErr := launch.ReadVersionPatchFile(patchDir, versionID)
	if readErr != nil {
		return VersionPatchDocument{}, readErr
	}
	document.Exists = exists
	document.Content = string(content)
	return document, nil
}

// SaveVersionPatch 保存实例当前的版本声明覆盖。
//
// 写入前必须通过 launch 的合并校验：语法正确但语义非法的补丁会让实例
// 直接无法启动，不能等到下次启动才发现。
func (s *InstanceService) SaveVersionPatch(req SaveVersionPatchRequest) (VersionPatchDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return VersionPatchDocument{}, err
	}
	item, err := s.getLocked(req.InstanceID)
	if err != nil {
		return VersionPatchDocument{}, err
	}
	if item.InstallationState == "installing" {
		return VersionPatchDocument{}, errors.New("实例正在安装，请等待完成后再修改版本覆盖")
	}
	versionID := launchVersionIDOf(item)
	if versionID == "" {
		return VersionPatchDocument{}, errors.New("实例还没有可覆盖的版本，请先完成安装")
	}
	document := VersionPatchDocument{InstanceID: item.ID, VersionID: versionID}

	content := []byte(req.Content)
	if validateErr := launch.ValidateVersionPatchDocument(s.root, versionID, content); validateErr != nil {
		return document, validateErr
	}

	patchDir, dirErr := s.patchDir(item.ID)
	if dirErr != nil {
		return document, dirErr
	}
	if mkErr := os.MkdirAll(patchDir, 0755); mkErr != nil {
		return document, errors.New("无法创建实例补丁目录")
	}
	if writeErr := writePatchFileAtomic(patchDir, versionID, content); writeErr != nil {
		return document, writeErr
	}
	document.Exists = true
	document.Content = string(content)
	return document, nil
}

// DeleteVersionPatch 删除实例的版本声明覆盖，恢复核心声明。
func (s *InstanceService) DeleteVersionPatch(req IDRequest) (VersionPatchDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return VersionPatchDocument{}, err
	}
	item, err := s.getLocked(req.InstanceID)
	if err != nil {
		return VersionPatchDocument{}, err
	}
	versionID := launchVersionIDOf(item)
	document := VersionPatchDocument{InstanceID: item.ID, VersionID: versionID}
	if versionID == "" {
		return document, nil
	}
	if !safeVersionDirectoryName(versionID) {
		return VersionPatchDocument{}, errors.New("实例的版本标识无效，无法使用版本覆盖")
	}
	patchDir, dirErr := s.patchDir(item.ID)
	if dirErr != nil {
		return VersionPatchDocument{}, dirErr
	}
	target := filepath.Join(patchDir, versionID+".json")
	if removeErr := os.Remove(target); removeErr != nil && !os.IsNotExist(removeErr) {
		return VersionPatchDocument{}, errors.New("删除版本覆盖失败")
	}
	return document, nil
}

// launchVersionIDOf 返回实例实际启动的版本标识。
//
// 与 ResolveLaunchConfig 保持同一优先级：VersionID 优先，缺失时回退 MCVersion。
func launchVersionIDOf(item Instance) string {
	if versionID := strings.TrimSpace(item.VersionID); versionID != "" {
		return versionID
	}
	return strings.TrimSpace(item.MCVersion)
}

// writePatchFileAtomic 以「临时文件 + Rename」写入补丁。
//
// 不在原地覆盖：写入过程中崩溃会留下半个 JSON，下次启动直接失败。临时文件
// 前缀以点开头，不会被当作版本标识。
func writePatchFileAtomic(patchDir, versionID string, content []byte) error {
	temp, createErr := os.CreateTemp(patchDir, ".patch-*.json")
	if createErr != nil {
		return errors.New("无法创建版本覆盖文件")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, writeErr := temp.Write(content); writeErr != nil {
		temp.Close()
		return errors.New("写入版本覆盖失败")
	}
	if syncErr := temp.Sync(); syncErr != nil {
		temp.Close()
		return errors.New("同步版本覆盖失败")
	}
	if closeErr := temp.Close(); closeErr != nil {
		return errors.New("关闭版本覆盖文件失败")
	}
	if chmodErr := os.Chmod(tempPath, 0644); chmodErr != nil {
		return errors.New("设置版本覆盖权限失败")
	}
	if renameErr := os.Rename(tempPath, filepath.Join(patchDir, versionID+".json")); renameErr != nil {
		return errors.New("提交版本覆盖失败")
	}
	return nil
}
