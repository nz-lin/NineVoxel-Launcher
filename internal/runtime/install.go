// 本文件实现 Runtime 的安装编排：官方 manifest → 校验 → 逐文件下载 → 校验 →
// 事务提交到 `runtimes/<runtimeId>/` → 写 `java_runtimes`。
//
// 为什么不走 P3-04 的下载队列（D-48）：Runtime 是「单组件 manifest + 多文件」
// 的复合操作，塞进队列要求队列支持「提交后再执行异步后处理」，改动面远大于收益。
// 因此这里独立编排，但**复用** P3-05 的传输层（白名单、重定向限制、限额、续传）
// 与 P3-06 的校验原语（`VerifyFile`）。
//
// 网络点只有两处，都经过同一套来源白名单校验：
//  1. `fetchRuntimeDocument`：取 all.json 与组件 manifest（内存，限量读取）；
//  2. `download.TransferFunc`：取文件本体（写 staging）。
//
// 官方 `java-runtime` manifest 的现状是**逐文件 raw 模型**，没有 zip 归档，
// 因此 `UnpackZip` 在本卡不被主路径调用；归档模型留给 manifest 演进时使用，
// 其路径校验与逐文件模型共用 `unpackTargetPath`。
package runtime

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	goruntime "runtime"

	"example.com/ninevoxel/launcher/internal/artifact"
	"example.com/ninevoxel/launcher/internal/download"
	"example.com/ninevoxel/launcher/internal/events"
	"example.com/ninevoxel/launcher/internal/logging"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// 官方端点与限额。
const (
	// runtimeAllManifestURL 是官方 `java-runtime` 的组件索引。
	//
	// 该地址由官方固定路径给出，其中的 sha1 段对应整份 all.json 的内容版本；
	// 它本身不提供 sha1/size 元数据，因此下载后只做结构校验（见 doc.go 的口径）。
	runtimeAllManifestURL = "https://launchermeta.mojang.com/v1/products/java-runtime/2ec0cc96c44e5a76b9c8b7c39df7210883d12871/all.json"

	// runtimeManifestMaxBytes 是 all.json 的读取上限。
	runtimeManifestMaxBytes = 4 << 20
	// runtimeComponentManifestMaxBytes 是单组件 manifest 的读取上限。
	runtimeComponentManifestMaxBytes = 8 << 20
	// runtimeDocumentTimeout 是取元数据 JSON 的超时。
	runtimeDocumentTimeout = 15 * time.Second

	// runtimeMaxFiles 是安装文件数上限（与解压限额同口径）。
	runtimeMaxFiles = unpackDefaultMaxFiles
	// runtimeMaxTotalBytes 是安装总字节上限（1 GiB）。
	runtimeMaxTotalBytes = int64(1 << 30)
	// runtimeMaxFileBytes 是单文件字节上限（512 MiB）。
	runtimeMaxFileBytes = int64(512 << 20)

	// runtimeStagingPrefix 是暂存目录前缀。
	runtimeStagingPrefix = ".staging-"
	// runtimeQuarantinePrefix 是隔离目录前缀。
	runtimeQuarantinePrefix = ".quarantine-"
	// runtimeEventTaskPrefix 是进度事件的关联键前缀（D-48）。
	runtimeEventTaskPrefix = "runtime-install-"
	// runtimeProgressThrottleMs 是进度事件节流间隔（卡面要求 ≥500ms）。
	runtimeProgressThrottleMs = int64(500)

	// runtimeEventStateDownloading / Committing / Completed / Failed 是进度事件状态取值。
	runtimeEventStateDownloading  = "downloading"
	runtimeEventStateCommitting   = "committing"
	runtimeEventStateCompleted    = "completed"
	runtimeEventStateFailed       = "failed"
	runtimeFileTypeDirectory      = "directory"
	runtimeFileTypeFile           = "file"
	runtimeFileTypeLink           = "link"
	runtimeDownloadKindRaw        = "raw"
	runtimeAppVersionForTransport = "p3-09"
)

// WarningUnpackSkippedLink 是「归档里的符号链接被跳过」的告警码。
const WarningUnpackSkippedLink = "RUNTIME_LINK_SKIPPED"

// runtimeManifestDownload 是 manifest 里的一条下载记录。
type runtimeManifestDownload struct {
	// URL 为官方地址（必须 https + 白名单 host）。
	URL string `json:"url"`
	// SHA1 为内容校验值（40 位小写 hex）。
	SHA1 string `json:"sha1"`
	// Size 为字节数。
	Size int64 `json:"size"`
}

// runtimeManifestDownloads 是 manifest 条目的 downloads 容器。
type runtimeManifestDownloads struct {
	// Raw 为原始文件下载记录。
	Raw *runtimeManifestDownload `json:"raw"`
}

