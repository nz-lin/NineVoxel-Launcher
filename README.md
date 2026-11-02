# NineVoxel Launcher

NineVoxel Launcher 是面向 Minecraft Java Edition 的跨平台桌面启动器。本仓库当前处于 P0「工程基础与技术验证」，尚未进入业务功能开发。

## 当前成果

- Wails v3、Go、Vue 3、TypeScript、Vite、Pinia 与 Vue Router 的桌面工程骨架。
- Go `AppService.PublishRuntimeInfo` 返回运行时诊断信息，并发布 `app:runtime` 事件，供前端验证 Wails Services、Bindings 与 Events 通信。
- SQLite 驱动评估与 Wazero 技术冒烟记录；驱动尚未集成到应用。
- WebView 隔离评估结论：P0 禁用 Custom Web UI，仅保留 Schema UI 的后续方向。

## 当前边界

此阶段不实现 Minecraft 启动、下载、账号认证、实例管理、插件运行时或插件市场。没有固定 localhost HTTP 服务。开发模式使用 Wails 管理的 Vite 预览服务。SQLite 接入应用前需满足 [ADR-002](docs/decisions/ADR-002-database.md) 的用户确认门槛；Custom Web UI 按 [ADR-009](docs/decisions/ADR-009-custom-web-ui-isolation.md) 保持关闭。

## 固定工具链

- Go `1.27.3`（模块最低语言版本 `1.26.0`）
- Wails CLI 与 `@wailsio/runtime` `3.0.0`
- Node.js `22.16.0`、pnpm `10.33.0`
- Vue `3.5.44`、Vue Router `5.4.0`、Pinia `4.1.0`
- TypeScript `5.9.3`、Vite `8.3.5`、`@vitejs/plugin-vue` `6.0.10`、`vue-tsc` `3.3.14`

详见 [工具链锁定](docs/development/toolchain.md)。

## 本地工程命令

在 `launcher/` 目录执行：

```powershell
pnpm --dir frontend install --frozen-lockfile
wails3 generate bindings -clean=true -ts -i -d frontend/bindings .
wails3 dev
```

前端静态构建与 Go 静态检查：

```powershell
pnpm --dir frontend run build
go vet ./...
```

以上是工程检查，不代表用户功能验收。桌面开发运行需安装 Wails v3 所需的平台工具与 Windows WebView2 Runtime。

## Windows 构建说明

Windows 构建使用标准 wails v3.0.0 依赖，不需要额外的本地补丁或 `WebView2Loader.dll`。

```powershell
wails3 task build
```

启动器会按 [ADR-011](docs/decisions/ADR-011-webview2-user-data-directory.md) 显式选择 WebView2 用户数据目录：
优先 `<exe 目录>\webview`，该目录不可写时回退到 `%LocalAppData%\NineVoxel-Launcher\webview`。
启动日志同样优先用户目录，不可写时回退到 `<exe 目录>\logs`。`webview/` 与 `logs/` 都是运行时数据，已加入忽略列表。

安装包需要保证安装目录可写；安装到 `Program Files` 等只读目录时，WebView2 数据会自动落在用户目录。

## P0 验收边界

Agent 只执行格式、静态检查、类型检查、绑定生成和构建。桌面窗口启动、前后端事件显示与真实用户体验由用户本人验收；未执行的检查会在 [开发进度](docs/development/progress.md) 中标明。
