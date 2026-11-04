package instance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
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
//   - 快速校验通过（文件都在且大小一致）；
//   - **版本 JSON 声明的库在磁盘上齐全**。
//
// 最后一条是清单自洽性之外的必要补充：清单可能生成得不完整（历史版本就出现过
// 漏记整个 install_profile.libraries 的情况），此时清单校验会「通过」——因为它
// 只检查自己列出的文件。而游戏启动时会因缺库直接失败，表现为
// `installation is corrupted` 这类无法定位的错误。
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
	if !versionLibrariesPresent(s.root, expected) {
		return "", false
	}
	return expected, true
}

// versionLibrariesPresent 检查版本 JSON 声明的库是否都在磁盘上。
//
// 只看这个版本自己的 JSON（不含继承链）：原版库由原版版本的清单负责，
// 加载器版本的清单只应覆盖它自己声明的那部分。
//
// 无法读取版本 JSON 时返回 false：读不出内容就无法确认完整性，
// 按「未安装」处理比盲目复用更安全。
func versionLibrariesPresent(root, versionID string) bool {
	versionDir, found, dirErr := artifact.ResolveVersionDir(root, versionID)
	if dirErr != nil || !found {
		return false
	}
	absolute, resolveErr := artifact.ResolveInsideRoot(root, versionDir+"/"+versionID+".json")
	if resolveErr != nil {
		return false
	}
	body, readErr := os.ReadFile(absolute)
	if readErr != nil || len(body) == 0 {
		return false
	}

	var document struct {
		Libraries []struct {
			Downloads *struct {
				Artifact *struct {
					Path string `json:"path"`
				} `json:"artifact"`
			} `json:"downloads"`
		} `json:"libraries"`
	}
	if jsonErr := json.Unmarshal(body, &document); jsonErr != nil {
		return false
	}

	for _, library := range document.Libraries {
		if library.Downloads == nil || library.Downloads.Artifact == nil {
			continue
		}
		relativePath, validErr := artifact.ValidateRelativePath(library.Downloads.Artifact.Path)
		if validErr != nil || relativePath != library.Downloads.Artifact.Path {
			return false
		}
		target, targetErr := artifact.ResolveInsideRoot(root, artifact.DirLibraries+"/"+relativePath)
		if targetErr != nil {
			return false
		}
		info, statErr := os.Stat(target)
		if statErr != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
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
	versionDir, dirErr := coreVersionDirRel(kind, versionID)
	if dirErr != nil {
		return dirErr
	}

	versionJSON, jsonErr := versionChainFiles(s.root, versionID)
	if jsonErr != nil {
		// 版本 JSON 是启动的必需品，缺失说明安装不完整。
		return jsonErr
	}

	manifest, buildErr := buildCoreManifest(s.root, manifestInput{
		VersionID:         versionID,
		VersionDir:        versionDir,
		Identity:          identity.String(),
		LoaderKind:        string(kind),
		LoaderVersion:     loaderVersion,
		MCVersion:         mcVersion,
		RecipeFingerprint: s.recipeFingerprint(kind, mcVersion, loaderVersion),
		Downloaded:        artifacts.Downloaded,
		VersionJSON:       versionJSON,
		AssetIndex:        artifacts.AssetIndex,
		AssetObjects:      artifacts.AssetObjects,
		ProcessorOutputs:  mergeProcessorOutputs(artifacts.ProcessorOutputs, processorOutputs(s.root, versionID, artifacts.Downloaded)),
	})
	if buildErr != nil {
		return buildErr
	}
	return core.WriteManifest(s.root, manifest)
}

// coreVersionDirRel 返回某个版本应落在哪个核心目录。
//
// 原版保持官方 `.minecraft` 布局（`versions/`）；加载器版本由启动器生成，
// 统一放 `loaders/`。这是写入时的唯一判定点，读取一律走
// artifact.ResolveVersionDir 探测。
func coreVersionDirRel(kind loader.Kind, versionID string) (string, error) {
	var relative string
	var appErr *artifact.Error
	if kind == loader.KindVanilla {
		relative, appErr = artifact.VersionDirRel(versionID)
	} else {
		relative, appErr = artifact.LoaderVersionDirRel(versionID)
	}
	if appErr != nil {
		return "", errors.New("版本目录无效")
	}
	return relative, nil
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

// mergeProcessorOutputs 合并显式记录的与扫描得到的 processor 产物。
//
// 两者都要保留：现代 Forge 把产物放在 `libraries/`（安装流程显式记录），
// 而部分版本的安装器会往 `versions/<id>/` 写 jar（扫描得到）。
// 按路径去重，避免同一文件在清单里出现两次。
func mergeProcessorOutputs(explicit, scanned []string) []string {
	if len(explicit) == 0 {
		return scanned
	}
	seen := make(map[string]struct{}, len(explicit)+len(scanned))
	result := make([]string, 0, len(explicit)+len(scanned))
	for _, group := range [][]string{explicit, scanned} {
		for _, path := range group {
			if path == "" {
				continue
			}
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			result = append(result, path)
		}
	}
	return result
}

// processorOutputs 找出安装器额外生成、且不在下载计划中的版本级产物。
//
// Forge / NeoForge 的安装器会在版本目录下写出 `-client.jar` 等文件，
// 这些无法从下载计划推导，只能通过对比目录内容识别。
//
// 只收集版本目录顶层的 jar：natives 已不在核心清单里（属于实例目录），
// 清单文件本身也不应算作产物。
func processorOutputs(root, versionID string, downloaded []string) []string {
	known := make(map[string]struct{}, len(downloaded))
	for _, rel := range downloaded {
		known[rel] = struct{}{}
	}
	relative, found, dirErr := artifact.ResolveVersionDir(root, versionID)
	if dirErr != nil || !found {
		return nil
	}
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