// runtimeManifestFile 是组件 manifest 里的单个文件条目。
type runtimeManifestFile struct {
	// Type 为 `directory` / `file` / `link`。
	Type string `json:"type"`
	// Executable 表示该文件需要可执行位（仅 POSIX 生效）。
	Executable bool `json:"executable"`
	// Downloads 为下载记录。
	Downloads runtimeManifestDownloads `json:"downloads"`
	// Target 是 link 条目的目标（本卡只记录，不创建链接）。
	Target string `json:"target"`
}

// runtimeManifestFiles 是「相对路径 → 条目」的映射。
type runtimeManifestFiles map[string]runtimeManifestFile

// runtimeManifestEntry 是 all.json 里某个组件的版本条目。
type runtimeManifestEntry struct {
	// Manifest 为组件 manifest 的位置与校验值。
	Manifest runtimeManifestDownload `json:"manifest"`
	// Version 为版本信息。
	Version struct {
		// Name 为版本名，例如 `21.0.5+11`。
		Name string `json:"name"`
		// Released 为发布日期。
		Released string `json:"released"`
	} `json:"version"`
}

// runtimeAllManifest 是 all.json 的结构：平台 → 组件 → 版本条目列表。
type runtimeAllManifest map[string]map[string][]runtimeManifestEntry

// runtimeStagingSink 把传输层写盘接到 `runtimes/.staging-<runtimeId>/`。
type runtimeStagingSink struct {
	// stagingRoot 为暂存根目录的绝对路径。
	stagingRoot string
}

// Open 打开一个 staging 写入句柄。
//
// 路径校验与逐文件模型共用 `unpackTargetPath`：传输层只负责字节，
// 「能不能写到这里」由本包判定。
func (s *runtimeStagingSink) Open(_ context.Context, task download.Task, offset int64, truncate bool) (download.StagingWriter, error) {
	target, pathErr := unpackTargetPath(s.stagingRoot, task.TargetRelPath)
	if pathErr != nil {
		return nil, pathErr
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(target), 0o755); mkdirErr != nil {
		return nil, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeStagingFailed, false)
	}
	flags := os.O_CREATE | os.O_WRONLY
	if truncate || offset == 0 {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_APPEND
	}
	file, openErr := os.OpenFile(target, flags, 0o644)
	if openErr != nil {
		return nil, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeStagingFailed, false)
	}
	return &runtimeStagingWriter{file: file}, nil
}

// runtimeStagingWriter 是 staging 写入句柄。
type runtimeStagingWriter struct {
	// file 为底层文件。
	file *os.File
}

// Write 写入一段字节。
func (w *runtimeStagingWriter) Write(p []byte) (int, error) {
	return w.file.Write(p)
}

// Flush 把缓冲内容落盘（断点续传依赖「已 flush 的字节才计入 part 大小」）。
func (w *runtimeStagingWriter) Flush() error {
	return w.file.Sync()
}

// Close 关闭句柄。
func (w *runtimeStagingWriter) Close() error {
	return w.file.Close()
}

// runtimeTransportLogger 把传输层日志接到本包日志器。
type runtimeTransportLogger struct{}

// Info 写入一条 info 日志。
func (runtimeTransportLogger) Info(message string, fields logging.Fields) {
	runtimeLog().Info(message, fields)
}

// Warn 写入一条 warn 日志。
func (runtimeTransportLogger) Warn(message string, fields logging.Fields) {
	runtimeLog().Warn(message, fields)
}

// runtimeSourceRegistry 返回官方来源白名单，并读取当前的镜像开关（D-57）。
//
// 为什么是函数而不是包级变量：包级 var 只在 init 时求值一次，
// 用户在设置里打开镜像后，已经构造好的注册表不会跟着变化——开关就等于失效。
// 改为每次使用时构造，代价只是几次 map 查找，换来的是一致的行为。
func runtimeSourceRegistry() *download.SourceRegistry {
	return download.NewSourceRegistryFromProvider()
}

// runtimeDocumentFetcherFunc 是「取元数据 JSON」的接缝，测试可替换。
type runtimeDocumentFetcherFunc func(ctx context.Context, rawURL string, maxBytes int) ([]byte, *AppError)

// runtimeDocumentFetcher 取回一份 JSON 文档（默认实现见 fetchRuntimeDocument）。
var runtimeDocumentFetcher runtimeDocumentFetcherFunc = fetchRuntimeDocument

// runtimeHTTPClientFactory 返回取元数据用的 HTTP 客户端，测试可替换。
var runtimeHTTPClientFactory = newRuntimeHTTPClient

// newRuntimeHTTPClient 构造取元数据用的客户端。
//
// 不设 Proxy（与 D-30 一致：不复用系统代理）；重定向最多 3 跳且每跳都要过白名单。
func newRuntimeHTTPClient() *http.Client {
	return &http.Client{
		Timeout: runtimeDocumentTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > 3 {
				return fmt.Errorf("runtime: too many redirects")
			}
			if request == nil || request.URL == nil {
				return fmt.Errorf("runtime: empty redirect target")
			}
			if appErr := runtimeSourceRegistry().ValidateURL(request.URL.String()); appErr != nil {
				return appErr
			}
			return nil
		},
	}
}

