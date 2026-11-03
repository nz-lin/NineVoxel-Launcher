package instance

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/download"
	"example.com/ninevoxel/launcher/internal/loader"
	"example.com/ninevoxel/launcher/internal/version"
)

func (s *InstanceService) install(ctx context.Context, item Instance) error {
	kind, err := loader.ParseKind(item.Loader)
	if err != nil {
		return err
	}
	switch kind {
	case loader.KindVanilla:
		if err := s.installPreparedVersion(ctx, item, item.MCVersion, 1); err != nil {
			return err
		}
		return s.setResolvedVersionID(item.ID, item.MCVersion)
	case loader.KindFabric, loader.KindQuilt:
		return s.installProfileLoader(ctx, item, kind)
	case loader.KindForge, loader.KindNeoForge:
		return s.installInstallerLoader(ctx, item, kind)
	default:
		return errors.New("暂不支持该游戏核心")
	}
}

func (s *InstanceService) installPreparedVersion(ctx context.Context, item Instance, versionID string, maxProgress float64) error {
	prepared, err := version.PrepareInstallation(versionID)
	if err != nil {
		return fmt.Errorf("解析版本安装信息失败：%w", err)
	}
	return s.installPreparedRange(ctx, item, prepared, s.runPlan, maxProgress)
}

// installPrepared assembles only verified downloads into the isolated game layout.
func (s *InstanceService) installPrepared(ctx context.Context, item Instance, prepared version.Installation, runPlan func(context.Context, string, download.TransferPlan, float64, float64) error) error {
	return s.installPreparedRange(ctx, item, prepared, runPlan, 1)
}

func (s *InstanceService) installPreparedRange(ctx context.Context, item Instance, prepared version.Installation, runPlan func(context.Context, string, download.TransferPlan, float64, float64) error, maxProgress float64) error {
	if maxProgress <= 0 || maxProgress > 1 {
		maxProgress = 1
	}
	if err := runPlan(ctx, item.ID, prepared.Plan, 0, 0.35*maxProgress); err != nil {
		return err
	}
	index, appErr := download.LoadAssetIndex(s.root, prepared.AssetIndexID)
	if appErr != nil {
		return appErr
	}
	if index.MapToResources {
		return errors.New("此版本要求旧式 resources 布局，当前尚不支持")
	}
	expansion, appErr := download.ExpandAssetObjects(s.root, index, download.AssetExpandOptions{VerifyExisting: true})
	if appErr != nil {
		return appErr
	}
	if len(expansion.Items) > 0 {
		assets := download.TransferPlan{VersionID: item.MCVersion, PlanHash: prepared.Plan.PlanHash + "-assets", Items: expansion.Items}
		if err := runPlan(ctx, item.ID, assets, 0.35*maxProgress, 0.5*maxProgress); err != nil {
			return err
		}
	}
	game, err := s.gameDir(item)
	if err != nil {
		return err
	}
	for _, file := range prepared.Files {
		if err := ctx.Err(); err != nil {
			return errors.New("安装已中断，请重试")
		}
		source, err := managedPath(s.root, file.Source)
		if err != nil {
			return err
		}
		target, err := managedPath(game, file.Target)
		if err != nil {
			return err
		}
		if file.SHA1 != "" {
			if _, appErr := download.VerifyFile(source, download.VerifyExpectation{ExpectedSize: file.Size, ExpectedSHA1: file.SHA1}); appErr != nil {
				return appErr
			}
		}
		if file.Extract {
			err = extractNatives(ctx, source, target, file.Exclude)
		} else {
			err = copyFile(ctx, source, target)
		}
		if err != nil {
			return err
		}
	}
	// Objects in the content-addressed cache must also exist at Minecraft's layout.
	for i, object := range index.Objects {
		if err := ctx.Err(); err != nil {
			return errors.New("安装已中断，请重试")
		}
		relative, appErr := artifact.ObjectRelPath(artifact.HashAlgorithmSHA1, object.Hash)
		if appErr != nil {
			return appErr
		}
		source, err := managedPath(s.root, relative)
		if err != nil {
			return err
		}
		target, err := managedPath(game, "assets/objects/"+object.Hash[:2]+"/"+object.Hash)
		if err != nil {
			return err
		}
		if err := copyFile(ctx, source, target); err != nil {
			return err
		}
		if index.Virtual {
			target, err := managedPath(game, "assets/"+object.LogicalName)
			if err != nil {
				return err
			}
			if err := copyFile(ctx, source, target); err != nil {
				return err
			}
		}
		if i%100 == 0 {
			if err := s.progress(item.ID, "", maxProgress*(0.85+0.14*float64(i+1)/float64(len(index.Objects))), prepared.JavaMajor); err != nil {
				return err
			}
		}
	}
	return s.progress(item.ID, "", 0.99*maxProgress, prepared.JavaMajor)
}

