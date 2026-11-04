// 本文件实现 P2-09「启动计划预览」：把内部的 LaunchPlan 转成面向用户的脱敏预览。
//
// 设计约束（决策记录第八节 D-14~D-18，不得自行放宽）：
//   - 预览与启动**共用同一份构建函数** `buildLaunchPlan`（P2-04），本文件只做
//     「编排 + 脱敏 + 警告码映射」，绝不另写一套参数拼装逻辑；
//   - 所有脱敏复用 P2-07 的 `RedactForDisplay` / `RedactCommandSummary`，
//     以及 P2-04 的 `RedactPathInArgument` / `buildGameRootDisplay`；
//   - 预览**不启动任何进程**（尤其不执行 `java -version` 探针，D-16），
//     也不写任何文件。
package launch

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// ===== D-18：预览警告码 =====
//
// 这些是 `warnings[].code` 的取值，**不是** Error.code：它们描述「预览里出现的
// 可读警告」，不参与 launch 包的错误分类，因此不放进 errors.go 的错误码常量区。
const (
	// WarningCodePlaceholderUnresolved 表示版本 JSON 中有占位符无法解析（blocking）。
	WarningCodePlaceholderUnresolved = "PLACEHOLDER_UNRESOLVED"

	// WarningCodeLibrariesMissing 表示依赖库缺失（blocking）。
	WarningCodeLibrariesMissing = "LIBRARIES_MISSING"

	// WarningCodeNativesMissing 表示版本声明了 natives 但目录缺失（blocking）。
	WarningCodeNativesMissing = "NATIVES_MISSING"

	// WarningCodeClassPathEmpty 表示 classpath 为空（blocking）。
	WarningCodeClassPathEmpty = "CLASSPATH_EMPTY"

	// WarningCodeOfflineIdentity 表示当前使用离线占位身份（info）。
	WarningCodeOfflineIdentity = "OFFLINE_IDENTITY"

	// WarningCodeJavaVersionUnknown 表示没有可用的 Java 版本校验结果（info）。
	WarningCodeJavaVersionUnknown = "JAVA_VERSION_UNKNOWN"
)

// 预览专用文案。
//
// 这些文案只出现在预览 DTO 里，因此集中定义在本文件，避免与 P2-04 的构建提示混在一起。
const (
	// messagePreviewPlaceholderUnresolved 是未解析占位符的提示模板；参数为数量与占位符列表。
	messagePreviewPlaceholderUnresolved = "存在 %d 个未解析的占位符：%s；该版本当前不可直接启动。"

	// messagePreviewJavaVersionUnknown 表示没有可复用的 Java 版本校验结果（D-16）。
	messagePreviewJavaVersionUnknown = "尚未校验过 Java 版本，预览不执行版本探测；请先在设置中校验 Java 后重新预览。"

	// notePreviewOfflinePlaceholder 是 `--username` 值的补充标注（D-15：标注「离线占位身份」）。
	notePreviewOfflinePlaceholder = "离线占位身份"

	// diagnosticsTitle 是诊断信息文本的标题行。
	diagnosticsTitle = "NineVoxel 启动计划预览（已脱敏）"

	// diagnosticsLineIndent 是诊断信息中参数项的行前缀。
	diagnosticsLineIndent = "  - "

	// diagnosticsUnavailable 是字段缺失时的占位说明，避免输出「空白行」难以阅读。
	diagnosticsUnavailable = "（无）"
)

// 预览摘要的容量常量。
const (
	// previewClassPathHeadEntries 是 classpath 摘要保留的头部条目数（D-14：前 5 条）。
	previewClassPathHeadEntries = 5

	// previewClassPathTailEntries 是 classpath 摘要保留的尾部条目数（D-14：末 1 条）。
	previewClassPathTailEntries = 1

	// maxPreviewArgumentRunes 是单个参数摘要的最大字符数。
	//
	// 目的：极长的 `-D...=` 参数会把界面撑爆，也会让复制出来的诊断信息难以阅读；
	// 截断值本身（不是键名）既保留可读性，也不影响用户确认「启动器解析到了什么」。
	maxPreviewArgumentRunes = 160
)