// fetchRuntimeDocument 下载一份 JSON 文档并施加大小上限。
//
// 这是本卡唯一的「取元数据」网络点：只允许官方白名单 host、只读内存、不落盘。
func fetchRuntimeDocument(ctx context.Context, rawURL string, maxBytes int) ([]byte, *AppError) {
	target := strings.TrimSpace(rawURL)
	if target == "" {
		return nil, newInputError(ErrorCodeRuntimeInstall, messageRuntimeSourceRejected)
	}
	if appErr := runtimeSourceRegistry().ValidateURL(target); appErr != nil {
		// 不把原始地址写进日志（可能带查询串），只记「被拒绝」。
		runtimeLog().Warn("Runtime 元数据地址被拒绝", logging.Fields{
			Event:     "runtime.manifest.rejected",
			ErrorCode: ErrorCodeRuntimeInstall,
		})
		return nil, newInputError(ErrorCodeRuntimeInstall, messageRuntimeSourceRejected)
	}
	request, buildErr := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if buildErr != nil {
		return nil, newInputError(ErrorCodeRuntimeInstall, messageRuntimeSourceRejected)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")

	client := runtimeHTTPClientFactory()
	response, doErr := client.Do(request)
	if doErr != nil {
		return nil, newExternalError(ErrorCodeRuntimeInstall, messageRuntimeManifestUnreachable, true)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return nil, newExternalError(ErrorCodeRuntimeInstall, messageRuntimeManifestUnreachable, true)
	}
	limit := int64(maxBytes) + 1
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, limit))
	if readErr != nil {
		return nil, newExternalError(ErrorCodeRuntimeInstall, messageRuntimeManifestUnreachable, true)
	}
	if len(payload) > maxBytes {
		return nil, newInputError(ErrorCodeRuntimeInstall, messageRuntimeManifestTooLarge)
	}
	return payload, nil
}

// runtimeTransferFactory 构造传输函数，测试可替换。
var runtimeTransferFactory = func(stagingRoot string) download.TransferFunc {
	transport := download.NewHTTPTransport(download.HTTPTransportOptions{
		Registry:   runtimeSourceRegistry(),
		Sink:       &runtimeStagingSink{stagingRoot: stagingRoot},
		Logger:     runtimeTransportLogger{},
		AppVersion: runtimeAppVersionForTransport,
	})
	return transport.Transfer
}

// runtimeProgressSink 是进度事件的接缝，测试可替换。
var runtimeProgressSink = emitRuntimeProgress

// runtimeEmitterOnce 保证发射器只创建一次。
var (
	runtimeEmitterOnce sync.Once
	runtimeEmitterInst *events.Emitter
)

// runtimeEmitter 返回事件发射器，并惰性绑定当前 Wails 应用。
func runtimeEmitter() *events.Emitter {
	runtimeEmitterOnce.Do(func() {
		runtimeEmitterInst = events.NewEmitter()
		if app := application.Get(); app != nil {
			runtimeEmitterInst.BindApp(app)
		}
	})
	return runtimeEmitterInst
}

// emitRuntimeProgress 发射一次安装进度事件（复用下载任务事件，D-45）。
func emitRuntimeProgress(taskID, state string, progress float64, errorCode string) {
	emitter := runtimeEmitter()
	if emitter == nil {
		return
	}
	payload := events.DownloadTaskChangedPayload{
		TaskID:    taskID,
		State:     state,
		Progress:  progress,
		ErrorCode: errorCode,
	}
	if _, emitErr := events.Emit(emitter, events.EventDownloadTaskChanged, payload); emitErr != nil {
		runtimeLog().Warn("Runtime 安装进度事件发射失败", logging.Fields{
			Event:      events.EventDownloadTaskChanged,
			ErrorCode:  "EVENT_EMIT",
			Attributes: map[string]string{"taskId": taskID},
		})
	}
}

// installProgress 维护一次安装的进度与节流状态。
//
// 进度按「已写入字节 / 总字节」计算，而不是文件计数：runtime manifest 里
// 单个文件可以相差三个数量级（几 KB 的配置 vs 上百 MB 的 jar），按文件数
// 计会让进度条长时间停在个位数，然后一下跳到 100%。
type installProgress struct {
	// taskID 为事件关联键。
	taskID string
	// totalBytes 为待下载内容的总字节数；0 表示清单未声明大小。
	totalBytes int64
	// doneBytes 为已确认写入的字节数。
	doneBytes int64
	// totalFiles 为待下载文件数，仅在 totalBytes 为 0 时用于回退计算。
	totalFiles int
	// doneFiles 为已完成文件数。
	doneFiles int
	// lastEmitMs 为上次发射时间。
	lastEmitMs int64
	// now 为时钟。
	now func() int64
}

// newInstallProgress 创建进度跟踪器。
//
// totalBytes 为待下载内容的总字节数；为 0（清单未声明大小）时回退到按文件数
// 计算，避免出现「永远 0%」。
func newInstallProgress(taskID string, totalBytes int64, totalFiles int) *installProgress {
	return &installProgress{
		taskID:     taskID,
		totalBytes: totalBytes,
		totalFiles: totalFiles,
		now: func() int64 {
			return time.Now().UnixMilli()
		},
	}
}

