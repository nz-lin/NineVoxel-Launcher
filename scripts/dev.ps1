<#
.SYNOPSIS
在 Windows 上以开发模式启动 NineVoxel Launcher。

.DESCRIPTION
统一准备 Go 编译缓存目录与网络代理，然后执行 wails3 task dev。
代理按以下优先级解析，避免把某一台机器的代理地址写死：
  1. -Proxy 参数；
  2. 环境变量 NV_DEV_PROXY；
  3. Windows 系统代理（Internet 选项 / WinINET）；
  4. 不使用代理。
Go 缓存全部放在仓库内被忽略的 .task 目录，避免污染用户全局 GOPATH。

.PARAMETER Proxy
显式指定代理地址，例如 http://127.0.0.1:7898。

.PARAMETER DryRun
只打印解析后的环境变量，不真正启动 wails3。

.EXAMPLE
pwsh -File scripts/dev.ps1
.EXAMPLE
pwsh -File scripts/dev.ps1 -Proxy http://127.0.0.1:7898
#>
param(
    [string] $Proxy = '',
    [switch] $DryRun,
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]] $Rest = @()
)

$ErrorActionPreference = 'Stop'

# 仓库根目录：本脚本位于 <repo>/scripts 下。
$repoRoot = Split-Path -Parent $PSScriptRoot
$taskDir = Join-Path $repoRoot '.task'

# Resolve-DevProxy 按优先级解析开发代理地址。
# 返回空字符串表示直连，不设置代理环境变量。
function Resolve-DevProxy {
    param([string] $Explicit)

    if ($Explicit) { return $Explicit }
    if ($env:NV_DEV_PROXY) { return $env:NV_DEV_PROXY }

    # 读取 Windows 系统代理配置；未启用或读取失败时返回空。
    $key = Get-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' -ErrorAction SilentlyContinue
    if (-not $key -or $key.ProxyEnable -ne 1 -or -not $key.ProxyServer) { return '' }

    $server = [string] $key.ProxyServer
    # 形如 https=host:port;http=host:port 时优先取 https。
    if ($server -match '(?i)(?:^|;)https?=(?<hp>[^;]+)') { $server = $Matches['hp'] }
    elseif ($server -match '(?i)^https?=(?<hp>[^;]+)') { $server = $Matches['hp'] }
    return "http://$server"
}

$proxy = Resolve-DevProxy -Explicit $Proxy

# Go 工具链与缓存目录：全部落在仓库内，便于清理与隔离。
$env:GOTOOLCHAIN = 'local'
$env:GOPATH = Join-Path $taskDir 'gopath'
$env:GOMODCACHE = Join-Path $taskDir 'gomodcache'
$env:GOCACHE = Join-Path $taskDir 'gocache'

if ($proxy) {
    $env:HTTP_PROXY = $proxy
    $env:HTTPS_PROXY = $proxy
    $env:ALL_PROXY = $proxy
    # 本机回环地址不走代理，避免 wails3 与 Vite 的本地端口被代理拦截。
    $env:NO_PROXY = 'localhost,127.0.0.1,::1'
} else {
    Write-Warning '未检测到代理配置，将直连网络。如需要请设置 NV_DEV_PROXY 或加 -Proxy 参数。'
}

if ($DryRun) {
    Write-Host '--- 开发环境变量（DryRun，不启动应用）---'
    'GOTOOLCHAIN = ' + $env:GOTOOLCHAIN
    'GOPATH      = ' + $env:GOPATH
    'GOMODCACHE  = ' + $env:GOMODCACHE
    'GOCACHE     = ' + $env:GOCACHE
    'HTTP_PROXY  = ' + $env:HTTP_PROXY
    'HTTPS_PROXY = ' + $env:HTTPS_PROXY
    'NO_PROXY    = ' + $env:NO_PROXY
    'RepoRoot    = ' + $repoRoot
    exit 0
}

Push-Location $repoRoot
try {
    # 使用 Taskfile 中的 dev 任务，保证 config 与前端端口和仓库保持一致。
    & wails3 task dev @Rest
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