// javaValidationSnapshot 是 Java 校验结果的只读快照。
type javaValidationSnapshot struct {
	// present 表示是否已经有过一次成功的校验记录（含「校验失败但已有结论」）。
	present bool

	// valid 表示该次校验的结论是否为「可用」。
	valid bool

	// display 为脱敏后的 Java 展示名（P2-01 口径）。
	display string

	// major 为 Java 大版本号；0 表示未知。
	major int
}

var (
	// javaValidationMutex 保护 javaValidationCache，避免校验与预览并发读写。
	javaValidationMutex sync.RWMutex

	// javaValidationCache 保存 P2-01 最近一次校验结果（D-16）。
	//
	// 为什么用包级缓存而不是每次预览重新探测：D-16 明确禁止预览执行
	// `java -version` 探针（启动子进程既慢又会在预览这种只读操作里产生副作用），
	// 因此只能复用用户最近一次「校验 Java」的结果。
	javaValidationCache javaValidationSnapshot
)

// rememberJavaValidation 记录一次 Java 校验结果，供后续预览复用。
//
// 参数 result 为 P2-01 的校验结果；JavaDisplay 在写入前再走一次路径脱敏，
// 保证缓存里存的就是「可展示形态」，预览不必重复处理。
func rememberJavaValidation(result JavaValidationResult) {
	javaValidationMutex.Lock()
	defer javaValidationMutex.Unlock()

	javaValidationCache = javaValidationSnapshot{
		present: true,
		valid:   result.Valid,
		display: RedactPathText(result.JavaDisplay),
		major:   result.MajorVersion,
	}
}

// currentJavaValidation 读取最近一次 Java 校验结果。
//
// 返回值为快照；没有记录时 present 为 false，调用方据此展示「版本未知」。
func currentJavaValidation() javaValidationSnapshot {
	javaValidationMutex.RLock()
	defer javaValidationMutex.RUnlock()
	return javaValidationCache
}

// buildPreviewFromPlan 把内部 LaunchPlan 转换为 P2-09 的面向用户预览。
//
// 参数 plan 为 P2-04 的构建结果（非 nil）；versionID 与 versionType 来自版本 JSON，
// 用于展示（非敏感）。
//
// 返回的预览满足 D-14（路径脱敏）、D-15（身份脱敏）、D-17（诊断文本）与
// D-18（警告码表）；`safeToLaunch` 由 blocking 级警告推导。
func buildPreviewFromPlan(plan *LaunchPlan, versionID, versionType string) LaunchPlanPreview {
	preview := LaunchPlanPreview{
		VersionID:               versionID,
		VersionType:             versionType,
		MainClass:               plan.MainClass,
		ClassPathEntryCount:     len(plan.ClassPathEntries),
		ClassPathEntries:        summarizeClassPath(plan.ClassPathEntries),
		JVMMajorArgs:            summarizeArgumentParts(plan.JVMMajorArgs),
		GameArguments:           summarizeArgumentParts(plan.GameArguments),
		NativesDirectoryDisplay: redactPathForPreview(plan.NativesDirectory, displayCategoryGame),
		AssetsDirectoryDisplay:  redactPathForPreview(plan.AssetsDirectory, displayCategoryGame),
		WorkingDirectoryDisplay: redactPathForPreview(plan.WorkingDirectory, displayCategoryGame),
		AssetIndexID:            plan.AssetIndexID,
		OfflineIdentityNotice:   messageOfflineIdentity,
		UnresolvedPlaceholders:  sortedUnresolvedPlaceholders(plan.UnresolvedPlaceholders),
	}

	// Java 展示名与大版本：只来自 P2-01 的最近一次校验缓存（D-16）。
	preview.JavaDisplay, preview.JavaMajorVersion, preview.JavaCheckStale = describePreviewJava()

	// 警告：按 D-18 码表映射；同时解析缺失库数量。
	preview.Warnings = mapPlanWarnings(plan.Warnings, plan.UnresolvedPlaceholders)
	if preview.JavaMajorVersion == 0 {
		preview.Warnings = append(preview.Warnings, ArgumentWarning{
			Code:     WarningCodeJavaVersionUnknown,
			Message:  messagePreviewJavaVersionUnknown,
			Severity: warningSeverityInfo,
		})
	}

	// blocking 子集供 UI 直接渲染阻断原因；安全判定以它为准（D-18）。
	preview.BlockingWarnings = filterBlockingWarnings(preview.Warnings)
	preview.SafeToLaunch = len(preview.BlockingWarnings) == 0

	// 缺失库数量：从 P2-04 的缺失库警告里解析，供界面显示「计数与缺失数」。
	preview.ClassPathMissingCount = countMissingLibraries(preview.Warnings)

	// 诊断文本（D-17）：在已脱敏数据上再走一次脱敏函数后生成。
	preview.DiagnosticsText = buildPlanDiagnostics(preview)

	return preview
}

