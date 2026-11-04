// 本文件实现 LaunchService 的 Wails 服务壳。
//
// 为什么服务类型放在 internal/launch 而不是 package main：
// 与 P1-04 的设置服务同理——`internal/app/bootstrap.go` 是本卡唯一允许追加注册的位置，
// 而它无法引用 `package main` 中的类型。因此服务类型必须定义在 `internal/launch` 内。
//
// 绑定发现性：wails3 的绑定生成器扫描**所有被加载的包**中 `application.NewService`
// 的调用点（见 wails v3.0.0 `internal/generator/analyse.go` 的 FindServices），
// 因此这里的调用点同样会生成前端绑定。
//
// 本卡只提供两个方法（P2-01 卡面第 6 条）：
//   - `ValidateJavaPath`：接收路径并做三层校验；
//   - `PickJavaExecutable`：弹原生文件对话框返回选中路径（用户取消返回空串、不报错）。
//
// 后续 P2 卡（目录校验、版本解析、参数构建、进程启动）在此追加方法，
// 不重排既有方法与 DTO。
package launch

import (
	"runtime"
	"strings"
	"sync"

	"example.com/ninevoxel/launcher/internal/logging"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// 服务与装配相关的包级状态。
//
// 与 P1-04 设置服务相同的两种衔接需求：
//   - 服务在 `Options()` 阶段构造（此时应用实例还不存在）；
//   - 对话框需要应用实例（`app.Dialog`）。
//
// 因此用一个包级变量保存应用实例，供 `PickJavaExecutable` 使用；
// 未绑定时该变量为 nil，方法走降级分支而不是 panic。
var (
	// sharedApp 为应用实例，由 internal/app 的启动钩子绑定。
	sharedApp *application.App

	// sharedAppMu 保护 sharedApp：绑定发生在启动阶段，读取发生在服务调用时。
	sharedAppMu sync.RWMutex

	// launchLogger 是 component=launch 的日志实例，惰性初始化。
	launchLoggerOnce sync.Once
	launchLogger     *logging.Logger
)

// LaunchService 是 Minecraft 本地启动子系统的 Wails 服务。
//
// 对外只暴露卡面授权的两个方法；其余辅助能力（归一化、校验、解析）
// 都是包级函数或未导出方法，不会被映射成前端绑定。
type LaunchService struct{}

// newLaunchService 创建服务实例。
//
// 无内部状态：日志与对话框都通过包级衔接变量获取，
// 因此服务可以在 `Options()` 阶段安全构造。
func newLaunchService() *LaunchService {
	return &LaunchService{}
}

// NewWailsService 创建并注册 LaunchService。
//
// 返回值可直接加入 `application.Options.Services`；
// 由 `internal/app` 的 `RegisterLaunchService` 追加到服务列表。
func NewWailsService() application.Service {
	return application.NewService(&LaunchService{})
}

// BindAppToLaunchService 把应用实例绑定给 launch 包，供对话框与日志使用。
//
// 必须在 `application.New` 之后调用（由 internal/app 的启动钩子驱动）；
// 参数 wailsApp 为 nil 时视为解绑，`PickJavaExecutable` 会走降级分支。
func BindAppToLaunchService(wailsApp *application.App) {
	sharedAppMu.Lock()
	sharedApp = wailsApp
	sharedAppMu.Unlock()
}

// appInstance 返回当前绑定的应用实例，未绑定时为 nil。
//
// 统一读取路径保证所有调用点都持有读锁。
func appInstance() *application.App {
	sharedAppMu.RLock()
	defer sharedAppMu.RUnlock()
	return sharedApp
}

// logger 返回 component=launch 的日志实例。
//
// 惰性初始化并复用 `logging.Default`（P1-05 交付的进程级写入器），
// 因此不需要在装配阶段额外构造，也不会出现两个日志文件句柄。
func logger() *logging.Logger {
	launchLoggerOnce.Do(func() {
		launchLogger = logging.Default.WithComponent("launch")
	})
	return launchLogger
}

// ValidateJavaPath 接收用户提供的 Java 路径并完成三层校验。
//
// 参数 req 为请求（含 `javaPath`）。
// 返回结构化结果：无论成功或失败都返回同一种 DTO，失败时 `Valid=false` 且带错误码。
// 本方法不返回 error：校验失败是**业务结果**而不是调用异常，
// 让前端只需处理一种返回形态（卡面第 5 条要求结构化结果）。
//
// 日志（卡面第 7 条）：成功记一条 info，失败记一条 warn；
// `attributes` 只放非敏感键（可执行文件名、主版本号、错误码），
// **不放完整路径、不放输出全文**。
func (s *LaunchService) ValidateJavaPath(req ValidateJavaRequest) (JavaValidationResult, error) {
	result := validateJavaPath(req.JavaPath)

	// D-16：把本次校验结果记入缓存，供 `PreviewLaunchPlan` 复用。
	// 预览禁止再执行 `java -version` 探针，只能取这份「最近一次校验」的结论。
	rememberJavaValidation(result)

	if result.Valid {
		logger().Info("Java 路径校验通过", logging.Fields{
			Event:     "launch.java.validated",
			ErrorCode: "",
			Attributes: map[string]string{
				// 只放非敏感键：文件名与主版本号足以支撑排查，且不含路径。
				"javaExecutableName": result.ExecutableName,
				"javaMajorVersion":   versionAttribute(result.MajorVersion),
				"warningCount":       countAttribute(len(result.Warnings)),
			},
		})
		return result, nil
	}

	logger().Warn("Java 路径校验失败", logging.Fields{
		Event:     "launch.java.rejected",
		ErrorCode: result.ErrorCode,
		Attributes: map[string]string{
			// 失败时也只用文件名与前两级信息之外的**非敏感键**；
			// 刻意不记录 javaDisplay（它含末两级目录名，无需写进日志）。
			"javaExecutableName": result.ExecutableName,
			"javaMajorVersion":   versionAttribute(result.MajorVersion),
			"warningCount":       countAttribute(len(result.Warnings)),
		},
	})

	return result, nil
}

// PickJavaExecutable 弹出系统原生文件对话框，让用户选择 Java 可执行文件。
//
// 返回 `PickJavaResult`：
//   - 用户选中：`JavaPath` 为完整路径（允许返回给前端一次，供其立即传给
//     `ValidateJavaPath`）、`JavaDisplay` 为脱敏展示形态；
//   - **用户取消**：`Canceled=true`、路径为空串，**不返回错误**
//     （决策记录第四节的硬性降级要求：取消不得弹错误提示）；
//   - 应用实例未绑定：返回明确错误码与中文说明，**不 panic**（优雅降级）。
//
// 对话框必须 `AttachToWindow(主窗口)`：避免非模态窗口抢焦点。
func (s *LaunchService) PickJavaExecutable() (PickJavaResult, error) {
	wailsApp := appInstance()
	if wailsApp == nil {
		// 优雅降级：没有应用实例时不能调用 Dialog，否则会空指针。
		// 返回明确错误码让前端提示「请手动填写」，而不是崩溃或静默。
		logger().Warn("文件选择对话框不可用：应用实例未就绪", logging.Fields{
			Event:     "launch.java.picker.unavailable",
			ErrorCode: ErrorCodePathInvalid,
		})
		return PickJavaResult{
			ErrorCode:    ErrorCodePathInvalid,
			ErrorMessage: messageDialogUnavailable,
		}, nil
	}

	// Windows 的 Java 可执行文件有 .exe 扩展名；macOS/Linux 的 java 通常没有扩展名，
	// 不能沿用 .exe 过滤器，否则用户根本选不到目标文件。
	options := &application.OpenFileDialogOptions{
		CanChooseFiles:       true,
		CanChooseDirectories: false,
		Title:                dialogTitleJavaExecutable,
	}
	if runtime.GOOS == "windows" {
		options.Filters = []application.FileFilter{
			{DisplayName: filterNameJavaExecutable, Pattern: filterPatternJavaExecutable},
		}
	}
	dialog := wailsApp.Dialog.OpenFileWithOptions(options)

	// 附着到主窗口：避免对话框与主窗口的模态/焦点关系不正确。
	if window := wailsApp.Window.Current(); window != nil {
		dialog.AttachToWindow(window)
	}

	selectedPath, err := dialog.PromptForSingleSelection()
	if err != nil {
		// 对话框本身报错（例如系统拒绝）。同样走降级：提示用户手动输入。
		logger().Warn("文件选择对话框调用失败", logging.Fields{
			Event:     "launch.java.picker.failed",
			ErrorCode: ErrorCodePathInvalid,
		})
		return PickJavaResult{
			ErrorCode:    ErrorCodePathInvalid,
			ErrorMessage: messageDialogFailed,
		}, nil
	}

	// 用户取消：返回空串是正常语义，不当作错误。
	// 这里刻意不记 warn 日志（用户主动取消不是问题），避免噪音。
	if selectedPath == "" {
		return PickJavaResult{Canceled: true}, nil
	}

	return PickJavaResult{
		// 完整路径允许返回给前端一次（决策记录第六节边界）。
		JavaPath: selectedPath,
		// 脱敏形态供界面直接展示，前端不应使用 JavaPath 渲染。
		JavaDisplay: buildJavaDisplay(selectedPath),
	}, nil
}

// versionAttribute 把主版本号转为日志属性字符串。
//
// 未解析出主版本（0）时返回空串：日志里出现 `"0"` 会让人误以为「版本是 0」。
func versionAttribute(majorVersion int) string {
	if majorVersion <= 0 {
		return ""
	}
	return itoa(majorVersion)
}

// countAttribute 把计数转为日志属性字符串。
func countAttribute(count int) string {
	if count <= 0 {
		return "0"
	}
	return itoa(count)
}

// itoa 是 strconv.Itoa 的最小替代，避免为两处调用引入额外 import。
//
// 只处理非负数：调用方（版本号、计数）都已保证非负。
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append(digits, byte('0'+value%10))
		value /= 10
	}
	// 逆序输出。
	for left, right := 0, len(digits)-1; left < right; left, right = left+1, right-1 {
		digits[left], digits[right] = digits[right], digits[left]
	}
	return string(digits)
}