func (s *InstanceService) runPlan(ctx context.Context, id string, plan download.TransferPlan, base, span float64) error {
	if err := ctx.Err(); err != nil {
		return errors.New("安装已中断，请重试")
	}
	// 同一次实例安装会拆成多个计划：用实例 ID 作为操作组，界面据此合并下载条目。
	plan.OperationID = id
	accepted, err := download.StartTransferPlan(ctx, plan)
	if err != nil {
		return err
	}
	if err := s.progress(id, accepted.TaskID, base, 0); err != nil {
		return err
	}
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		task, err := download.GetTransferTask(accepted.TaskID)
		if err != nil {
			return err
		}
		ratio := float64(0)
		if task.Progress != nil {
			ratio = *task.Progress
		}
		if err := s.progress(id, accepted.TaskID, base+span*ratio, 0); err != nil {
			return err
		}
		switch task.State {
		case "completed":
			return nil
		case "failed":
			if task.Error != nil {
				return fmt.Errorf("下载失败：%s（%s）", task.Error.Message, task.Error.Code)
			}
			return errors.New("下载失败，请重试安装")
		case "cancelled":
			return errors.New("下载已取消，请重试安装")
		}
		select {
		case <-ctx.Done():
			return errors.New("安装已中断，请重试")
		case <-ticker.C:
		}
	}
}

func (s *InstanceService) progress(id, task string, value float64, javaMajor int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.getLocked(id)
	if err != nil {
		return err
	}
	item.InstallationProgress = value
	if task != "" {
		item.InstallTaskID = task
	}
	if javaMajor > 0 {
		item.JavaVersion = fmt.Sprintf("Java %d", javaMajor)
	}
	return s.saveLocked(item)
}

// copyFile writes through a temporary file, leaving existing user files intact on error.
func copyFile(ctx context.Context, source, target string) error {
	if err := ctx.Err(); err != nil {
		return errors.New("安装已中断，请重试")
	}
	input, err := os.Open(source)
	if err != nil {
		return errors.New("无法读取已下载的游戏文件")
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return errors.New("创建游戏目录失败")
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".install-*")
	if err != nil {
		return errors.New("创建游戏文件失败")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	_, writeErr := io.Copy(temp, &contextReader{ctx: ctx, reader: input})
	closeErr := temp.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("写入游戏文件失败")
	}
	if err := os.Chmod(tempPath, 0644); err != nil {
		return errors.New("设置游戏文件权限失败")
	}
	if err := os.Rename(tempPath, target); err != nil {
		return errors.New("提交游戏文件失败")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// extractNatives rejects traversal, links and oversized archives before extracting.
func extractNatives(ctx context.Context, source, target string, exclude []string) error {
	archive, err := zip.OpenReader(source)
	if err != nil {
		return errors.New("本地库压缩包无效")
	}
	defer archive.Close()
	if len(archive.File) > 10000 {
		return errors.New("本地库压缩包文件过多")
	}
	var total uint64
	for _, file := range archive.File {
		if file.Mode()&os.ModeSymlink != 0 {
			return errors.New("本地库压缩包包含符号链接")
		}
		total += file.UncompressedSize64
		if file.UncompressedSize64 > 256<<20 || total > 1<<30 {
			return errors.New("本地库解压大小超出限制")
		}
		if _, err := artifact.ValidateRelativePath(strings.TrimSuffix(file.Name, "/")); err != nil {
			return errors.New("本地库压缩包包含非法路径")
		}
	}
	for _, file := range archive.File {
		if err := ctx.Err(); err != nil {
			return errors.New("安装已中断，请重试")
		}
		skip := strings.HasPrefix(file.Name, "META-INF/")
		for _, prefix := range exclude {
			if strings.HasPrefix(file.Name, prefix) {
				skip = true
			}
		}
		if skip || file.FileInfo().IsDir() {
			continue
		}
		destination, err := managedPath(target, file.Name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return errors.New("创建本地库目录失败")
		}
		reader, err := file.Open()
		if err != nil {
			return errors.New("读取本地库失败")
		}
		err = writeNative(ctx, reader, destination, int64(file.UncompressedSize64))
		reader.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeNative(ctx context.Context, reader io.Reader, destination string, size int64) error {
	temp, err := os.CreateTemp(filepath.Dir(destination), ".native-*")
	if err != nil {
		return errors.New("创建本地库失败")
	}
	name := temp.Name()
	defer os.Remove(name)
	count, writeErr := io.Copy(temp, &contextReader{ctx: ctx, reader: io.LimitReader(reader, size+1)})
	closeErr := temp.Close()
	if writeErr != nil || closeErr != nil || count != size {
		return errors.New("本地库文件校验失败")
	}
	if err := os.Chmod(name, 0755); err != nil {
		return errors.New("设置本地库权限失败")
	}
	if err := os.Rename(name, destination); err != nil {
		return errors.New("提交本地库失败")
	}
	return nil
}