// buildFailedPreview 构造「计划构建失败」的结构化预览。
//
// 卡面第 78 行要求失败也给出结构化原因而不是通用错误：因此失败时仍返回
// `LaunchPlanPreview`，把稳定错误码与脱敏原因放在 `BuildErrorCode` /
// `BuildErrorMessage` 与一条 blocking 警告里，前端无需解析错误字符串。
//
// 参数 versionID 为预览目标版本；errorCode 为 P2-04 的稳定错误码（可为空）；
// warningCode 为展示用的警告码（classpath 为空时用 D-18 的 `CLASSPATH_EMPTY`）；
// message 为面向用户的原因说明（调用方保证已脱敏）。
func buildFailedPreview(versionID, errorCode, warningCode, message string, extraWarnings []ArgumentWarning) LaunchPlanPreview {
	preview := LaunchPlanPreview{
		VersionID:              versionID,
		ClassPathEntries:       []string{},
		JVMMajorArgs:           []PreviewArgumentPart{},
		GameArguments:          []PreviewArgumentPart{},
		Warnings:               []ArgumentWarning{},
		UnresolvedPlaceholders: []string{},
		OfflineIdentityNotice:  messageOfflineIdentity,
		BuildFailed:            true,
		BuildErrorCode:         errorCode,
		BuildErrorMessage:      RedactForDisplay(message),
		SafeToLaunch:           false,
	}

	// Java 字段照常填充：即使计划失败，用户也需要看到「当前用的是哪个 Java」。
	preview.JavaDisplay, preview.JavaMajorVersion, preview.JavaCheckStale = describePreviewJava()

	if warningCode == "" {
		warningCode = errorCode
	}
	preview.Warnings = append(preview.Warnings, ArgumentWarning{
		Code:     warningCode,
		Message:  RedactForDisplay(message),
		Severity: warningSeverityBlocking,
	})
	// 归一化 / 版本加载阶段的附带提示（warn 级）一并展示，便于排查。
	preview.Warnings = append(preview.Warnings, extraWarnings...)

	preview.BlockingWarnings = filterBlockingWarnings(preview.Warnings)
	preview.DiagnosticsText = buildPlanDiagnostics(preview)

	return preview
}