// addBytes 累加已下载字节并按节流规则上报一次进度。
//
// 传输过程中每读到一段就调用一次：节流在 report 内部完成，因此可以高频调用。
func (p *installProgress) addBytes(delta int64) {
	if delta <= 0 {
		return
	}
	p.doneBytes += delta
	p.report(runtimeEventStateDownloading, "")
}

// isComplete 判断按字节计的下载是否已经写满。
//
// 用于让「最后一帧 100%」绕过节流：否则最后一次 addBytes 可能被时间窗口吞掉，
// 进度条会停在 97% 之类的位置，紧接着又被 committing 阶段的 100% 覆盖。
func (p *installProgress) isComplete() bool {
	return p.totalBytes > 0 && p.doneBytes >= p.totalBytes
}

// report 按节流规则发射一次进度。
//
// 终态（committing/completed/failed）总是发射：否则一次很快的安装可能一条事件都没有，
// 界面会一直停在「准备中」。
func (p *installProgress) report(state, errorCode string) {
	nowMs := p.now()
	// 下载阶段的中间进度按时间节流；终态、阶段切换与「已下载满」不节流。
	if state == runtimeEventStateDownloading && p.doneFiles < p.totalFiles && !p.isComplete() {
		if nowMs-p.lastEmitMs < runtimeProgressThrottleMs {
			return
		}
	}
	p.lastEmitMs = nowMs

	progress := 0.0
	switch {
	case state == runtimeEventStateCompleted:
		progress = 1
	case p.totalBytes > 0:
		progress = float64(p.doneBytes) / float64(p.totalBytes)
	case p.totalFiles > 0:
		// 清单没有声明总大小时退回文件计数，至少不是死板的 0。
		progress = float64(p.doneFiles) / float64(p.totalFiles)
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	runtimeProgressSink(p.taskID, state, progress, errorCode)
}

// InstallRuntime 安装官方 Java Runtime。
//
// 流程：许可校验 → 平台映射 → 取 all.json → 取组件 manifest → 限额与磁盘预检 →
// 幂等判定 → 逐文件下载并校验 → 清单校验 → 事务提交 → 写库 → 发完成事件。
func InstallRuntime(req InstallRuntimeRequest) (download.TaskAccepted, *AppError) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	return installRuntime(ctx, req)
}

