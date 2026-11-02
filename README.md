# NineVoxel Launcher

NineVoxel Launcher 是面向 Minecraft Java Edition 的跨平台桌面启动器，基于 Wails v3（Go 后端 + WebView 前端）构建。

当前仓库为 **P3 阶段代码基线**：版本清单与安装计划、下载队列、Java 运行时管理、启动参数预览与进程监督、启动配置、设置与日志等后端能力已落地；部分前端界面仍使用演示数据，账号认证、实例管理、插件/模组市场尚未实现，部分前后端链路仍在接线中，具体以代码为准。

## 技术栈

| 层 | 技术 |
| --- | --- |
| 桌面壳 | Wails v3.0.0 |
| 后端 | Go 1.26（`go.mod` toolchain `go1.27.3`）、modernc.org/sqlite |
| 前端 | React 19、TypeScript、Tailwind CSS 4 |
| 前端构建 | Vite 8、pnpm（锁文件 `frontend/pnpm-lock.yaml`） |

版本锁定：`go.mod` / `go.sum`、`.node-version`（Node 22.16.0）、`frontend/pnpm-lock.yaml`、CI 变量（`.gitlab-ci.yml`）。

Vite 只用于前端构建与开发调试，**运行期不参与**：前端产物 `frontend/dist` 通过 `//go:embed` 嵌入二进制，由 Wails 资源服务器直接提供。

## 仓库结构

```text
.                     Go 模块根：Wails 应用入口（main.go / appservice.go / logservice.go）
internal/app          Wails 装配、窗口与启动/退出钩子
internal/artifact     数据根、制品路径、内容寻址与路径边界校验
internal/database     SQLite 打开、迁移与 checksum 校验
internal/download     下载队列、断点续传、校验、隔离与启动恢复
internal/events       事件信封、事件名与载荷类型注册
internal/launch       游戏目录检查、版本解析、启动计划、进程监督、游戏日志
internal/logging      结构化日志、脱敏、文件轮转与查询
internal/platform     用户目录、WebView2 目录、主题与窗口的平台差异
internal/runtime      Java 运行时探测、Adoptium/官方来源安装与选择
internal/settings     设置 schema、SQLite 持久化与变更事件
internal/version      版本清单、版本详情、安装计划与兼容报告
frontend/             React 前端（bindings/ 与 dist/ 为生成目录，不入库）
build/                Wails 构建配置与各平台打包脚本
```

## 本地开发

前置工具：Go（按 `go.mod` 的 toolchain）、Node 22.16.0、pnpm、Wails CLI v3.0.0：

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
```

首次开发：

```bash
pnpm --dir frontend install --frozen-lockfile
wails3 generate bindings -clean=true -ts -i   # 生成 frontend/bindings（构建任务也会自动执行）
wails3 dev                                    # 开发模式：Vite dev server + 热重载
```

`wails3 dev` 依赖 Vite dev server（默认 `127.0.0.1:9245`）。若只想运行构建产物，可执行 `wails3 task build` 后直接运行 `bin/ninevoxel-launcher`。

## 检查与构建

```bash
pnpm --dir frontend run lint      # tsc --noEmit 类型检查
pnpm --dir frontend run build     # 生产前端资源 → frontend/dist
go vet ./...
go test ./...
wails3 task build                 # 当前平台构建（产物在 bin/）
wails3 task package               # 当前平台安装包
```

注意：`main.go` 使用 `//go:embed all:frontend/dist` 引用前端产物。干净 checkout 下直接执行 `go build` / `go vet` / `go test` 会因 `frontend/dist` 不存在而失败，需先完成前端构建（CI 中通过写入占位文件规避）。`frontend/bindings`、`frontend/dist`、`bin` 均为生成目录，已加入忽略列表。

## 数据与日志位置

应用数据根（SQLite `app.db`、`versions/`、`artifacts/`、`downloads/`、`runtimes/`）：

- macOS：`~/Library/Application Support/NineVoxel-Launcher/data`
- Windows：`%AppData%\NineVoxel-Launcher\data`
- 兜底：可执行文件同级 `data/`；便携版以该目录已存在为判定依据

日志目录（`launcher.log`、`app-YYYYMMDD[-序号].log`、`game-<launchId>-YYYYMMDD[-序号].log`）：

- macOS：`~/Library/Application Support/NineVoxel-Launcher/logs`
- Windows：`%LocalAppData%\NineVoxel-Launcher\logs`
- 兜底：可执行文件同级 `logs/`

`webview/`（WebView2 用户数据目录）只对 Windows 有意义：优先 `<exe 目录>\webview`，不可写时回退 `%LocalAppData%\NineVoxel-Launcher\webview`；macOS 使用系统 WKWebView 的数据目录，应用不再创建该目录。

## 验收边界

Agent 与 CI 只执行格式检查、静态检查、类型检查、单元测试、绑定生成与构建；桌面窗口、前后端事件与真实交互由人工验收。设计文档（ADR、任务卡、开发进度等）未随本仓库分发，本文档不引用仓库中不存在的路径。