// previewLaunchPlanForUser 编排一次完整的预览构建。
//
// 调用链与启动路径**完全一致**（这是「预览与启动共用同一份 LaunchPlan」的证据）：
// `normalizeGameRoot` → `isVersionIDValid` → `loadMergedVersion` → `buildLaunchPlan`。
// 其中 `buildLaunchPlan` 是 P2-04 的唯一构建函数，P2-05 的启动路径也调用它，
// 因此预览看到的参数与真正要执行的参数来自同一份代码。
//
// 预览阶段**不启动进程、不写文件**：Java 版本信息只读缓存（D-16）。
func previewLaunchPlanForUser(rawGameDirectory, versionID string, additionalJvmArgs, additionalGameArgs []string) LaunchPlanPreview {
	root, normalizeWarnings, pathErr := normalizeGameRoot(rawGameDirectory)
	if pathErr != nil {
		return buildFailedPreview(versionID, pathErr.Code, pathErr.Code, pathErr.Message, nil)
	}

	if !isVersionIDValid(versionID) {
		return buildFailedPreview(versionID, ErrorCodeVersionMetadata, ErrorCodeVersionMetadata,
			messageVersionIDInvalid, nil)
	}

	outcome := loadMergedVersion(root, versionID)
	// 版本加载阶段的提示（例如符号链接解析失败）统一按 warn 级展示。
	loadWarnings := buildVersionLoadWarnings(normalizeWarnings, outcome.warnings)
	if outcome.merged == nil {
		code := outcome.errorCode
		if code == "" {
			code = ErrorCodeVersionMetadata
		}
		return buildFailedPreview(versionID, code, code, outcome.reason, loadWarnings)
	}

	plan, planErr := buildLaunchPlan(root, selfContainedResourceRoot(root), versionID, outcome.merged, additionalJvmArgs, additionalGameArgs)
	if planErr != nil {
		// classpath 为空时用 D-18 的 CLASSPATH_EMPTY 作为展示码，其余沿用原错误码。
		warningCode := planErr.Code
		if planErr.Message == messageClassPathEmpty {
			warningCode = WarningCodeClassPathEmpty
		}
		return buildFailedPreview(versionID, planErr.Code, warningCode, planErr.Message, loadWarnings)
	}

	preview := buildPreviewFromPlan(plan, versionID, outcome.merged.Type)
	// 附加的 warn 级提示不参与安全判定，只用于告知用户「加载阶段发生过什么」。
	preview.Warnings = append(preview.Warnings, loadWarnings...)
	preview.BlockingWarnings = filterBlockingWarnings(preview.Warnings)
	preview.SafeToLaunch = len(preview.BlockingWarnings) == 0
	preview.DiagnosticsText = buildPlanDiagnostics(preview)

	return preview
}

// buildVersionLoadWarnings 把归一化与版本加载阶段的文本提示转成 warn 级警告。
//
// 参数 normalizeWarnings 为游戏目录归一化产生的提示；loadWarnings 为版本加载提示。
// 这些提示不是 D-18 码表内的安全项，因此沿用来源错误码并标为 warn 级。
func buildVersionLoadWarnings(normalizeWarnings, loadWarnings []string) []ArgumentWarning {
	result := make([]ArgumentWarning, 0, len(normalizeWarnings)+len(loadWarnings))
	for _, text := range normalizeWarnings {
		result = append(result, ArgumentWarning{
			Code:     ErrorCodeGameDir,
			Message:  RedactPathText(text),
			Severity: warningSeverityWarn,
		})
	}
	for _, text := range loadWarnings {
		result = append(result, ArgumentWarning{
			Code:     ErrorCodeLaunchArgs,
			Message:  RedactPathText(text),
			Severity: warningSeverityWarn,
		})
	}
	return result
}

// describePreviewJava 返回预览用的 Java 展示名、大版本与「来自历史校验」标志。
//
// 返回的三个值分别是：脱敏展示名（无记录时为空）、大版本号（0 表示未知）、
// 是否来自历史校验缓存（D-16 要求 UI 据此提示「以最近一次校验为准」）。
func describePreviewJava() (string, int, bool) {
	snapshot := currentJavaValidation()
	if !snapshot.present || !snapshot.valid {
		// 没有校验记录或上次校验失败：不猜版本，交给 UI 提示「未知」。
		return "", 0, false
	}
	if snapshot.major <= 0 {
		return snapshot.display, 0, true
	}
	return fmt.Sprintf("%s（Java %d）", snapshot.display, snapshot.major), snapshot.major, true
}

// summarizeClassPath 生成 classpath 摘要（D-14：计数之外只给前 5 + 末 1 条）。
//
// 每条都转成「游戏目录\…\<末两段>」形态：完整路径会暴露用户名与磁盘结构，
// 而预览只需要让用户确认「依赖库解析到了什么」。
func summarizeClassPath(entries []string) []string {
	if len(entries) == 0 {
		return []string{}
	}

	// 条目不多时全量展示（前 5 + 末 1 已覆盖全部），避免出现「省略」的空档。
	limit := previewClassPathHeadEntries + previewClassPathTailEntries
	if len(entries) <= limit {
		result := make([]string, 0, len(entries))
		for _, entry := range entries {
			result = append(result, redactPathForPreview(entry, displayCategoryGame))
		}
		return result
	}

	result := make([]string, 0, limit)
	for _, entry := range entries[:previewClassPathHeadEntries] {
		result = append(result, redactPathForPreview(entry, displayCategoryGame))
	}
	tailStart := len(entries) - previewClassPathTailEntries
	for _, entry := range entries[tailStart:] {
		result = append(result, redactPathForPreview(entry, displayCategoryGame))
	}
	return result
}