// ===== P2-02 游戏目录检查（本卡追加，不修改上方 P2-01 方法）=====

// CheckGameDirectory 以**只读**方式检查游戏根目录结构。
//
// 参数 req 为请求（含 `gameDirectory`）。
// 返回结构化结果：无论成功或失败都返回同一种 DTO。
//
// 语义要点（P2-02 卡面第 6 条）：
//   - 目录不存在 / 不是目录 / 路径非法 → `valid=false` 且带 `PATH_INVALID_001`；
//   - **目录存在但结构缺失** → `valid=false` 但 **`errorCode` 为空**，
//     通过 `launchableHint` 告诉用户缺什么（用户可能只是选错了层级，
//     弹「失败」会把人劝退）；
//   - 全程只读：不建目录、不写文件、不读任何版本 JSON。
//
// 日志（卡面第 9 条）：成功与失败各记一条 `component=launch` 摘要，
// `attributes` 只放 `versionsCount` 与各检查项的状态位，
// **不记录完整路径**。
func (s *LaunchService) CheckGameDirectory(req GameDirectoryRequest) (GameDirectoryCheckResult, error) {
	result := checkGameDirectory(req.GameDirectory)

	if result.ErrorCode != "" {
		logger().Warn("游戏目录检查失败", logging.Fields{
			Event:     "launch.gamedir.rejected",
			ErrorCode: result.ErrorCode,
			Attributes: map[string]string{
				// 只放状态位与计数，不放路径。
				"versionsCount": countAttribute(result.VersionsCount),
			},
		})
		return result, nil
	}

	logger().Info("游戏目录检查完成", logging.Fields{
		Event: "launch.gamedir.checked",
		Attributes: map[string]string{
			// 只放计数与三态状态位：这些足以支撑排查，且不含路径。
			"versionsCount": countAttribute(result.VersionsCount),
			"valid":         boolAttribute(result.Valid),
			"checks":        checksSummaryAttribute(result.Checks),
		},
	})

	return result, nil
}

