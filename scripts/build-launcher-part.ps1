<#
.SYNOPSIS
    重新编译启动引导器（launcher part）的 Java 字节码。

.DESCRIPTION
    引导器必须按 Java 8（class 版本 52.0）编译：1.12.2 及更早的实例运行在
    Java 8 上，更高目标版本的字节码会直接抛 UnsupportedClassVersionError。

    编译产物 internal/launch/launcherpart/classes/ 需要提交到仓库，
    这样构建启动器本体时不依赖本机安装 JDK。

.PARAMETER JavacPath
    javac 可执行文件路径。默认从 PATH 查找。

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts/build-launcher-part.ps1
#>
param(
    [string]$JavacPath = "javac"
)

$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$sourceRoot = Join-Path $repositoryRoot "internal/launch/launcherpart"
$sourceFile = Join-Path $sourceRoot "EntryPoint.java"
$outputRoot = Join-Path $sourceRoot "classes"

if (-not (Test-Path $sourceFile)) {
    throw "未找到引导器源码：$sourceFile"
}

# 清理旧产物，避免删除源码目录后残留的 .class 被打进二进制。
if (Test-Path $outputRoot) {
    Remove-Item -Recurse -Force $outputRoot
}
New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null

& $JavacPath --release 8 -encoding UTF-8 -Xlint:-options -d $outputRoot $sourceFile
if ($LASTEXITCODE -ne 0) {
    throw "javac 编译失败（退出码 $LASTEXITCODE）"
}

$classFile = Join-Path $outputRoot "com/ninezhou/ninevoxel/EntryPoint.class"
if (-not (Test-Path $classFile)) {
    throw "编译后未找到预期类文件：$classFile"
}

$bytes = [System.IO.File]::ReadAllBytes($classFile)
$majorVersion = ([int]$bytes[6] -shl 8) -bor [int]$bytes[7]
if ($majorVersion -gt 52) {
    throw "class 版本 $majorVersion 高于 Java 8（52），Java 8 实例会启动失败"
}

Write-Host "引导器编译完成：class 版本 $majorVersion，$($bytes.Length) 字节"