// summarizeArgumentParts 生成参数摘要数组（每个参数一项，值已脱敏）。
//
// 脱敏顺序固定，每一步都有理由：
//  1. `RedactCommandSummary`（P2-07）：遮挡 `--accessToken` / `--uuid` / `--username` 等凭证类值；
//  2. `RedactPathInArgument`（P2-04）：把参数里的绝对路径换成脱敏形态；
//  3. 超长值截断：避免把界面撑爆；
//  4. 为 `--username` 的值补「离线占位身份」标注（D-15）。
//
// 预览**不拼接完整命令行**（D-14）：参数始终保持数组形态，每项独立展示。
func summarizeArgumentParts(parts []ArgumentPart) []PreviewArgumentPart {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		values = append(values, part.Value)
	}

	// 1. P2-07 的凭证脱敏（`--uuid` 已由 P2-07 的敏感键表覆盖，本卡不再补第二套规则）。
	values = RedactCommandSummary(values)

	// 4 的输入：原始参数中的 --username 位置（值被遮挡后仍要知道哪一项是占位名）。
	usernameIndexes := previewUsernameIndexes(parts)

	result := make([]PreviewArgumentPart, 0, len(parts))
	for index, part := range parts {
		value := values[index]
		note := ""

		// D-15：`--username` 的值展示为占位名 `Player` 并标注「离线占位身份」。
		//
		// 为什么这里要覆盖 P2-07 的结果：`RedactCommandSummary` 按凭证处理，
		// 会把 `--username` 的值替换成 `***`；但离线占位名本身不敏感，
		// 而且 D-15 要求用户能在预览里看到它就是占位身份。
		// 恢复范围严格限制在「原值等于占位名」的情形，其它取值仍保持 `***`。
		if usernameIndexes[index] {
			value = placeholderUsername
			note = notePreviewOfflinePlaceholder
		}

		// 2. 路径脱敏：已脱敏的文本再次处理是幂等的。
		redacted, pathChanged := RedactPathInArgument(value)

		// 3. 超长值截断（只截断不含分隔语义的单值，不影响参数个数）。
		redacted, truncated := truncatePreviewArgument(redacted)

		result = append(result, PreviewArgumentPart{
			Value:    redacted,
			Source:   part.Source,
			Redacted: pathChanged || truncated,
			Note:     note,
		})
	}
	return result
}

// previewUsernameIndexes 找出「离线占位用户名」所在的参数下标。
//
// 命中条件：参数为 `--username`（不含内联形式），且下一项取值恰好是
// 占位名 `Player`（P2-04 的 `placeholderUsername`）。
//
// 只对占位名恢复展示，是因为 P2-07 的 `RedactCommandSummary` 把 `--username`
// 按凭证处理（替换为 `***`）；若将来接入真实登录，这里自然不再命中，
// 值继续保持 `***`，不会泄漏真实用户名。
//
// 参数 parts 为原始参数数组；返回下标集合。
func previewUsernameIndexes(parts []ArgumentPart) map[int]bool {
	result := make(map[int]bool)
	for index := 0; index < len(parts); index++ {
		key, _, hasInline := splitKeyValue(parts[index].Value)
		if key != "--username" || hasInline {
			continue
		}
		if index+1 < len(parts) && parts[index+1].Value == placeholderUsername {
			result[index+1] = true
		}
	}
	return result
}

// truncatePreviewArgument 截断超长参数值。
//
// 参数 value 为脱敏后的参数文本；返回截断结果与「是否发生截断」标志。
// 短参数原样返回，保证绝大多数参数（-Xmx2G、--version 等）零改动。
func truncatePreviewArgument(value string) (string, bool) {
	runes := []rune(value)
	if len(runes) <= maxPreviewArgumentRunes {
		return value, false
	}
	return string(runes[:maxPreviewArgumentRunes]) + "…", true
}