// PickGameDirectory 弹出系统原生目录选择对话框，让用户选择游戏目录。
//
// 返回语义与 `PickJavaExecutable` 一致：
//   - 用户选中：`GameDirectory` 为完整路径（供立即回传 `CheckGameDirectory`）、
//     `GameRootDisplay` 为脱敏展示形态；
//   - **用户取消**：`Canceled=true`、路径为空串，**不返回错误**；
//   - 应用实例未绑定：返回明确错误码与中文说明，**不 panic**。
//
// 对话框配置按决策记录 C-8 / Q-4：允许选目录、不允许选文件，
// 即 `CanChooseDirectories: true` 且 `CanChooseFiles: false`。
func (s *LaunchService) PickGameDirectory() (PickGameDirectoryResult, error) {
	wailsApp := appInstance()
	if wailsApp == nil {
		logger().Warn("目录选择对话框不可用：应用实例未就绪", logging.Fields{
			Event:     "launch.gamedir.picker.unavailable",
			ErrorCode: ErrorCodePathInvalid,
		})
		return PickGameDirectoryResult{
			ErrorCode:    ErrorCodePathInvalid,
			ErrorMessage: messageGameDirDialogUnavailable,
		}, nil
	}

	dialog := wailsApp.Dialog.OpenFileWithOptions(&application.OpenFileDialogOptions{
		// 目录选择：只允许选目录，不允许选文件。
		CanChooseFiles:       false,
		CanChooseDirectories: true,
		Title:                dialogTitleGameDirectory,
	})

	if window := wailsApp.Window.Current(); window != nil {
		dialog.AttachToWindow(window)
	}

	selectedPath, err := dialog.PromptForSingleSelection()
	if err != nil {
		logger().Warn("目录选择对话框调用失败", logging.Fields{
			Event:     "launch.gamedir.picker.failed",
			ErrorCode: ErrorCodePathInvalid,
		})
		return PickGameDirectoryResult{
			ErrorCode:    ErrorCodePathInvalid,
			ErrorMessage: messageGameDirDialogFailed,
		}, nil
	}

	// 用户取消：正常语义，不当作错误，也不记 warn（避免噪音）。
	if selectedPath == "" {
		return PickGameDirectoryResult{Canceled: true}, nil
	}

	return PickGameDirectoryResult{
		// 完整路径允许返回给前端一次（决策记录第六节边界）。
		GameDirectory: selectedPath,
		// 脱敏形态供界面展示，前端不应使用 GameDirectory 渲染。
		GameRootDisplay: buildGameRootDisplay(selectedPath),
	}, nil
}

