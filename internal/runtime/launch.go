package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

// ResolveRuntimeExecutable returns the absolute Java executable path for a
// runtime ID. Callers must keep the result in the backend and must not render
// or persist it in a frontend DTO.
func ResolveRuntimeExecutable(runtimeID string) (string, error) {
	id := strings.TrimSpace(runtimeID)
	if id == "" {
		return "", errors.New("未选择 Java 运行时")
	}

	row, findErr := findRuntimeRow(context.Background(), id)
	if findErr != nil {
		return "", errors.New("无法读取 Java 运行时记录")
	}
	if row != nil {
		if row.Kind == SourceDownloaded {
			directory, dirErr := managedRuntimeDir(*row)
			if dirErr != nil {
				return "", errors.New("Java 运行时目录不可用")
			}
			executable := filepath.Join(directory, "bin", javaExecutableName())
			if !isRegularExecutable(executable) && goruntime.GOOS == "darwin" {
				// 兼容修复前安装的 macOS 运行时：旧布局是 <dir>/Contents/Home/bin/java。
				legacy := filepath.Join(directory, "Contents", "Home", "bin", javaExecutableName())
				if isRegularExecutable(legacy) {
					executable = legacy
				}
			}
			if !isRegularExecutable(executable) {
				return "", errors.New("Java 运行时文件已缺失，请重新安装")
			}
			return executable, nil
		}
	}

	// Local runtimes do not store an absolute path in SQLite. Resolve them from
	// the explicitly configured Java candidate while keeping the path internal.
	for _, candidate := range enumerateJavaCandidates() {
		info, probeErr := probeJavaExecutable(candidate.path)
		if probeErr != nil {
			continue
		}
		summary := runtimeSummaryFromProbe(candidate.path, info, nowFunc().UnixMilli())
		if summary.RuntimeID == id {
			return candidate.path, nil
		}
	}
	return "", errors.New("找不到该 Java 运行时，请重新选择")
}

// isRegularExecutable 判断路径存在且为非空普通文件。
func isRegularExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// ResolveSelectedRuntimeExecutable resolves the runtime currently selected in
// RuntimeService. It is used only when no explicit instance/global Java path is
// configured.
func ResolveSelectedRuntimeExecutable() (string, error) {
	id, appErr := selectedRuntimeID(context.Background())
	if appErr != nil {
		return "", errors.New("无法读取已选 Java 运行时")
	}
	if strings.TrimSpace(id) == "" {
		return "", errors.New("尚未选择 Java 运行时")
	}
	return ResolveRuntimeExecutable(id)
}

// runtimeCandidate 是选型时使用的运行时摘要。
//
// 抽成独立类型是为了让选型逻辑可以脱离数据库与文件系统做单元测试：
// 「挑哪个 Java」是纯函数，不该被探测与路径解析拖住。
type runtimeCandidate struct {
	// id 为运行时标识。
	id string
	// major 为大版本号。
	major int
	// full 为完整版本号，用于同大版本内比较。
	full string
}

// ResolveRuntimeExecutableForMajor 按版本要求的 Java 大版本挑选运行时。
//
// 为什么需要它：用户通常不会手动配置全局 Java，而「已选运行时」可能恰好是
// 另一个版本装的那个——用 Java 25 跑要求 Java 8 的 1.7.10 会直接崩。
// 版本 JSON 里的 `javaVersion.majorVersion` 是权威要求，据此匹配最省心。
func ResolveRuntimeExecutableForMajor(major int) (string, error) {
	if major <= 0 {
		return "", errors.New("版本未声明 Java 大版本要求")
	}
	rows, appErr := installedRuntimeRows(context.Background())
	if appErr != nil {
		return "", errors.New("无法读取已安装的 Java 运行时")
	}

	selectedID, _ := selectedRuntimeID(context.Background())
	candidates := make([]runtimeCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, runtimeCandidate{
			id:    row.ID,
			major: row.MajorVersion,
			full:  row.FullVersion,
		})
	}

	picked := pickRuntimeForMajor(candidates, strings.TrimSpace(selectedID), major)
	if picked == "" {
		return "", errors.New("未找到满足该版本要求的 Java，请在 Java 管理器中安装")
	}
	return ResolveRuntimeExecutable(picked)
}

// pickRuntimeForMajor 从候选中挑选最合适的大版本；无合适项时返回空串。
//
// 匹配规则：
//  1. 优先取大版本完全相等的；多个时优先「当前已选」，其次取版本号最高的；
//  2. 没有完全相等时，取「大于要求的最小大版本」——Java 向后兼容，
//     用 21 跑要求 17 的游戏通常可用，但**绝不能用低于要求的版本**；
//  3. 都不满足时返回空串，由调用方给出可操作提示。
//
// 参数 selectedID 为用户当前选中的运行时；空表示未选。
func pickRuntimeForMajor(candidates []runtimeCandidate, selectedID string, major int) string {
	if major <= 0 || len(candidates) == 0 {
		return ""
	}

	// 第一轮：完全相等。
	exact := ""
	exactFull := ""
	for _, candidate := range candidates {
		if candidate.major != major {
			continue
		}
		if candidate.id == selectedID {
			return candidate.id
		}
		if exact == "" || compareJavaFullVersion(candidate.full, exactFull) > 0 {
			exact = candidate.id
			exactFull = candidate.full
		}
	}
	if exact != "" {
		return exact
	}

	// 第二轮：大于要求的最小版本。
	higher := ""
	higherMajor := 0
	higherFull := ""
	for _, candidate := range candidates {
		if candidate.major <= major {
			continue
		}
		if higher == "" || candidate.major < higherMajor ||
			(candidate.major == higherMajor && compareJavaFullVersion(candidate.full, higherFull) > 0) {
			higher = candidate.id
			higherMajor = candidate.major
			higherFull = candidate.full
		}
	}
	return higher
}

// compareJavaFullVersion 比较两段 Java 完整版本号（`21.0.1` 或 `21.0.1+12`）。
//
// 返回负数表示 left 更小，0 表示相等，正数表示更大。
// 只比较开头的连续数字段：`+12` 这类构建号与 `LTS` 后缀不影响选型，
// 直接忽略可以避免为它们引入一套完整的版本解析。
func compareJavaFullVersion(left, right string) int {
	leftParts := javaVersionNumberParts(left)
	rightParts := javaVersionNumberParts(right)
	for index := 0; index < len(leftParts) || index < len(rightParts); index++ {
		leftValue := 0
		if index < len(leftParts) {
			leftValue = leftParts[index]
		}
		rightValue := 0
		if index < len(rightParts) {
			rightValue = rightParts[index]
		}
		if leftValue != rightValue {
			if leftValue < rightValue {
				return -1
			}
			return 1
		}
	}
	return 0
}

// javaVersionNumberParts 提取版本号开头的连续数字段。
//
// `+` 之后是构建号（`21.0.1+12` 的 `12`），不参与选型比较：
// 同一大版本的两个补丁版本不该因为构建号不同而分出高下。
// 遇到第一个非数字段同样停止。
func javaVersionNumberParts(version string) []int {
	trimmed := version
	if index := strings.IndexByte(trimmed, '+'); index >= 0 {
		trimmed = trimmed[:index]
	}
	segments := strings.FieldsFunc(trimmed, func(char rune) bool {
		return char == '.' || char == '-' || char == '_'
	})
	result := make([]int, 0, len(segments))
	for _, segment := range segments {
		value := 0
		for _, char := range segment {
			if char < '0' || char > '9' {
				return result
			}
			value = value*10 + int(char-'0')
		}
		result = append(result, value)
	}
	return result
}
