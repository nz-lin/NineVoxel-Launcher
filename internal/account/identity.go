// 本文件提供账号包面向**其他后端模块**的唯一查询入口。
//
// 为什么单独一个文件：
//   - Wails 服务壳（wails.go）只该暴露面向前端的 DTO；
//   - 启动链路需要的是「档案 + 凭证」这种后端专用数据，不能经过前端；
//   - 把跨包入口集中在这里，便于审计「谁在读取账号凭证」。
//
// 安全约定：调用方必须保证返回值不外泄（不写日志、不进前端、不进错误文案）。
package account

import "context"

// ResolveAccountIdentity 返回启动游戏所需的档案与凭证。
//
// 行为：读取账号记录；若凭证即将过期且来源支持刷新，先续期再返回
// （续期失败原样返回错误，界面应提示重新登录）。
//
// 目前唯一调用方是 internal/app 的装配层：它把结果转换成
// launch.AccountIdentity 注入启动链路，使 internal/launch 不依赖本包。
func ResolveAccountIdentity(ctx context.Context, accountID string) (Profile, Credentials, *Error) {
	service, initErr := sharedAccountService()
	if initErr != nil {
		return Profile{}, Credentials{}, initErr
	}
	return service.ResolveCredentials(ctx, accountID)
}
