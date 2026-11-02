// 本文件只保留限速接缝（C-2 / D-30 已冻结为「P3 不做限速」）。
//
// 为什么仍然放一个接口而不是完全不写：
//   - 传输层需要提前留出调用点，否则将来接入限速要改 HTTPTransport 的读循环；
//   - 但本卡**不得**实现令牌桶、不得读取限速配置、不得新增依赖
//     （golang.org/x/time/rate 属禁止项）。
//
// 因此这里的默认实现恒为空操作，生产路径注入 nil 即可。
package download

import "context"

// RateLimiter 是单任务限速接缝。
//
// 语义：在写入 n 字节之前调用，阻塞直到「允许写这些字节」。
// 返回值用于响应 context 取消——传输层据此中止本次下载。
type RateLimiter interface {
	// WaitN 阻塞等待 n 字节的发送配额。
	WaitN(ctx context.Context, n int) error
}

// noopRateLimiter 是限速关闭时的实现：立即返回，不做任何计量。
//
// 刻意不记录「已限速」状态：P3 的限速未启用，
// 伪造一个「有限速」的观感只会让排查者误判。
type noopRateLimiter struct{}

// WaitN 立即返回 nil（空实现）。
func (noopRateLimiter) WaitN(_ context.Context, _ int) error {
	return nil
}

// NewNoopRateLimiter 返回限速关闭时的空实现。
//
// 传 nil 有同样的效果（HTTPTransport 内部会把 nil 归一为空实现），
// 提供这个构造函数是为了让调用点显式表达「限速关闭」而不是「忘了传」。
func NewNoopRateLimiter() RateLimiter {
	return noopRateLimiter{}
}