// mapPlanWarnings 把 P2-04 的构建警告映射为 D-18 码表的预览警告。
//
// 映射规则：
//   - 未解析占位符 → `PLACEHOLDER_UNRESOLVED`（blocking）；
//   - 依赖库缺失文案 → `LIBRARIES_MISSING`（blocking）；
//   - natives 缺失文案 → `NATIVES_MISSING`（blocking）；
//   - 离线身份文案 → `OFFLINE_IDENTITY`（info）；
//   - 其余 info（如默认分辨率提示）不进入预览码表：D-18 未为其定义码，
//     且它不影响安全判定，丢弃可避免 UI 出现「无码可读」的条目；
//   - 其它 blocking 警告原样保留（code 沿用来源错误码），确保阻断原因不被吞掉。
//
// 参数 planWarnings 为构建阶段警告；unresolved 为未解析占位符清单。
func mapPlanWarnings(planWarnings []ArgumentWarning, unresolved []UnresolvedPlaceholder) []ArgumentWarning {
	result := make([]ArgumentWarning, 0, len(planWarnings)+1)

	names := sortedUnresolvedPlaceholders(unresolved)
	if len(names) > 0 {
		result = append(result, ArgumentWarning{
			Code:     WarningCodePlaceholderUnresolved,
			Message:  fmt.Sprintf(messagePreviewPlaceholderUnresolved, len(names), strings.Join(names, "、")),
			Severity: warningSeverityBlocking,
		})
	}

	for _, warning := range planWarnings {
		message := RedactPathText(warning.Message)
		switch {
		case isLibrariesMissingMessage(warning.Message):
			result = append(result, ArgumentWarning{
				Code:     WarningCodeLibrariesMissing,
				Message:  message,
				Severity: warningSeverityBlocking,
			})
		case warning.Message == messageNativesMissing:
			result = append(result, ArgumentWarning{
				Code:     WarningCodeNativesMissing,
				Message:  message,
				Severity: warningSeverityBlocking,
			})
		case warning.Message == messageOfflineIdentity:
			result = append(result, ArgumentWarning{
				Code:     WarningCodeOfflineIdentity,
				Message:  message,
				Severity: warningSeverityInfo,
			})
		case warning.Severity == warningSeverityBlocking:
			result = append(result, ArgumentWarning{
				Code:     warning.Code,
				Message:  message,
				Severity: warningSeverityBlocking,
			})
		default:
			// info / warn 级且不在 D-18 码表内：不进预览，避免出现无码条目。
		}
	}

	return result
}

// isLibrariesMissingMessage 判断一条警告是否是「依赖库缺失」。
//
// 参数 message 为警告文案；返回 true 表示命中 P2-04 的缺失库模板。
func isLibrariesMissingMessage(message string) bool {
	return strings.HasPrefix(message, "有 ") && strings.HasSuffix(message, "个依赖库缺失，未加入 classpath。")
}

// parseMissingLibraryCount 从缺失库警告文案中解析缺失数量。
//
// 参数 message 为警告文案；返回数量与是否解析成功。
// 解析失败时返回 0，调用方按「未提供数量」处理。
func parseMissingLibraryCount(message string) (int, bool) {
	if !isLibrariesMissingMessage(message) {
		return 0, false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(message, "有 "), " 个依赖库缺失，未加入 classpath。")
	count, err := strconv.Atoi(strings.TrimSpace(body))
	if err != nil {
		return 0, false
	}
	return count, true
}

// filterBlockingWarnings 取出 blocking 级警告。
//
// 参数 warnings 为完整警告列表；返回其 blocking 子集（保持原顺序）。
func filterBlockingWarnings(warnings []ArgumentWarning) []ArgumentWarning {
	result := make([]ArgumentWarning, 0, len(warnings))
	for _, warning := range warnings {
		if warning.Severity == warningSeverityBlocking {
			result = append(result, warning)
		}
	}
	return result
}

// countMissingLibraries 统计预览中的缺失库数量。
//
// 参数 warnings 为预览警告列表；返回解析出的缺失数量（无法解析时为 0）。
func countMissingLibraries(warnings []ArgumentWarning) int {
	for _, warning := range warnings {
		if warning.Code != WarningCodeLibrariesMissing {
			continue
		}
		if count, ok := parseMissingLibraryCount(warning.Message); ok {
			return count
		}
	}
	return 0
}

