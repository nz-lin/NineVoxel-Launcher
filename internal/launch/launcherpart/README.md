# 启动引导器（launcher part）

本目录存放**启动引导器**的 Java 源码与编译产物，由 `internal/launch/launcherpart.go`
通过 `go:embed` 内嵌进启动器二进制。

## 它解决什么

Windows 的 `CreateProcess` 对整条命令行有 32767 字符上限。装有加载器
（Forge / NeoForge）的实例会把大量依赖库塞进 `-cp`、`-p`，游戏参数
（`--accessToken`、`--userProperties`、`--tweakClass` 等）再叠上去就会超界。

引导器把**游戏主类与全部游戏参数**从命令行移到 stdin：

```
java <JVM 参数> -cp <引导器目录>:<classpath> com.ninezhou.ninevoxel.EntryPoint
  └─ stdin:
       mainClass net.minecraft.client.main.Main
       param --username
       param Steve
       ...
       launch
```

收到 `launch` 后，它在**同一个 JVM 里**反射调用游戏主类，不会产生第二个进程，
因此进程句柄、stdio 管道与退出码仍直接对应游戏本身。

附带收益：`--accessToken` 不再出现在任务管理器 / `ps` 的命令行里。

**不使用 EOF 作为启动信号**：启动器用「关闭 stdin」实现优雅停止，
`launch` 之后 stdin 保持打开，这条停止路径的语义才不会被破坏。

## 硬约束

**必须按 Java 8（class 版本 52.0）编译。** 1.12.2 及更早的实例运行在 Java 8 上，
Java 9+ 目标版本的字节码会直接抛 `UnsupportedClassVersionError`。
`internal/launch/launcherpart_test.go` 的 `TestLauncherPartIsJava8Bytecode`
会守住这条约束。

## 重新编译

改了 `EntryPoint.java` 后，在仓库根目录执行：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/build-launcher-part.ps1
```

或手工执行：

```bash
javac --release 8 -encoding UTF-8 -Xlint:-options \
  -d internal/launch/launcherpart/classes \
  internal/launch/launcherpart/EntryPoint.java
```

编译产物 `classes/com/ninezhou/ninevoxel/EntryPoint.class` 需要提交到仓库：
构建时不依赖本机安装 JDK。

## 协议约定

| 行 | 含义 |
| --- | --- |
| `mainClass <全限定名>` | 游戏主类；必须出现在 `launch` 之前 |
| `param <值>` | 追加一个游戏参数，值里允许含空格 |
| `launch` | 立即反射调用主类 |

键与值的分隔符是**第一个空格**，因此值本身不需要转义；代价是值里不能含换行
（`buildLaunchScript` 会在写入前拒绝这种参数）。
未知键会被忽略，便于以后追加指令而不破坏旧版启动器。

## 已知取舍

- **`-cp` 仍在命令行上**：JVM 启动前就必须知道 classpath，放 stdin 没有效果。
  它本身超长时由 `@argfile` 兜底（Java 9+），Java 8 只能报错并提示缩短游戏目录。
- **`sun.java.command` 变成引导器类名**：系统属性由 java 启动器按真实命令行写入，
  因此它显示的是 `com.ninezhou.ninevoxel.EntryPoint` 而不是游戏主类。
  若将来某个加载器依赖该属性判断主类，可在这里调用
  `System.setProperty("sun.java.command", ...)` 还原。
- **stdin 被引导器独占读取**：客户端不读 stdin，因此不影响游戏；
  但若有插件想读控制台输入，会拿不到数据。