// installRuntime 是 InstallRuntime 的内部实现（测试直接调用以便注入 ctx）。
func installRuntime(ctx context.Context, req InstallRuntimeRequest) (download.TaskAccepted, *AppError) {
	if !req.AcceptLicense {
		return download.TaskAccepted{}, newInputError(ErrorCodeRuntimeInstall, messageRuntimeLicenseRequired)
	}
	component := strings.TrimSpace(req.Component)
	if component == "" {
		return download.TaskAccepted{}, newInputError(ErrorCodeRuntimeInstall, messageRuntimeComponentRequired)
	}
	platformToken, platformOK := manifestPlatformToken()
	if !platformOK {
		return download.TaskAccepted{}, newInputError(ErrorCodeRuntimeUnpack, messageRuntimePlatformUnsupported)
	}
	root, rootErr := runtimeDataRoot()
	if rootErr != nil {
		return download.TaskAccepted{}, rootErr
	}

	allPayload, allErr := runtimeDocumentFetcher(ctx, runtimeAllManifestURL, runtimeManifestMaxBytes)
	if allErr != nil {
		return download.TaskAccepted{}, allErr
	}
	all, parseErr := parseAllManifest(allPayload)
	if parseErr != nil {
		return download.TaskAccepted{}, parseErr
	}
	entry, entryOK := selectManifestEntry(all, platformToken, component)
	if !entryOK {
		return download.TaskAccepted{}, newInputError(ErrorCodeRuntimeNotFound, messageRuntimeComponentUnsupported)
	}

	componentPayload, componentErr := runtimeDocumentFetcher(ctx, entry.Manifest.URL, runtimeComponentManifestMaxBytes)
	if componentErr != nil {
		return download.TaskAccepted{}, componentErr
	}
	if verifyErr := verifyManifestDocument(componentPayload, entry.Manifest); verifyErr != nil {
		return download.TaskAccepted{}, verifyErr
	}
	files, filesErr := parseComponentManifest(componentPayload)
	if filesErr != nil {
		return download.TaskAccepted{}, filesErr
	}
	stats, statsErr := inspectManifestFiles(files)
	if statsErr != nil {
		return download.TaskAccepted{}, statsErr
	}
	// 版本名来自 all.json：组件 manifest 本身不带版本信息。
	stats.versionName = strings.TrimSpace(entry.Version.Name)
	if stats.versionName == "" {
		stats.versionName = component
	}
	if major, majorOK := ParseJavaVersionString(stats.versionName); majorOK {
		stats.majorVersion = major
	}

	runtimeID, idErr := buildRuntimeID(component, platformToken, stats.versionName)
	if idErr != nil {
		return download.TaskAccepted{}, idErr
	}
	taskID := runtimeEventTaskPrefix + runtimeID

	// 磁盘预检：不足时明确失败，不静默开始数百 MB 下载（卡面第 13 条）。
	if _, spaceErr := download.CheckDiskSpace(root, stats.totalBytes); spaceErr != nil {
		return download.TaskAccepted{}, downloadErrToAppError(spaceErr)
	}

	finalDir := filepath.Join(root, artifact.DirRuntimes, runtimeID)
	nowMs := runtimeSelectionNowMs()
	if installed, checkErr := installedRuntimeIntact(finalDir, files); checkErr != nil {
		return download.TaskAccepted{}, checkErr
	} else if installed {
		row := runtimeRow{
			ID:           runtimeID,
			Kind:         SourceDownloaded,
			Component:    component,
			MajorVersion: stats.majorVersion,
			FullVersion:  stats.versionName,
			Platform:     currentPlatformName(),
			Arch:         currentArch(),
			Bitness:      bitnessValue(currentBitness()),
			Vendor:       stats.vendor,
			SourceURI:    stripQuery(entry.Manifest.URL),
			SHA1:         entry.Manifest.SHA1,
			LocalRelPath: artifact.DirRuntimes + "/" + runtimeID,
			VerifiedAtMs: nowMs,
			CreatedAtMs:  nowMs,
		}
		if saveErr := saveRuntime(ctx, row); saveErr != nil {
			return download.TaskAccepted{}, saveErr
		}
		progress := newInstallProgress(taskID, 0, 0)
		progress.report(runtimeEventStateCompleted, "")
		runtimeLog().Info("Runtime 已存在且校验通过，跳过安装", logging.Fields{
			Event:      "runtime.install.skipped",
			Attributes: map[string]string{"runtimeId": runtimeID},
		})
		return download.TaskAccepted{
			TaskID:      taskID,
			State:       runtimeEventStateCompleted,
			Duplicate:   true,
			CreatedAtMs: nowMs,
		}, nil
	}

	// 已有目录但校验不通过：整体移入隔离区后重装，不就地覆盖。
	if _, statErr := os.Stat(finalDir); statErr == nil {
		quarantineDir := filepath.Join(root, artifact.DirRuntimes, runtimeQuarantinePrefix+runtimeID+"-"+fmt.Sprintf("%d", nowMs))
		if moveErr := os.Rename(finalDir, quarantineDir); moveErr != nil {
			return download.TaskAccepted{}, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeQuarantineFailed, false)
		}
		runtimeLog().Warn("已隔离损坏的 Runtime 目录", logging.Fields{
			Event:      "runtime.install.quarantined",
			Attributes: map[string]string{"runtimeId": runtimeID},
		})
	}

	stagingDir := filepath.Join(root, artifact.DirRuntimes, runtimeStagingPrefix+runtimeID)
	if cleanErr := os.RemoveAll(stagingDir); cleanErr != nil {
		return download.TaskAccepted{}, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeStagingFailed, false)
	}
	if mkdirErr := os.MkdirAll(stagingDir, 0o755); mkdirErr != nil {
		return download.TaskAccepted{}, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeStagingFailed, false)
	}

	// 优先按字节计进度：清单里单个文件的体积相差极大，按文件数计会让进度条失真。
	var totalBytes int64
	for _, entry := range files {
		if entry.Type == runtimeFileTypeDirectory || entry.Type == runtimeFileTypeLink {
			continue
		}
		totalBytes += entry.Downloads.Raw.Size
	}
	progress := newInstallProgress(taskID, totalBytes, stats.fileCount)
	progress.report(runtimeEventStateDownloading, "")

	transfer := runtimeTransferFactory(stagingDir)
	if downloadErr := downloadManifestFiles(ctx, transfer, stagingDir, files, progress); downloadErr != nil {
		_ = os.RemoveAll(stagingDir)
		progress.report(runtimeEventStateFailed, downloadErr.Code)
		return download.TaskAccepted{}, downloadErr
	}
	if verifyErr := verifyStagedRuntime(stagingDir, files); verifyErr != nil {
		_ = os.RemoveAll(stagingDir)
		progress.report(runtimeEventStateFailed, verifyErr.Code)
		return download.TaskAccepted{}, verifyErr
	}

	progress.report(runtimeEventStateCommitting, "")
	if commitErr := os.Rename(stagingDir, finalDir); commitErr != nil {
		_ = os.RemoveAll(stagingDir)
		progress.report(runtimeEventStateFailed, ErrorCodeRuntimeInstall)
		return download.TaskAccepted{}, newSystemError(ErrorCodeRuntimeInstall, messageRuntimeCommitFailed, false)
	}

	row := runtimeRow{
		ID:           runtimeID,
		Kind:         SourceDownloaded,
		Component:    component,
		MajorVersion: stats.majorVersion,
		FullVersion:  stats.versionName,
		Platform:     currentPlatformName(),
		Arch:         currentArch(),
		Bitness:      bitnessValue(currentBitness()),
		Vendor:       stats.vendor,
		SourceURI:    stripQuery(entry.Manifest.URL),
		SHA1:         entry.Manifest.SHA1,
		LocalRelPath: artifact.DirRuntimes + "/" + runtimeID,
		VerifiedAtMs: nowMs,
		CreatedAtMs:  nowMs,
	}
	if saveErr := saveRuntime(ctx, row); saveErr != nil {
		progress.report(runtimeEventStateFailed, saveErr.Code)
		return download.TaskAccepted{}, saveErr
	}
	progress.report(runtimeEventStateCompleted, "")
	runtimeLog().Info("Runtime 安装完成", logging.Fields{
		Event: "runtime.install.completed",
		Attributes: map[string]string{
			"runtimeId": runtimeID,
			"files":     fmt.Sprintf("%d", stats.fileCount),
		},
	})
	return download.TaskAccepted{
		TaskID:      taskID,
		State:       runtimeEventStateCompleted,
		Duplicate:   false,
		CreatedAtMs: nowMs,
	}, nil
}

