package launch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// 本文件实现「实例级版本声明覆盖」的校验。
//
// 语义与 Prism 的 `patches/<uid>.json` 对齐：补丁是**声明覆盖**，不是文件
// 迁移。它只改变 mainClass / libraries / arguments / inheritsFrom 等声明，
// jar、libraries、assets 仍然从共享核心读取。
//
// 校验必须发生在写入之前：一个语法正确但语义非法的补丁（自引用、缺 mainClass）
// 会让实例从此无法启动，而用户很难把故障和自己的修改联系起来。

const (
	// MaxVersionPatchBytes 是单个补丁文件的上限。
	//
	// 取 1 MiB：补丁是「覆盖几项声明」的小文件，真实用途远小于此；
	// 上限只是拦住误粘贴整个版本 JSON 的情况，仍留足整合包的空间。
	MaxVersionPatchBytes = 1 << 20
)

// VersionPatchError 描述补丁校验失败。
type VersionPatchError struct {
	Message string
}

// Error 实现 error。
func (e VersionPatchError) Error() string { return e.Message }

// ValidateVersionPatchDocument 校验一份尚未落盘的补丁文档。
//
// 校验内容：
//   - 必须是 JSON 对象；
//   - `id` 若存在必须与目标版本一致（避免把补丁写到错误的版本上）；
//   - 与核心继承链合并后必须仍能产出可启动的声明（有 mainClass、无环、深度受限）。
//
// 参数 root 为核心根；versionID 为实例当前启动版本；content 为补丁原文。
func ValidateVersionPatchDocument(root, versionID string, content []byte) error {
	if strings.TrimSpace(versionID) == "" {
		return VersionPatchError{Message: "实例没有可覆盖的版本。"}
	}
	if !isVersionIDValid(versionID) {
		return VersionPatchError{Message: "实例的版本标识无效，无法写入覆盖。"}
	}
	if len(content) == 0 {
		return VersionPatchError{Message: "补丁内容不能为空。"}
	}
	if len(content) > MaxVersionPatchBytes {
		return VersionPatchError{Message: "补丁内容过大，请只覆盖需要修改的字段。"}
	}

	// 先确认是 JSON 对象：数组或标量无法作为版本声明。
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(content, &probe); err != nil {
		return VersionPatchError{Message: "补丁不是合法的 JSON 对象。"}
	}

	var parsed versionJSON
	if err := json.Unmarshal(content, &parsed); err != nil {
		return VersionPatchError{Message: "补丁格式无法解析。"}
	}
	if id := strings.TrimSpace(parsed.ID); id != "" && id != versionID {
		return VersionPatchError{Message: "补丁的 id 与实例当前版本不一致。"}
	}

	source := versionSource{root: root, overrideID: versionID, override: &parsed}
	outcome := loadMergedVersionFrom(source, versionID)
	if outcome.merged == nil {
		reason := outcome.reason
		if reason == "" {
			reason = "补丁合并后无法得到可启动的版本声明。"
		}
		return VersionPatchError{Message: reason}
	}
	return nil
}

// ReadVersionPatchFile 读取实例补丁文件。
//
// 返回 (内容, 是否存在, 错误)。文件不存在不是错误。
func ReadVersionPatchFile(patchDir, versionID string) ([]byte, bool, error) {
	if strings.TrimSpace(patchDir) == "" || strings.TrimSpace(versionID) == "" {
		return nil, false, nil
	}
	// 版本标识直接参与文件名拼接：非法标识按「没有补丁」处理，
	// 避免 `../x.json` 之类的值把读取带出补丁目录。
	if !isVersionIDValid(versionID) {
		return nil, false, nil
	}
	path := filepath.Join(patchDir, versionID+".json")
	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, false, nil
		}
		return nil, false, errors.New("无法读取实例补丁")
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("实例补丁不是普通文件")
	}
	if info.Size() > MaxVersionPatchBytes {
		return nil, false, errors.New("实例补丁文件过大")
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, false, errors.New("无法读取实例补丁")
	}
	return content, true, nil
}
