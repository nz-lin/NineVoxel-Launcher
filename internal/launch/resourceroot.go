package launch

import (
	"path/filepath"
	"strings"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// 与启动计划构建相关、但不属于版本解析的提示文案。
const (
	// messageLaunchRootsMissing 表示构建启动计划时缺少游戏目录或核心目录。
	//
	// 双根模型不接受空值：任一为空都说明调用方装配有问题，静默回退会让
	// 实例目录与核心目录的边界在个别路径上悄悄消失。
	messageLaunchRootsMissing = "启动计划的游戏目录或核心目录为空，无法构建启动参数。"

	// messageResourceRootUnavailable 表示无法解析共享核心根目录。
	messageResourceRootUnavailable = "无法访问游戏核心目录，请检查数据目录是否可写。"
)

// selfContainedResourceRoot 用于「预览任意外部目录」的场景。
//
// 该入口接受用户手写的游戏目录（例如从别处拷来的自包含 `.minecraft`），
// 此时 libraries / assets / versions 就在该目录下，核心根与被预览目录相同。
//
// 这不是「空值回退」：实例启动走的是 resolveResourceRoot，两者互不影响。
func selfContainedResourceRoot(directory string) string {
	return directory
}

// resolveResourceRoot 返回共享核心根的绝对路径。
//
// 核心根就是数据根：libraries、assets、versions 都在它下面，由所有实例共用。
// 该函数是「核心根从哪来」的**唯一**来源，避免各处重复调用 DataRoot 后得到
// 不一致的结果（例如测试替换了数据根提供者）。
//
// 返回 `*Error`：数据根不可用时给出可读原因，而不是回退到当前目录。
func resolveResourceRoot() (string, *Error) {
	root, appErr := artifact.DataRoot()
	if appErr != nil || strings.TrimSpace(root) == "" {
		return "", newSystemError(ErrorCodeGameDir, messageResourceRootUnavailable)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", newSystemError(ErrorCodeGameDir, messageResourceRootUnavailable)
	}
	return absolute, nil
}