// manifestFileStats 是组件 manifest 的聚合信息。
type manifestFileStats struct {
	// fileCount 为需要下载的文件数。
	fileCount int
	// totalBytes 为需要下载的总字节。
	totalBytes int64
	// versionName 为版本名（来自 all.json 的 version.name）。
	versionName string
	// majorVersion 为主版本号（从版本名解析，解析不出时为 0）。
	majorVersion int
	// vendor 为厂商（官方 Runtime 恒为 openjdk）。
	vendor string
}

// parseAllManifest 解析 all.json。
func parseAllManifest(payload []byte) (runtimeAllManifest, *AppError) {
	var all runtimeAllManifest
	if len(payload) == 0 {
		return nil, newInputError(ErrorCodeRuntimeInstall, messageRuntimeManifestMalformed)
	}
	if unmarshalErr := json.Unmarshal(payload, &all); unmarshalErr != nil {
		return nil, newInputError(ErrorCodeRuntimeInstall, messageRuntimeManifestMalformed)
	}
	if len(all) == 0 {
		return nil, newInputError(ErrorCodeRuntimeInstall, messageRuntimeManifestMalformed)
	}
	return all, nil
}

// selectManifestEntry 从 all.json 里挑出平台 + 组件的版本条目。
func selectManifestEntry(all runtimeAllManifest, platformToken, component string) (runtimeManifestEntry, bool) {
	components, platformOK := all[platformToken]
	if !platformOK {
		return runtimeManifestEntry{}, false
	}
	entries, componentOK := components[component]
	if !componentOK || len(entries) == 0 {
		return runtimeManifestEntry{}, false
	}
	entry := entries[0]
	if strings.TrimSpace(entry.Manifest.URL) == "" || entry.Manifest.Size <= 0 {
		return runtimeManifestEntry{}, false
	}
	return entry, true
}

// verifyManifestDocument 校验组件 manifest 的 size 与 sha1。
func verifyManifestDocument(payload []byte, expected runtimeManifestDownload) *AppError {
	if expected.Size > 0 && int64(len(payload)) != expected.Size {
		return newInputError(ErrorCodeRuntimeInstall, messageRuntimeManifestChecksumMismatch)
	}
	if checksum := strings.ToLower(strings.TrimSpace(expected.SHA1)); checksum != "" {
		sum := sha1.Sum(payload)
		if hex.EncodeToString(sum[:]) != checksum {
			return newInputError(ErrorCodeRuntimeInstall, messageRuntimeManifestChecksumMismatch)
		}
	}
	return nil
}

// parseComponentManifest 解析组件 manifest。
func parseComponentManifest(payload []byte) (runtimeManifestFiles, *AppError) {
	var document struct {
		Files runtimeManifestFiles `json:"files"`
	}
	if unmarshalErr := json.Unmarshal(payload, &document); unmarshalErr != nil {
		return nil, newInputError(ErrorCodeRuntimeInstall, messageRuntimeManifestMalformed)
	}
	if len(document.Files) == 0 {
		return nil, newInputError(ErrorCodeRuntimeInstall, messageRuntimeManifestMalformed)
	}
	return document.Files, nil
}

