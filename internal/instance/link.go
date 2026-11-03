// 本文件实现「把共享缓存中的制品落地到实例目录」的文件写入策略。
//
// 背景：内容寻址缓存（`artifacts/objects/<前两位>/<hash>`）里的对象会被多个实例
// 重复引用。若一律复制，则「只有加载器不同」的实例会各存一份完整资源——实测 5 个
// 实例的 `assets` 各 463MB，其中 4 份是完全重复的。
//
// 因此这里优先建立**硬链接**：同一份内容只占一份磁盘空间，而实例目录中仍然看得见
// 该文件，把实例目录拷到别处时也会正常复制成实体文件（自包含性不变）。
//
// 硬链接失败（跨卷、文件系统不支持、权限不足）时回退为复制：空间优化不能成为
// 安装失败的原因。
package instance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// linkTempPrefix 是建立硬链接时使用的临时文件名前缀。
const linkTempPrefix = ".link-*"

// materializeFile 把 source 落地到 target，优先硬链接、失败回退复制。
//
// 参数 source 为共享缓存中的绝对路径；target 为实例目录中的绝对路径。
func materializeFile(ctx context.Context, source, target string) error {
	if err := ctx.Err(); err != nil {
		return errors.New("安装已中断，请重试")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return errors.New("创建游戏目录失败")
	}
	// 已指向同一份内容时什么都不做：重复安装不该反复重建链接。
	if same, sameErr := sameFileContent(source, target); sameErr == nil && same {
		return nil
	}
	if linkErr := linkAtomically(source, target); linkErr == nil {
		return nil
	}
	return copyFile(ctx, source, target)
}

// linkAtomically 以「临时名建链 → 改名」的方式替换 target。
//
// 直接 `os.Link(source, target)` 在 target 已存在时失败，且失败前无法保证原子性；
// 先链到同目录的临时名再改名，可以让读者要么看到旧文件、要么看到新链接。
func linkAtomically(source, target string) error {
	temp, err := os.CreateTemp(filepath.Dir(target), linkTempPrefix)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	// CreateTemp 已经落下一个实体文件，必须先删除才能对同名路径建立硬链接。
	_ = temp.Close()
	if removeErr := os.Remove(tempPath); removeErr != nil {
		return removeErr
	}

	if err := os.Link(source, tempPath); err != nil {
		return err
	}
	if err := os.Rename(tempPath, target); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

// sameFileContent 判断两个路径是否已经指向同一份内容（同一个 inode）。
//
// 参数 source / target 为待比较路径；任一侧不存在时返回 false。
func sameFileContent(source, target string) (bool, error) {
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return false, err
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		return false, err
	}
	return os.SameFile(sourceInfo, targetInfo), nil
}