// boolAttribute 把布尔值转为日志属性字符串。
func boolAttribute(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// checksSummaryAttribute 把检查项状态压缩成日志属性。
//
// 格式为 `versions=present;libraries=missing;...`：
// 单条属性就能看出全部状态位，便于排查且不含任何路径。
//
// 参数 checks 为结构检查结果列表。
func checksSummaryAttribute(checks []GameDirectoryCheckItem) string {
	parts := make([]string, 0, len(checks))
	for _, item := range checks {
		parts = append(parts, item.Name+"="+item.Status)
	}
	return strings.Join(parts, ";")
}

// ===== P2-03 版本识别（本卡追加，不修改上方 P2-01 / P2-02 方法）=====

// ListLocalVersions 列举游戏根目录下的本地版本（**纯读取**）。
//
// 参数 req 为请求（含 `gameDirectory`）。
// 返回版本摘要列表 + 目录级错误码 + 被跳过的非法目录名。
//
// 行为要点（P2-03 卡面第 1 条）：
//   - 只读 `versions/` 的**一层**子目录，不递归、不跟随符号链接目录；
//   - 非法 id（字符集不符、超长、`.`/`..`）被跳过并记录在 `skippedIds`；
//   - 每个候选版本都会尝试读取其 `<id>.json`，据此给出三态判定。
//
// 日志：成功与失败各记一条 `component=launch` 摘要，
// `attributes` 只放版本数量与状态计数，**不记录路径、不记录版本 JSON 内容**。
func (s *LaunchService) ListLocalVersions(req VersionListRequest) (VersionListResult, error) {
	result := listLocalVersions(req.GameDirectory)

	if result.ErrorCode != "" {
		logger().Warn("列出版本失败", logging.Fields{
			Event:     "launch.versions.listing.failed",
			ErrorCode: result.ErrorCode,
		})
		return result, nil
	}

	logger().Info("列出版本完成", logging.Fields{
		Event: "launch.versions.listed",
		Attributes: map[string]string{
			"versionCount":    countAttribute(len(result.Versions)),
			"skippedCount":    countAttribute(len(result.SkippedIDs)),
			"launchSupported": countAttribute(countVersionsByStatus(result.Versions, versionStatusLaunchSupported)),
			"metadataOnly":    countAttribute(countVersionsByStatus(result.Versions, versionStatusMetadataOnly)),
			"brokenCount":     countAttribute(countVersionsByStatus(result.Versions, versionStatusBroken)),
		},
	})

	return result, nil
}

// CheckLocalVersion 检查单个本地版本能否启动（**纯读取**）。
//
// 参数 req 为请求（含 `gameDirectory` 与 `versionId`）。
// 返回详细检查结果：mainClass、客户端 JAR 存在性、库计数与缺失清单、
// natives 存在性、所需 Java 主版本与警告。
//
// 错误语义（卡面「错误语义」段）：
//   - JSON 无法解析 / 缺关键字段 / 继承环 → `VERSION_METADATA_001`；
//   - 版本目录不存在 → `GAME_DIR_001`；
//   - **缺库 / 缺 jar / 缺 natives 不是错误**，是 `metadata_only` 的业务结论
//     （此时 `errorCode` 为空，只有 `status` 与 `reason`）。
func (s *LaunchService) CheckLocalVersion(req VersionCheckRequest) (VersionCheckResult, error) {
	result := checkVersionWithRoot(req.GameDirectory, req.VersionID)

	// 只有真正的元数据/路径错误才记 warn；结构缺失属于正常业务结论，记 info。
	if result.ErrorCode != "" {
		logger().Warn("版本检查失败", logging.Fields{
			Event:     "launch.version.check.failed",
			ErrorCode: result.ErrorCode,
			Attributes: map[string]string{
				"status": result.Status,
			},
		})
		return result, nil
	}

	logger().Info("版本检查完成", logging.Fields{
		Event: "launch.version.checked",
		Attributes: map[string]string{
			// 只放状态与计数：不含版本 id 之外的任何路径信息。
			"status":            result.Status,
			"clientJarPresent":  boolAttribute(result.ClientJarPresent),
			"librariesRequired": countAttribute(result.LibrariesRequired),
			"librariesPresent":  countAttribute(result.LibrariesPresent),
			"librariesMissing":  countAttribute(len(result.LibrariesMissing)),
			"javaMajorVersion":  versionAttribute(result.JavaMajorVersionRequired),
			"slow":              boolAttribute(result.Slow),
		},
	})

	return result, nil
}

// countVersionsByStatus 统计指定状态的版本数量。
//
// 参数 versions 为版本摘要列表；status 为目标状态值。
// 用于日志摘要，不参与业务判定。
func countVersionsByStatus(versions []VersionSummary, status string) int {
	count := 0
	for _, item := range versions {
		if item.Status == status {
			count++
		}
	}
	return count
}

// ===== P2-04 参数建模（本卡追加，不修改上方 P2-01/02/03 方法）=====

// PreviewLaunchPlan 构建但**不启动**游戏，返回脱敏后的启动计划预览。
//
// 参数 req 为请求（游戏目录、版本 id、可选附加参数）。
// 返回的 `LaunchPlanPreview` **不含任何未脱敏绝对路径**（D-8 + 决策记录第六节的硬性要求）。
//
// 关键语义（P2-04 卡面）：
//   - classpath / mainClass / JVM 参数 / 游戏参数**四路分离**，每项带来源标注；
//   - 占位符能解析的写入参数，不能解析的进 `unresolvedPlaceholders` 并置
//     `safeToLaunch=false`（不静默丢弃、不替换为空串）；
//   - natives 目录只检查存在性，缺失时给 blocking 警告，**不创建、不解压**（D-10）；
//   - 附加参数逐项校验，试图覆盖离线身份三项或安全必需参数时返回 `LAUNCH_ARGS_001`。
//
// 日志：成功与失败各记一条 `component=launch` 摘要，
// `attributes` 只放计数与布尔位，**不记录任何路径、不记录参数内容**。
func (s *LaunchService) PreviewLaunchPlan(req LaunchPlanPreviewRequest) (LaunchPlanPreview, error) {
	// 复用 P2-09 的编排函数：它内部走的仍是 P2-04 的 buildLaunchPlan，
	// 与启动路径共用同一份构建逻辑（不另起一套「预览专用」参数拼装）。
	preview := previewLaunchPlanForUser(req.GameDirectory, req.VersionID, req.AdditionalJvmArgs, req.AdditionalGameArgs)

	// 本方法**不启动进程、不写文件**：预览是纯只读计算（D-16），
	// 日志只记录「已生成预览，warnings 数 N」这类计数，绝不记录路径或参数内容。
	if preview.BuildFailed {
		logger().Warn("启动计划预览构建失败", logging.Fields{
			Event:     "launch.plan.preview.failed",
			ErrorCode: preview.BuildErrorCode,
			Attributes: map[string]string{
				"warningCount": countAttribute(len(preview.Warnings)),
			},
		})
		return preview, nil
	}

	if !preview.SafeToLaunch {
		// 取第一条 blocking 警告的错误码作为摘要（预览本身不返回 error）。
		code := ""
		if len(preview.BlockingWarnings) > 0 {
			code = preview.BlockingWarnings[0].Code
		}
		logger().Warn("启动计划预览不可安全启动", logging.Fields{
			Event:     "launch.plan.preview.unsafe",
			ErrorCode: code,
			Attributes: map[string]string{
				"classPathEntryCount":        countAttribute(preview.ClassPathEntryCount),
				"jvmArgCount":                countAttribute(len(preview.JVMMajorArgs)),
				"gameArgCount":               countAttribute(len(preview.GameArguments)),
				"unresolvedPlaceholderCount": countAttribute(len(preview.UnresolvedPlaceholders)),
				"warningCount":               countAttribute(len(preview.Warnings)),
				"blockingWarningCount":       countAttribute(len(preview.BlockingWarnings)),
			},
		})
		return preview, nil
	}

	logger().Info("已生成启动计划预览", logging.Fields{
		Event: "launch.plan.previewed",
		Attributes: map[string]string{
			// 只放计数与脱敏后的类别信息，不放路径、不放参数内容。
			"classPathEntryCount": countAttribute(preview.ClassPathEntryCount),
			"jvmArgCount":         countAttribute(len(preview.JVMMajorArgs)),
			"gameArgCount":        countAttribute(len(preview.GameArguments)),
			"warningCount":        countAttribute(len(preview.Warnings)),
		},
	})

	return preview, nil
}

// ===== P2-07 游戏日志通道（本卡追加，不修改上方既有方法）=====

// GetGameLog 读取游戏日志的一页快照（**事件丢失后的恢复入口**）。
//
// 参数 req 为查询条件（`launchId` / `beforeMs` / `limit`）。
//
// 这是与增量事件并列的**第二种读取方式**：控制台在收到 `launch.log.appended.v1`
// 后做增量追加，但在以下情形必须用本方法重建视图：
//   - 页面首次打开（事件是增量，拿不到历史）；
//   - 事件丢失或前端重连；
//   - 用户点击「刷新」。
//
// 因此本方法不是可选的「补充」，而是「事件不可靠」这一前提下唯一可信的数据源。
//
// 日志行已由通道在采集时脱敏（`RedactForDisplay`），本方法不再二次处理。
func (s *LaunchService) GetGameLog(req GameLogQueryRequest) (GameLogQueryResult, error) {
	snapshot, queryErr := queryGameLog(req.LaunchID, req.BeforeMs, req.Limit)

	result := GameLogQueryResult{
		// 初始化为空切片而不是 nil：JSON 序列化为 `[]` 而不是 `null`。
		Lines: []GameLogLineResult{},
	}

	if queryErr != nil {
		result.ErrorCode = queryErr.Code
		result.ErrorMessage = queryErr.Message
		logger().Warn("读取游戏日志失败", logging.Fields{
			Event:     "launch.gamelog.read.failed",
			ErrorCode: queryErr.Code,
		})
		return result, nil
	}

	for _, line := range snapshot.lines {
		result.Lines = append(result.Lines, GameLogLineResult{
			Sequence:    line.Sequence,
			TimestampMs: line.TimestampMs,
			Stream:      line.Stream,
			Text:        line.Text,
		})
	}
	result.NextBeforeMs = snapshot.nextBeforeMs
	result.DroppedLines = snapshot.droppedLines
	result.HasMore = snapshot.hasMore

	logger().Info("读取游戏日志完成", logging.Fields{
		Event: "launch.gamelog.read",
		Attributes: map[string]string{
			// 只放计数与布尔位，**不放任何日志文本**。
			"lineCount":    countAttribute(len(result.Lines)),
			"droppedLines": countAttribute(result.DroppedLines),
			"hasMore":      boolAttribute(result.HasMore),
		},
	})

	return result, nil
}

// ===== P2-06 启动生命周期（本卡追加，不修改上方既有方法）=====

// StartLaunch 启动一个本地 Minecraft 进程。
//
// 参数 req 为启动请求（**只接受结构化参数，不接受任意命令行**）。
// 返回受理结果（launchId、受理时间、初始状态）。
//
// 行为要点：
//   - **单活动进程**（D-9）：已有活动启动时返回 `LAUNCH_STATE_001`，不排队；
//   - 启动前先建日志通道（P2-05 的 `dispatchGameOutput` 依赖它，
//     无通道会静默丢弃全部输出），通道不可用时中止并返回 `LAUNCH_LOG_001`；
//   - 启动失败时记录置为 `failed` 但不占用活动位，用户可直接重试。
//
// 日志：只记版本 id 与状态，**不记录命令行、不记录路径**。
func (s *LaunchService) StartLaunch(req StartLaunchRequest) (LaunchAcceptedResult, error) {
	accepted, launchErr := StartLaunchProcess(req)
	if launchErr != nil {
		attributes := map[string]string{
			"versionId": req.VersionID,
		}
		if req.InstanceID != "" {
			attributes["instanceId"] = req.InstanceID
		}
		logger().Warn("启动请求被拒绝", logging.Fields{
			Event:      "launch.start.rejected",
			ErrorCode:  launchErr.Code,
			Attributes: attributes,
		})
		return LaunchAcceptedResult{}, launchErr
	}

	attributes := map[string]string{
		"versionId": req.VersionID,
		"state":     accepted.State,
	}
	if req.InstanceID != "" {
		attributes["instanceId"] = req.InstanceID
	}
	logger().Info("启动受理成功", logging.Fields{
		Event:      "launch.start.accepted",
		Attributes: attributes,
	})

	return accepted, nil
}

// StopLaunch 请求停止由本启动器托管的进程。
//
// 参数 req 为停止请求（`launchId` 可空表示当前活动启动；`force` 表示直接强制）。
// 返回停止后的状态快照。
//
// **幂等**：重复调用不重复终止——已在 `stopping` 时直接返回当前快照。
//
// **风险必须明示**（D-3）：`force=true` 时 Windows 的 `Kill()` 不执行 JVM 关闭钩子，
// Minecraft 可能来不及保存世界数据。UI 必须在二次确认后才传 `force=true`。
//
// 注意：本方法返回的是**停止中**的快照，不是「已退出」——UI 点击 ≠ 进程已退出
// （D-6 的 `stopping` 状态就是为此存在）。
func (s *LaunchService) StopLaunch(req StopLaunchRequest) (LaunchStatusResult, error) {
	snapshot, launchErr := StopLaunchProcess(req)
	if launchErr != nil {
		logger().Warn("停止请求被拒绝", logging.Fields{
			Event:     "launch.stop.rejected",
			ErrorCode: launchErr.Code,
			Attributes: map[string]string{
				"launchId": req.LaunchID,
				"force":    boolAttribute(req.Force),
			},
		})
		return LaunchStatusResult{}, launchErr
	}

	logger().Info("停止请求已受理", logging.Fields{
		Event: "launch.stop.accepted",
		Attributes: map[string]string{
			"launchId":   snapshot.LaunchID,
			"state":      snapshot.State,
			"force":      boolAttribute(req.Force),
			"stopResult": snapshot.StopResult,
		},
	})

	return snapshot, nil
}

// GetLaunchStatus 返回指定启动的当前状态快照，供事件丢失后恢复界面。
//
// 参数 req 为查询请求；`launchId` 为空时返回当前活动启动（无活动时返回最近的终态记录）。
//
// **本方法是唯一真相源**（卡面第 9 条）：事件可能丢失或重复，
// 前端必须以本方法的返回值重建界面，不得把事件当作唯一数据来源。
// 快照过期清理（5 分钟）也在这里显式执行。
//
// 查询失败返回带 `LAUNCH_STATE_001` 的快照（**不返回 error**），
// 这样前端只需处理一种返回形态。
func (s *LaunchService) GetLaunchStatus(req LaunchStatusRequest) (LaunchStatusResult, error) {
	snapshot, launchErr := GetLaunchStatusSnapshot(req)
	if launchErr != nil {
		result := LaunchStatusResult{
			LaunchID:     req.LaunchID,
			ErrorCode:    launchErr.Code,
			ErrorMessage: launchErr.Message,
		}
		logger().Warn("状态查询失败", logging.Fields{
			Event:     "launch.status.failed",
			ErrorCode: launchErr.Code,
		})
		return result, nil
	}

	logger().Info("状态查询完成", logging.Fields{
		Event: "launch.status.read",
		Attributes: map[string]string{
			"launchId": snapshot.LaunchID,
			"state":    snapshot.State,
			"outcome":  snapshot.Outcome,
		},
	})

	return snapshot, nil
}

// ===== P2-13 启动配置持久化（本卡追加，不修改上方既有方法与 DTO）=====

// SaveLaunchProfile 保存当前「Java + 游戏目录 + 版本 id」组合为一条启动配置。
//
// 参数 req 为保存请求（含绝对路径；路径只入库，不进入返回值）。
// 返回值是脱敏后的配置摘要；失败时返回 `LAUNCH_PROFILE_001` 的错误。
//
// 日志：只记版本 id 与错误码，**不记录命令行、不记录路径**。
func (s *LaunchService) SaveLaunchProfile(req SaveLaunchProfileRequest) (LaunchProfileSummary, error) {
	summary, profileErr := SaveLaunchProfile(req)
	if profileErr != nil {
		logger().Warn("启动配置保存失败", logging.Fields{
			Event:     "launch.profile.save.failed",
			ErrorCode: profileErr.Code,
			Attributes: map[string]string{
				"versionId": req.VersionID,
			},
		})
		return LaunchProfileSummary{}, profileErr
	}

	return *summary, nil
}

// ListLaunchProfiles 返回全部启动配置摘要（按最近使用时间倒序）。
//
// 返回值只含脱敏展示串，绝不含绝对路径。
func (s *LaunchService) ListLaunchProfiles() ([]LaunchProfileSummary, error) {
	summaries, profileErr := ListLaunchProfiles()
	if profileErr != nil {
		logger().Warn("启动配置列表读取失败", logging.Fields{
			Event:     "launch.profile.list.failed",
			ErrorCode: profileErr.Code,
		})
		return nil, profileErr
	}

	// 只记条数：列表内容属于用户配置，不进日志。
	logger().Info("启动配置列表读取完成", logging.Fields{
		Event: "launch.profile.listed",
		Attributes: map[string]string{
			"count": itoa(len(summaries)),
		},
	})

	return summaries, nil
}

// GetMostRecentLaunchProfile 返回最近使用的一条启动配置摘要。
//
// 行为：
//   - 没有任何配置时返回零值摘要与 nil：这是首次使用的正常状态，不是错误；
//   - 配置已失效（Java 文件或游戏目录不存在）时返回 `LAUNCH_PROFILE_001`，
//     前端据此如实提示「上次的 Java/目录已不可用，请重新选择」；
//   - **恢复过程不启动任何子进程**，校验只用 os.Stat。
func (s *LaunchService) GetMostRecentLaunchProfile() (LaunchProfileSummary, error) {
	summary, profileErr := GetMostRecentLaunchProfile()
	if profileErr != nil {
		logger().Warn("启动配置恢复失败", logging.Fields{
			Event:     "launch.profile.stale",
			ErrorCode: profileErr.Code,
		})
		return LaunchProfileSummary{}, profileErr
	}
	if summary == nil {
		// 首次使用：不算失败，也不写日志噪音。
		return LaunchProfileSummary{}, nil
	}

	logger().Info("启动配置恢复完成", logging.Fields{
		Event: "launch.profile.restored",
		Attributes: map[string]string{
			"versionId": summary.VersionID,
		},
	})

	return *summary, nil
}

// DeleteLaunchProfile 删除指定的启动配置。
//
// 幂等：配置不存在时也算成功，避免用户重复删除时报错。
func (s *LaunchService) DeleteLaunchProfile(req DeleteLaunchProfileRequest) (LaunchProfileDeletedResult, error) {
	if deleteErr := DeleteLaunchProfile(req.ID); deleteErr != nil {
		logger().Warn("启动配置删除失败", logging.Fields{
			Event:     "launch.profile.delete.failed",
			ErrorCode: deleteErr.Code,
			Attributes: map[string]string{
				"id": req.ID,
			},
		})
		return LaunchProfileDeletedResult{Deleted: false}, deleteErr
	}

	logger().Info("启动配置已删除", logging.Fields{
		Event: "launch.profile.deleted",
		Attributes: map[string]string{
			"id": req.ID,
		},
	})

	return LaunchProfileDeletedResult{Deleted: true}, nil
}