// inspectManifestFiles 汇总文件数、总字节与版本信息，并施加上限。
func inspectManifestFiles(files runtimeManifestFiles) (manifestFileStats, *AppError) {
	stats := manifestFileStats{vendor: VendorOpenJDK}
	for relPath, entry := range files {
		if _, pathErr := artifact.ValidateRelativePath(relPath); pathErr != nil {
			return stats, newInputError(ErrorCodeRuntimeInstall, messageRuntimeManifestMalformed)
		}
		switch entry.Type {
		case runtimeFileTypeDirectory:
			continue
		case runtimeFileTypeLink:
			// 官方 manifest 存在 link 条目（例如 `java` 指向 `bin/java`）。
			// 本卡不创建链接：内容由 `bin/java` 落盘，链接缺失不影响启动。
			continue
		case runtimeFileTypeFile:
			if entry.Downloads.Raw == nil {
				return stats, newInputError(ErrorCodeRuntimeInstall, messageRuntimeManifestMalformed)
			}
			raw := entry.Downloads.Raw
			if raw.Size <= 0 || raw.Size > runtimeMaxFileBytes {
				return stats, newInputError(ErrorCodeRuntimeUnpack, messageRuntimeFileTooLarge)
			}
			stats.fileCount++
			stats.totalBytes += raw.Size
		default:
			return stats, newInputError(ErrorCodeRuntimeInstall, messageRuntimeManifestMalformed)
		}
	}
	if stats.fileCount == 0 {
		return stats, newInputError(ErrorCodeRuntimeInstall, messageRuntimeManifestMalformed)
	}
	if stats.fileCount > runtimeMaxFiles {
		return stats, newInputError(ErrorCodeRuntimeUnpack, messageRuntimeTooManyFiles)
	}
	if stats.totalBytes > runtimeMaxTotalBytes {
		return stats, newInputError(ErrorCodeRuntimeUnpack, messageRuntimeTotalTooLarge)
	}
	return stats, nil
}

// sortedManifestPaths 返回按字典序排序的文件路径，保证安装顺序确定。
func sortedManifestPaths(files runtimeManifestFiles) []string {
	paths := make([]string, 0, len(files))
	for relPath := range files {
		paths = append(paths, relPath)
	}
	sort.Strings(paths)
	return paths
}

// downloadManifestFiles 逐文件下载到 staging 并校验。
func downloadManifestFiles(
	ctx context.Context,
	transfer download.TransferFunc,
	stagingDir string,
	files runtimeManifestFiles,
	progress *installProgress,
) *AppError {
	for _, relPath := range sortedManifestPaths(files) {
		entry := files[relPath]
		switch entry.Type {
		case runtimeFileTypeDirectory:
			target, pathErr := unpackTargetPath(stagingDir, relPath)
			if pathErr != nil {
				return pathErr
			}
			if mkdirErr := os.MkdirAll(target, 0o755); mkdirErr != nil {
				return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeStagingFailed, false)
			}
			continue
		case runtimeFileTypeLink:
			continue
		}

		raw := entry.Downloads.Raw
		task := download.Task{
			ID:             runtimeEventTaskPrefix + relPath,
			TaskType:       runtimeDownloadKindRaw,
			SourceURI:      raw.URL,
			TargetRelPath:  relPath,
			ExpectedSize:   raw.Size,
			ExpectedSHA1:   strings.ToLower(strings.TrimSpace(raw.SHA1)),
			TargetKind:     download.TargetKindVersion,
			StagingRelPath: "",
		}
		result := transfer(ctx, task)
		if result.Err != nil {
			runtimeLog().Warn("Runtime 文件下载失败", logging.Fields{
				Event:      "runtime.install.file_failed",
				ErrorCode:  ErrorCodeRuntimeInstall,
				Attributes: map[string]string{"runtimeFile": relPath},
			})
			return newExternalError(ErrorCodeRuntimeInstall, messageRuntimeFileDownloadFailed, true)
		}
		stagedPath, pathErr := unpackTargetPath(stagingDir, relPath)
		if pathErr != nil {
			return pathErr
		}
		verifyResult, verifyErr := download.VerifyFile(stagedPath, download.VerifyExpectation{
			ExpectedSize: raw.Size,
			ExpectedSHA1: strings.ToLower(strings.TrimSpace(raw.SHA1)),
		})
		if verifyErr != nil {
			_ = verifyResult
			return newInputError(ErrorCodeRuntimeInstall, messageRuntimeFileVerifyFailed)
		}
		if entry.Executable {
			applyExecutableBit(stagedPath)
		}
		progress.doneFiles++
		// 按实际写入字节累加：清单声明的 size 与实际不符时以实际为准，
		// 否则进度会在结尾处冲过 100% 或被截断。
		if result.Downloaded > 0 {
			progress.addBytes(result.Downloaded)
		} else {
			progress.addBytes(raw.Size)
		}
	}
	return nil
}

// verifyStagedRuntime 校验 staging 目录与 manifest 的文件集合一致。
//
// 数量与相对路径集合都要比：只比数量会让「少一个文件、多一个空文件」蒙混过关。
func verifyStagedRuntime(stagingDir string, files runtimeManifestFiles) *AppError {
	expected := make(map[string]bool, len(files))
	for relPath, entry := range files {
		if entry.Type == runtimeFileTypeFile {
			expected[relPath] = true
		}
	}
	actual := make(map[string]bool, len(expected))
	walkErr := filepath.WalkDir(stagingDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(stagingDir, path)
		if relErr != nil {
			return relErr
		}
		actual[filepath.ToSlash(rel)] = true
		return nil
	})
	if walkErr != nil {
		return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeManifestIncomplete, false)
	}
	if len(actual) != len(expected) {
		return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeManifestIncomplete, false)
	}
	for relPath := range expected {
		if !actual[relPath] {
			return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeManifestIncomplete, false)
		}
	}
	return nil
}

