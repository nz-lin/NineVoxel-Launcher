#!/usr/bin/env bash
# 在 macOS / Linux 上以开发模式启动 NineVoxel Launcher。
#
# 统一准备 Go 编译缓存目录与网络代理，然后执行 wails3 task dev。
# 代理按以下优先级解析，避免把某一台机器的代理地址写死：
#   1. 环境变量 NV_DEV_PROXY；
#   2. 系统代理（macOS 通过 scutil 读取；Linux 读取 gsettings）；
#   3. 不使用代理。
# Go 缓存全部放在仓库内被忽略的 .task 目录，避免污染用户全局 GOPATH。
#
# 用法：
#   ./scripts/dev.sh                     # 自动解析代理后启动
#   NV_DEV_PROXY=http://127.0.0.1:7897 ./scripts/dev.sh
#   ./scripts/dev.sh --dry-run           # 只打印环境变量，不启动

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dry_run=0
args=()

for arg in "$@"; do
  case "$arg" in
    --dry-run) dry_run=1 ;;
    *) args+=("$arg") ;;
  esac
done

# resolve_proxy 解析开发代理地址；输出为空表示直连。
resolve_proxy() {
  if [ -n "${NV_DEV_PROXY:-}" ]; then
    printf '%s' "$NV_DEV_PROXY"
    return
  fi

  local host port
  if command -v scutil >/dev/null 2>&1; then
    # macOS：系统代理设置由 scutil --proxy 输出，优先 HTTPS，其次 HTTP。
    for scheme in HTTPS HTTP; do
      host="$(scutil --proxy | awk -v k="${scheme}Proxy" '$1 == k {print $3; exit}')"
      port="$(scutil --proxy | awk -v k="${scheme}Port" '$1 == k {print $3; exit}')"
      if [ -n "${host:-}" ] && [ -n "${port:-}" ]; then
        printf 'http://%s:%s' "$host" "$port"
        return
      fi
    done
  fi

  if command -v gsettings >/dev/null 2>&1; then
    # Linux：GNOME 桌面代理设置。
    local mode
    mode="$(gsettings get org.gnome.system.proxy mode 2>/dev/null | tr -d "'")"
    if [ "$mode" = "manual" ]; then
      host="$(gsettings get org.gnome.system.proxy.https host 2>/dev/null | tr -d "'")"
      port="$(gsettings get org.gnome.system.proxy.https port 2>/dev/null)"
      if [ -n "${host:-}" ]; then
        printf 'http://%s:%s' "$host" "$port"
        return
      fi
    fi
  fi

  printf ''
}

proxy="$(resolve_proxy)"

# Go 工具链与缓存目录：全部落在仓库内，便于清理与隔离。
export GOTOOLCHAIN=local
export GOPATH="$repo_root/.task/gopath"
export GOMODCACHE="$repo_root/.task/gomodcache"
export GOCACHE="$repo_root/.task/gocache"

if [ -n "$proxy" ]; then
  export HTTP_PROXY="$proxy"
  export HTTPS_PROXY="$proxy"
  export ALL_PROXY="$proxy"
  # 本机回环地址不走代理，避免 wails3 与 Vite 的本地端口被代理拦截。
  export NO_PROXY="localhost,127.0.0.1,::1"
else
  echo "警告：未检测到代理配置，将直连网络。如需要请设置 NV_DEV_PROXY。" >&2
fi

if [ "$dry_run" -eq 1 ]; then
  echo "--- 开发环境变量（dry-run，不启动应用）---"
  echo "GOTOOLCHAIN = $GOTOOLCHAIN"
  echo "GOPATH      = $GOPATH"
  echo "GOMODCACHE  = $GOMODCACHE"
  echo "GOCACHE     = $GOCACHE"
  echo "HTTP_PROXY  = ${HTTP_PROXY:-<未设置>}"
  echo "HTTPS_PROXY = ${HTTPS_PROXY:-<未设置>}"
  echo "NO_PROXY    = ${NO_PROXY:-<未设置>}"
  echo "RepoRoot    = $repo_root"
  exit 0
fi

cd "$repo_root"
# 使用 Taskfile 中的 dev 任务，保证 config 与前端端口和仓库保持一致。
# 注意：macOS 自带 bash 3.2，空数组直接展开会退化成空参数，这里显式分支处理。
if [ "${#args[@]}" -gt 0 ]; then
  exec wails3 task dev "${args[@]}"
else
  exec wails3 task dev
fi
