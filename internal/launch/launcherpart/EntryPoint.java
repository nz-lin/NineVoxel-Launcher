// NineVoxel Launcher Part：从 stdin 读取启动脚本，在**同一个 JVM** 内反射调用游戏主类。
//
// 存在的理由：Windows 的 CreateProcess 对整条命令行有 32767 字符上限，装有加载器
// （Forge / NeoForge）的实例会把大量依赖库塞进 -cp 与 -p，游戏参数（含 accessToken、
// userProperties、--tweakClass）再叠上去就会超出。把游戏参数改由 stdin 传递之后，
// 命令行只剩 JVM 参数 + classpath + 本类名，真正的游戏参数不再受命令行长度限制，
// 同时令牌不再出现在任务管理器 / ps 的命令行里。
//
// 输入协议（UTF-8，逐行）：
//     mainClass <游戏主类全限定名>
//     param <一个游戏参数>
//     param <另一个游戏参数>
//     launch
// 收到 launch 后立即反射调用，并在调用期间**保持 stdin 不关闭**：启动器用关闭 stdin
// 触发优雅停止，这条通道的语义必须保留。
//
// 兼容性：必须按 Java 8（class 版本 52.0）编译，因为 1.12.2 及更早的实例使用 Java 8。
// 构建命令见同目录 README 或 scripts/build-launcher-part.ps1。
package com.ninezhou.ninevoxel;

import java.io.BufferedReader;
import java.io.IOException;
import java.io.InputStreamReader;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
import java.nio.charset.Charset;
import java.util.ArrayList;
import java.util.List;

public final class EntryPoint {

    /** 协议键：声明游戏主类。 */
    private static final String KEY_MAIN_CLASS = "mainClass";

    /** 协议键：追加一个游戏参数。 */
    private static final String KEY_PARAM = "param";

    /** 协议键：开始启动。收到后才反射调用主类。 */
    private static final String KEY_LAUNCH = "launch";

    /** 脚本不完整时的退出码（区别于游戏自身返回的退出码）。 */
    private static final int EXIT_SCRIPT_INCOMPLETE = 2;

    /** 主类调用失败时的退出码。 */
    private static final int EXIT_LAUNCH_FAILED = 1;

    private EntryPoint() {
        // 纯静态入口，不允许实例化。
    }

    public static void main(String[] args) {
        String mainClassName;
        List<String> gameArguments = new ArrayList<String>();

        try {
            mainClassName = readScript(gameArguments);
        } catch (IOException readError) {
            System.err.println("[ninevoxel] 读取启动脚本失败: " + readError.getMessage());
            System.exit(EXIT_SCRIPT_INCOMPLETE);
            return;
        }

        if (mainClassName == null) {
            // 脚本里没有 launch 行：启动器中途放弃或写入失败。
            System.err.println("[ninevoxel] 启动脚本不完整，未收到 launch 指令");
            System.exit(EXIT_SCRIPT_INCOMPLETE);
            return;
        }

        launch(mainClassName, gameArguments);
    }

    /**
     * 读取启动脚本，返回游戏主类名；未收到 launch 时返回 null。
     *
     * @param gameArguments 收集用的游戏参数列表（按出现顺序追加）
     * @return 游戏主类全限定名，或 null
     * @throws IOException stdin 读取失败
     */
    private static String readScript(List<String> gameArguments) throws IOException {
        BufferedReader reader = new BufferedReader(
                new InputStreamReader(System.in, Charset.forName("UTF-8")));

        String mainClassName = null;
        String line;
        while ((line = reader.readLine()) != null) {
            if (line.isEmpty()) {
                continue;
            }

            // 按第一个空格切分键与值：值里允许再出现空格（路径、参数片段）。
            int separator = line.indexOf(' ');
            String key = separator < 0 ? line : line.substring(0, separator);
            String value = separator < 0 ? "" : line.substring(separator + 1);

            if (KEY_MAIN_CLASS.equals(key)) {
                mainClassName = value;
                continue;
            }
            if (KEY_PARAM.equals(key)) {
                gameArguments.add(value);
                continue;
            }
            if (KEY_LAUNCH.equals(key)) {
                return mainClassName;
            }
            // 未知键忽略而不是报错：便于以后在协议里追加指令而不破坏旧启动器。
        }
        return null;
    }

    /**
     * 在**当前 JVM** 内反射调用游戏主类。
     *
     * 不另起进程：这样才不会产生第二条命令行，进程句柄、stdio 管道与退出码
     * 都直接对应游戏本身，启动器的 supervisor 无需感知引导层的存在。
     *
     * @param mainClassName 游戏主类全限定名
     * @param gameArguments 游戏参数
     */
    private static void launch(String mainClassName, List<String> gameArguments) {
        String[] arguments = gameArguments.toArray(new String[0]);

        try {
            Class<?> mainClass = Class.forName(mainClassName);
            Method mainMethod = mainClass.getMethod("main", String[].class);
            mainMethod.setAccessible(true);
            mainMethod.invoke(null, (Object) arguments);
        } catch (ClassNotFoundException notFound) {
            reportFailure("找不到游戏主类 " + mainClassName, notFound);
        } catch (NoSuchMethodException noMain) {
            reportFailure("游戏主类 " + mainClassName + " 没有 main(String[]) 方法", noMain);
        } catch (InvocationTargetException targetFailed) {
            // 游戏自身的异常必须按原样打印，否则堆栈里会套一层反射包装，
            // 用户和社区都无法据此搜索问题。
            Throwable cause = targetFailed.getCause();
            reportFailure("游戏启动失败", cause == null ? targetFailed : cause);
        } catch (Throwable other) {
            reportFailure("无法调用游戏主类 " + mainClassName, other);
        }
    }

    /**
     * 打印失败原因并以非零码退出。
     *
     * 非零退出码必须保留：启动器据此把结局判定为「崩溃」而不是「正常退出」。
     *
     * @param message 面向用户的简短说明
     * @param cause 原始异常（可能为 null）
     */
    private static void reportFailure(String message, Throwable cause) {
        System.err.println("[ninevoxel] " + message);
        if (cause != null) {
            cause.printStackTrace();
        }
        System.exit(EXIT_LAUNCH_FAILED);
    }
}
