package instance

import (
	"example.com/ninevoxel/launcher/internal/launch"
)

// 本文件提供「启动前 Java 就绪检查」的后端入口。
//
// 为什么不在新增实例时检查：创建实例只需要版本与配置，游戏资源安装也可以
// 在没有任何 Java 的情况下完成（原版与 Fabric 安装不需要 Java）。真正被阻塞
// 的是启动那一刻，而且只有启动时版本声明（javaVersion.majorVersion）已经
// 就位，才能给出「你需要 Java 21」这种可操作的信息。
//
// 检查逻辑放在 launch 包，与真正的启动共用同一份优先级；本文件只做服务包装。

// JavaReadinessRequest 是启动前检查的入参。
type JavaReadinessRequest struct {
	// InstanceID 是要启动的实例。
	InstanceID string `json:"instanceId"`
}

// CheckLaunchJava 检查实例启动所需的 Java 是否可用。
//
// 返回 Ready=false 时界面应弹出「安装或指定 Java」的对话框，并用
// RequiredJavaMajor 预选要下载的大版本。它不是错误：缺 Java 是可预期的、
// 用户可自行解决的状态。
func (s *InstanceService) CheckLaunchJava(req JavaReadinessRequest) (launch.JavaReadiness, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.openLocked(); err != nil {
		return launch.JavaReadiness{}, err
	}
	// 只做存在性校验：真正的路径解析在 launch 包里，与启动共用。
	if _, err := s.getLocked(req.InstanceID); err != nil {
		return launch.JavaReadiness{}, err
	}
	return launch.ResolveInstanceJavaReadiness(req.InstanceID)
}