// installedRuntimeIntact 校验已有安装目录是否与 manifest 完全一致。
//
// 这是「不重装」的判据（D-47：没有 artifacts 表，只能按磁盘实际文件 + 流式哈希判定）。
func installedRuntimeIntact(finalDir string, files runtimeManifestFiles) (bool, *AppError) {
	if _, statErr := os.Stat(finalDir); statErr != nil {
		return false, nil
	}
	for relPath, entry := range files {
		if entry.Type != runtimeFileTypeFile {
			continue
		}
		target, pathErr := unpackTargetPath(finalDir, relPath)
		if pathErr != nil {
			return false, pathErr
		}
		raw := entry.Downloads.Raw
		if _, verifyErr := download.VerifyFile(target, download.VerifyExpectation{
			ExpectedSize: raw.Size,
			ExpectedSHA1: strings.ToLower(strings.TrimSpace(raw.SHA1)),
		}); verifyErr != nil {
			return false, nil
		}
	}
	return true, nil
}

// applyExecutableBit 给 POSIX 平台的可执行文件补上执行位。
func applyExecutableBit(path string) {
	if goruntime.GOOS == "windows" {
		return
	}
	_ = os.Chmod(path, 0o755)
}

// manifestPlatformToken 返回当前平台在 all.json 里的键。
func manifestPlatformToken() (string, bool) {
	switch goruntime.GOOS {
	case "windows":
		switch goruntime.GOARCH {
		case "amd64":
			return "windows-x64", true
		case "386":
			return "windows-x86", true
		case "arm64":
			return "windows-arm64", true
		}
	case "darwin":
		switch goruntime.GOARCH {
		case "amd64":
			return "mac-os", true
		case "arm64":
			return "mac-os-arm64", true
		}
	case "linux":
		switch goruntime.GOARCH {
		case "amd64":
			return "linux", true
		case "386":
			return "linux-i386", true
		}
	}
	return "", false
}

// buildRuntimeID 生成 Runtime 标识，并校验字符集。
//
// 形式 `<component>-<platform>-<arch>-<version>`；字符集只允许 `[a-z0-9._-]`，
// 版本名里的 `+`（例如 `21.0.5+11`）统一换成 `-`，其余非法字符直接拒绝。
func buildRuntimeID(component, platformToken, versionName string) (string, *AppError) {
	cleanComponent := sanitizeIDPart(component)
	cleanVersion := sanitizeIDPart(strings.ToLower(versionName))
	if cleanComponent == "" || cleanVersion == "" {
		return "", newInputError(ErrorCodeRuntimeInstall, messageRuntimeIDInvalid)
	}
	platform := currentPlatformName()
	arch := currentArch()
	identifier := cleanComponent + "-" + platform + "-" + arch + "-" + cleanVersion
	if _, idErr := artifact.ValidateRelativePath(identifier); idErr != nil {
		return "", newInputError(ErrorCodeRuntimeInstall, messageRuntimeIDInvalid)
	}
	if !isRuntimeIDCharsetValid(identifier) {
		return "", newInputError(ErrorCodeRuntimeInstall, messageRuntimeIDInvalid)
	}
	return identifier, nil
}

// sanitizeIDPart 归一化标识片段；返回空串表示片段不可用。
func sanitizeIDPart(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	for _, char := range trimmed {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		case char == '.' || char == '_' || char == '-':
			builder.WriteRune(char)
		case char == '+':
			builder.WriteRune('-')
		default:
			return ""
		}
	}
	return builder.String()
}

// isRuntimeIDCharsetValid 校验 Runtime 标识只含允许字符。
func isRuntimeIDCharsetValid(identifier string) bool {
	if identifier == "" {
		return false
	}
	for _, char := range identifier {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= '0' && char <= '9':
		case char == '.' || char == '_' || char == '-':
		default:
			return false
		}
	}
	return true
}

// stripQuery 去掉 URL 的查询串与片段，避免把参数写进数据库与日志。
func stripQuery(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if index := strings.IndexAny(trimmed, "?#"); index >= 0 {
		return trimmed[:index]
	}
	return trimmed
}

// downloadErrToAppError 把下载包的错误转换成运行时包的错误类型。
func downloadErrToAppError(source *download.Error) *AppError {
	if source == nil {
		return newSystemError(ErrorCodeRuntimeInstall, messageRuntimeInstallFailed, false)
	}
	switch source.Code {
	case download.ErrorCodeSpace:
		return newSystemError(download.ErrorCodeSpace, source.Message, true)
	case download.ErrorCodeVerify:
		return newInputError(download.ErrorCodeVerify, source.Message)
	default:
		return newSystemError(ErrorCodeRuntimeInstall, source.Message, true)
	}
}