// buildPlanDiagnostics 生成可复制的诊断信息文本（D-17）。
//
// 内容边界（D-17 明确）：
//   - 只含脱敏后的参数摘要、warnings、版本 id / mainClass / classpath 计数、
//     Java 展示名与大版本；
//   - **不含**版本 JSON 原文、libraries 全量清单、未脱敏路径与凭证。
//
// 二次脱敏：文本里所有取自预览 DTO 的行都会再走一次 `RedactForDisplay`（P2-07），
// 保证即使将来某个字段漏了脱敏，复制出去的文本也不会带凭证。
//
// 参数 preview 为已脱敏的预览数据；返回多行诊断文本。
func buildPlanDiagnostics(preview LaunchPlanPreview) string {
	var builder strings.Builder

	builder.WriteString(diagnosticsTitle)
	builder.WriteString("\n")

	// 版本与主类。
	builder.WriteString(fmt.Sprintf("版本 id：%s（%s）\n", orUnavailable(preview.VersionID), orUnavailable(preview.VersionType)))
	builder.WriteString(fmt.Sprintf("主类：%s\n", orUnavailable(preview.MainClass)))
	builder.WriteString(fmt.Sprintf("classpath：%d 条（缺失 %d 条）\n", preview.ClassPathEntryCount, preview.ClassPathMissingCount))

	// Java 展示名与大版本（不含完整路径）。
	if preview.JavaMajorVersion > 0 {
		builder.WriteString(fmt.Sprintf("Java：%s\n", orUnavailable(preview.JavaDisplay)))
	} else {
		builder.WriteString(fmt.Sprintf("Java：%s\n", diagnosticsUnavailable))
	}

	// 参数摘要：逐项列出，保持数组形态（不拼成命令行）。
	builder.WriteString(fmt.Sprintf("JVM 参数（%d 项）：\n", len(preview.JVMMajorArgs)))
	writeDiagnosticsArguments(&builder, preview.JVMMajorArgs)
	builder.WriteString(fmt.Sprintf("游戏参数（%d 项）：\n", len(preview.GameArguments)))
	writeDiagnosticsArguments(&builder, preview.GameArguments)

	// 警告：只列 code / severity / message（message 已脱敏）。
	builder.WriteString(fmt.Sprintf("警告（%d 条）：\n", len(preview.Warnings)))
	if len(preview.Warnings) == 0 {
		builder.WriteString(diagnosticsLineIndent + diagnosticsUnavailable + "\n")
	}
	for _, warning := range preview.Warnings {
		builder.WriteString(fmt.Sprintf("%s[%s] %s：%s\n",
			diagnosticsLineIndent, warning.Severity, warning.Code, warning.Message))
	}

	// 二次脱敏：逐行再走一次 P2-07 的展示脱敏函数。
	lines := strings.Split(strings.TrimRight(builder.String(), "\n"), "\n")
	for index, line := range lines {
		lines[index] = RedactForDisplay(line)
	}
	return strings.Join(lines, "\n")
}

// writeDiagnosticsArguments 把参数摘要写入诊断文本。
//
// 参数 builder 为目标缓冲；parts 为已脱敏的参数数组。
// 每行格式为 `  - <值>（<来源>）`，值超长时已在摘要阶段截断。
func writeDiagnosticsArguments(builder *strings.Builder, parts []PreviewArgumentPart) {
	if len(parts) == 0 {
		builder.WriteString(diagnosticsLineIndent + diagnosticsUnavailable + "\n")
		return
	}
	for _, part := range parts {
		if part.Source == "" {
			builder.WriteString(diagnosticsLineIndent + part.Value + "\n")
			continue
		}
		builder.WriteString(fmt.Sprintf("%s%s（%s）\n", diagnosticsLineIndent, part.Value, part.Source))
	}
}

// orUnavailable 在字段为空时返回占位说明。
//
// 参数 value 为待展示字段；返回原值或占位说明。
func orUnavailable(value string) string {
	if strings.TrimSpace(value) == "" {
		return diagnosticsUnavailable
	}
	return value
}
