package instance

import (
	"context"
	"errors"
	"strings"

	"example.com/ninevoxel/launcher/internal/core"
	"example.com/ninevoxel/launcher/internal/loader"
)

// 本文件实现「已完整安装的版本不再重复安装」。
//
// 判据是核心安装清单：它只在该版本**全部步骤成功后**才写出，因此能区分
// 「完整」与「装到一半」——后者在 Forge 的 processor 阶段很常见
// （版本 JSON 已写好，但 client jar 尚未生成）。

// coreInstallLock 返回某安装身份对应的进程内互斥锁。
//
// 核心版本由所有实例共用：同一身份的两个安装请求若并发执行，会同时往
// versions/<id>/ 写入并互相破坏。锁以**安装身份**为键而不是版本目录名：
// Forge / NeoForge 的安装器可能生成与推导值不同的目录名。
//
// 锁表按需惰性创建：服务尚未 openLocked 时也必须有效。若在这里返回空操作，
// 并发安装会静默失去保护——这类问题只在多实例同时安装时暴露，很难复现。
func (s *InstanceService) coreInstallLock(identity string) func() {
	s.mu.Lock()
	if s.installLocks == nil {
		s.installLocks = newInstallLockTable()
	}
	table := s.installLocks
	s.mu.Unlock()
	return table.Lock(identity)
}

// installedVersionID 返回某个安装身份当前已完整安装的版本 ID。
//
// 命中条件（全部满足才复用）：
//   - 清单存在且结构合法；
//   - 配方指纹一致（安装器 / profile / 平台未变）；
//   - 快速校验通过（文件都在且大小一致）。
//
// 任一不满足都返回未命中，由调用方重新安装。慢速校验（逐文件哈希）不放在
// 这里：它在每次安装入口做会显著拖慢启动，改由修复流程按需触发。
func (s *InstanceService) installedVersionID(identity loader.Kind, mcVersion, loaderVersion string) (string, bool) {
	installIdentity, err := core.NewIdentity(core.Kind(identity), mcVersion, loaderVersion)
	if err != nil {
		return "", false
	}
	expected, versionErr := loader.VersionID(identity, mcVersion, loaderVersion)
	if versionErr != nil {
		return "", false
	}

	manifest, found, readErr := core.ReadManifest(s.root, expected)
	if readErr != nil || !found {
		return "", false
	}
	if manifest.Identity != installIdentity.String() {
		return "", false
	}
	if manifest.RecipeFingerprint != s.recipeFingerprint(identity, mcVersion, loaderVersion) {
		return "", false
	}
	report, verifyErr := core.VerifyManifest(s.root, manifest, core.VerifyFast)
	if verifyErr != nil || !report.Complete() {
		return "", false
	}
	return expected, true
}

// recipeFingerprint 计算当前安装配方的指纹。
//
// 参与指纹的字段只包含**会影响产物内容**的因素：
//   - 安装身份（加载器 + MC 版本 + 加载器版本）；
//   - 平台（natives 与部分加载器产物与平台强相关）；
//   - 配方版本（安装流程语义变化时递增，见 core 包）。
//
// 刻意不把安装器文件哈希算进来：它需要先下载才能得到，而本函数在安装前
// 就要用于判断；同一加载器版本的安装器内容由官方保证稳定。
func (s *InstanceService) recipeFingerprint(kind loader.Kind, mcVersion, loaderVersion string) string {
	installIdentity, err := core.NewIdentity(core.Kind(kind), mcVersion, loaderVersion)
	if err != nil {
		return ""
	}
	return core.Recipe{
		Identity: installIdentity,
		Platform: core.CurrentPlatform(),
		Version:  core.RecipeVersion,
	}.Fingerprint()
}

