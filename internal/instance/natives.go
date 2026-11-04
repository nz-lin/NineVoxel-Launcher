package instance

import (
	"os"
	"strings"

	"example.com/ninevoxel/launcher/internal/launch"
)

// 本文件集中定义实例级派生产物的位置。
//
// natives 与 patches 都属于实例而非共享核心：
//   - natives 是安装期解压产物，随实例删除一并回收，跨平台不可复用；
//   - patches 是实例对版本声明的覆盖，只有该实例启动时才生效。
//
// 两者都放在 `instances/<id>/` 下，而不是共享核心，避免出现「一个实例的
// 派生数据被别人复用」这种隐式耦合。

// nativesRel 返回实例 natives 目录相对数据根的路径。
//
// 放在 `.minecraft/` 内：它是游戏工作目录的一部分，启动参数里的
// `${natives_directory}` 直接指向这里，不再依赖核心目录。
func nativesRel(instanceID string) string {
	return activeInstanceRel(instanceID) + "/.minecraft/natives"
}

// patchesRel 返回实例补丁目录相对数据根的路径。
func patchesRel(instanceID string) string {
	return activeInstanceRel(instanceID) + "/patches"
}

// nativesDir 返回实例 natives 目录的绝对路径。
func (s *InstanceService) nativesDir(instanceID string) (string, error) {
	return managedPath(s.root, nativesRel(instanceID))
}

// patchDir 返回实例补丁目录的绝对路径。
func (s *InstanceService) patchDir(instanceID string) (string, error) {
	return managedPath(s.root, patchesRel(instanceID))
}

// nativesReady 判断实例的 natives 目录是否已就绪（存在且非空）。
//
// 只做目录列举：natives 不在核心清单里，单个文件是否完整由重新解压保证，
// 不做逐文件哈希。
func (s *InstanceService) nativesReady(instanceID string) bool {
	absolute, err := s.nativesDir(instanceID)
	if err != nil {
		return false
	}
	entries, readErr := os.ReadDir(absolute)
	if readErr != nil {
		return false
	}
	return len(entries) > 0
}

// versionDeclaresNatives 判断某个版本是否声明了 natives。
//
// 读取版本 JSON（含继承链）而不是核心清单：natives 自 schema v2 起不在清单里，
// 清单无法回答这个问题。元数据不可读时返回 false——修复会退化为只看清单，
// 不会因此误报「需要重装」。
func (s *InstanceService) versionDeclaresNatives(versionID string) bool {
	if strings.TrimSpace(versionID) == "" || s.root == "" {
		return false
	}
	traits, err := launch.AssessVersionTraits(s.root, versionID)
	if err != nil {
		return false
	}
	return traits.NativesConvention != launch.CompatNativesNone
}