// writeCoreManifest 在安装成功后写出核心安装清单。
//
// 调用方必须传入本次安装**确实落盘**的产物；任一文件缺失都会让整次安装
// 判定为失败，而不是写出一份不完整的清单。
func (s *InstanceService) writeCoreManifest(kind loader.Kind, mcVersion, loaderVersion, versionID string, artifacts installArtifacts) error {
	identity, err := core.NewIdentity(core.Kind(kind), mcVersion, loaderVersion)
	if err != nil {
		return errors.New("安装身份无效，无法记录安装清单")
	}

	versionJSON, jsonErr := versionChainFiles(s.root, versionID)
	if jsonErr != nil {
		// 版本 JSON 是启动的必需品，缺失说明安装不完整。
		return jsonErr
	}

	manifest, buildErr := buildCoreManifest(s.root, manifestInput{
		VersionID:         versionID,
		Identity:          identity.String(),
		LoaderKind:        string(kind),
		LoaderVersion:     loaderVersion,
		MCVersion:         mcVersion,
		RecipeFingerprint: s.recipeFingerprint(kind, mcVersion, loaderVersion),
		Downloaded:        artifacts.Downloaded,
		VersionJSON:       versionJSON,
		NativesDir:        artifacts.NativesDir,
		AssetIndex:        artifacts.AssetIndex,
		AssetObjects:      artifacts.AssetObjects,
		ProcessorOutputs:  processorOutputs(s.root, versionID, artifacts.Downloaded),
	})
	if buildErr != nil {
		return buildErr
	}
	return core.WriteManifest(s.root, manifest)
}

// installWithManifest 是安装的统一入口：先查清单、加锁、执行、再写清单。
//
// 参数 run 执行实际的安装步骤并返回落盘产物。返回值语义与 run 一致，
// 额外在成功时写出清单。
//
// 顺序不可调整：
//  1. 快速检查（不加锁）——绝大多数重复安装在这里就返回；
//  2. 取安装身份锁后再检查一次——排队期间可能已被另一个请求完成；
//  3. 执行安装；
//  4. 写清单（唯一的「提交点」）。
func (s *InstanceService) installWithManifest(ctx context.Context, kind loader.Kind, mcVersion, loaderVersion, versionID string, run func() (installArtifacts, error)) error {
	if err := ctx.Err(); err != nil {
		return errors.New("安装已中断，请重试")
	}
	identity, identityErr := core.NewIdentity(core.Kind(kind), mcVersion, loaderVersion)
	if identityErr != nil {
		return identityErr
	}

	unlock := s.coreInstallLock(identity.String())
	defer unlock()

	// 二次检查：排队期间可能已被另一个请求完成安装。没有这一步，
	// 并发安装同一身份的两个实例会各跑一次安装器。
	if installed, ok := s.installedVersionID(kind, mcVersion, loaderVersion); ok {
		versionID = installed
		return nil
	}

	artifacts, runErr := run()
	if runErr != nil {
		return runErr
	}
	if versionID == "" {
		return errors.New("安装未产生可用的版本标识")
	}
	return s.writeCoreManifest(kind, mcVersion, loaderVersion, versionID, artifacts)
}

// processorOutputs 找出安装器额外生成、且不在下载计划中的版本级产物。
//
// Forge / NeoForge 的安装器会在 `versions/<id>/` 下写出 `-client.jar` 等文件，
// 这些无法从下载计划推导，只能通过对比目录内容识别。
//
// 只收集 `versions/<id>/` 顶层的 jar：natives 目录已在清单的 native 组中，
// 清单文件本身也不应算作产物。
func processorOutputs(root, versionID string, downloaded []string) []string {
	known := make(map[string]struct{}, len(downloaded))
	for _, rel := range downloaded {
		known[rel] = struct{}{}
	}
	relative := "versions/" + versionID
	entries, err := readCoreDir(root, relative)
	if err != nil {
		return nil
	}
	result := make([]string, 0, 4)
	for _, name := range entries {
		if !strings.HasSuffix(strings.ToLower(name), ".jar") {
			continue
		}
		path := relative + "/" + name
		if _, exists := known[path]; exists {
			continue
		}
		result = append(result, path)
	}
	return result
}
