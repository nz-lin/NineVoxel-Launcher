// 本文件实现版本模块的 Wails 服务包装。
//
// 为什么服务类型放在 internal/version 而不是 launcher 根包：
// 注册新服务的唯一入口是 internal/app/bootstrap.go（D-23 允许追加服务注册），
// 而 internal/app 无法引用 package main 中的类型，因此服务类型必须定义在
// internal/version 内才能被 bootstrap 引用。
//
// 绑定发现性：wails3 的绑定生成器扫描所有被加载包中 application.NewService
// 的调用点（见 wails v3.0.0 internal/generator/analyse.go 的 FindServices），
// 因此这里的调用点同样会生成前端绑定，路径为
// bindings/example.com/ninevoxel/launcher/internal/version。
//
// 本卡不新增事件（P3 事件只属于下载任务），因此没有 BindEmitterToApp。
package version

import (
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"example.com/ninevoxel/launcher/internal/launch"
)

// VersionService 是版本子系统的 Wails 服务包装。
//
// 对外只暴露接口冻结清单 §1.2 中的三个只读方法；
// Wails 会把服务的每个导出方法映射为前端绑定，因此其余内部函数保持未导出。
type VersionService struct{}

// NewWailsService 创建版本服务，返回值可直接加入 application.Options.Services。
//
// 服务本身无状态：清单状态保存在包级变量中，由前端按需查询。
// 数据根通过 SetDataRootProvider 注入，未注入时三个方法返回 VERSION_LAYOUT_001。
func NewWailsService() application.Service {
	return application.NewService(&VersionService{})
}

// ListVersions 返回按类型过滤并分页后的可下载版本列表。
//
// 参数 req.Type 为空时只返回 release + snapshot（默认不展示旧预览版）；
// req.Offset / req.Limit 用于分页，Limit 上限为 200。
// 返回结果为 nil error 时不代表已联网：数据可能来自过期缓存，此时
// result.Stale 为 true 且 result.WarningCode 为 MANIFEST_STALE。
func (s *VersionService) ListVersions(req VersionListRequest) (VersionListResult, error) {
	result, appErr := ListVersions(req)
	if appErr != nil {
		return VersionListResult{}, appErr
	}
	return result, nil
}

// GetManifestStatus 返回当前清单的可用性状态。
//
// 该调用不联网：只读内存与磁盘缓存，供 UI 在启动时与事件丢失后恢复状态。
// 数据根不可用时返回 VERSION_LAYOUT_001。
func (s *VersionService) GetManifestStatus() (ManifestStatus, error) {
	status, appErr := GetManifestStatus()
	if appErr != nil {
		return ManifestStatus{}, appErr
	}
	return status, nil
}

// RefreshManifest 强制从官方源刷新清单并返回最新状态。
//
// 与 ListVersions 的降级策略不同：刷新失败会返回错误码，
// 让界面明确提示「刷新失败，当前使用缓存」，而不是静默沿用旧数据。
func (s *VersionService) RefreshManifest() (ManifestStatus, error) {
	status, appErr := RefreshManifest()
	if appErr != nil {
		return status, appErr
	}
	return status, nil
}

// GetVersionDetail 返回单个版本的详情与安装计划摘要。
//
// 只读本地版本元数据（`versions/<id>/<id>.json`），不发起任何网络请求；
// 元数据缺失或结构非法时返回 VERSION_JSON_001，数据根不可用时返回 VERSION_LAYOUT_001。
// 返回值不含绝对路径与下载地址。
func (s *VersionService) GetVersionDetail(req VersionDetailRequest) (VersionDetail, error) {
	detail, appErr := getVersionDetail(req)
	if appErr != nil {
		return VersionDetail{}, appErr
	}
	return detail, nil
}

// ResolveInstallPlan 返回确定性的安装计划（制品清单 + 哈希）。
//
// 同一份元数据重复调用必须得到相同的 summary.planHash；
// 计划不落库、不写盘、不下载，下载任务由 P3-04 依据该计划创建。
// 返回值中 sourceUri 恒为空：下载地址只在后端流转。
func (s *VersionService) ResolveInstallPlan(req ResolveInstallPlanRequest) (InstallPlanDTO, error) {
	plan, appErr := resolveInstallPlan(req)
	if appErr != nil {
		return InstallPlanDTO{}, appErr
	}
	return plan, nil
}

// GetCompatReport 返回指定版本的兼容性报告（P3-10）。
//
// 参数 req.VersionID 为版本目录名，req.GameDirectory 为游戏根目录：
// 结论全部来自元数据与本地文件存在性，不联网、不下载、不启动游戏。
// 返回的 Status 只能是六个状态词之一（planned / experimental / metadata_only /
// supported / deprecated / unsupported）；Evidence 给出每条依据及其是否已被实证。
func (s *VersionService) GetCompatReport(req CompatReportRequest) (CompatReportView, error) {
	view, appErr := buildCompatReport(req)
	if appErr != nil {
		return CompatReportView{}, appErr
	}
	return view, nil
}

// buildCompatReport 组装兼容性报告。
//
// 兼容性判定基于**本地** `<gameDirectory>/versions/<id>/<id>.json`，因此这里先
// 确保元数据就位（本地缺失时按需从官方源获取并校验 sha1），再交给启动模块评估。
func buildCompatReport(req CompatReportRequest) (CompatReportView, *AppError) {
	if strings.TrimSpace(req.VersionID) == "" {
		return CompatReportView{}, newInputError(ErrorCodeVersionJSON, messageCompatVersionIDRequired)
	}

	if ensureErr := ensureVersionMetadata(req.GameDirectory, req.VersionID); ensureErr != nil {
		return CompatReportView{}, ensureErr
	}

	traits, launchErr := launch.AssessVersionTraits(req.GameDirectory, req.VersionID)
	if launchErr != nil {
		return CompatReportView{}, &AppError{
			Code:      launchErr.Code,
			Message:   launchErr.Message,
			Retryable: false,
			Category:  CategoryInput,
		}
	}

	status := compatStatusWordOf(traits)
	evidence := make([]CompatEvidence, 0, len(traits.Notes))
	for _, note := range traits.Notes {
		evidence = append(evidence, CompatEvidence{
			Code:      note.Code,
			Message:   note.Message,
			Empirical: note.Empirical,
		})
	}

	inheritChain := make([]string, 0, len(traits.InheritChain))
	inheritChain = append(inheritChain, traits.InheritChain...)

	return CompatReportView{
		VersionID: req.VersionID,
		// 走到这里说明元数据已就位且已实际解析，四层判定基于真实内容。
		Evaluated: true,
		Layers: CompatLayers{
			MetadataSupported: traits.MetadataSupported,
			InstallSupported:  traits.InstallViable,
			LaunchSupported:   traits.LaunchViable,
			// 加载器安装属 P5-B，P3 恒为 false。
			LoaderSupported: false,
		},
		Status:   status,
		Evidence: evidence,
		Differences: CompatDifferences{
			AssetsLayout:          traits.AssetLayout,
			NativesConvention:     traits.NativesConvention,
			ArgumentsFormat:       traits.ArgumentsFormat,
			LoggingConfigured:     traits.LoggingConfigured,
			LibrariesWithoutRules: traits.LibrariesWithoutRules,
			MapToResources:        traits.MapToResources,
		},
		InheritChain: inheritChain,
		JavaRequirement: RuntimeRequirement{
			Major:  traits.JavaMajorVersion,
			Source: traits.JavaSource,
		},
		Summary: compatSummaryOf(status, traits),
	}, nil
}

// compatStatusWordOf 把推导结果归约为六个结论状态词。
//
// 映射规则：
//   - 元数据不可读 → unsupported；
//   - 元数据可读但缺安装所需标识 → metadata_only；
//   - 旧式虚拟资源索引或 map_to_resources → experimental（可安装但有旧格式差异）；
//   - 其余 → supported。
//
// planned / deprecated 属「未来计划」与「版本生命周期」语义，本卡没有可验证证据，故不产生。
func compatStatusWordOf(traits launch.VersionCompatTraits) string {
	switch {
	case !traits.MetadataSupported:
		return CompatStatusUnsupported
	case !traits.InstallViable:
		return CompatStatusMetadataOnly
	case traits.AssetLayout == launch.CompatAssetLayoutVirtual || traits.MapToResources:
		return CompatStatusExperimental
	default:
		return CompatStatusSupported
	}
}

// compatSummaryOf 生成一句中文结论摘要。
func compatSummaryOf(status string, traits launch.VersionCompatTraits) string {
	switch status {
	case CompatStatusUnsupported:
		if traits.MetadataReason != "" {
			return "元数据不可读：" + traits.MetadataReason
		}
		return "元数据不可读或结构不受支持。"
	case CompatStatusMetadataOnly:
		return "元数据可读，但缺少生成安装计划所需的客户端或资源索引标识。"
	case CompatStatusExperimental:
		return "可安装且可启动，但存在旧格式差异（资源索引布局或 map_to_resources），请按依据逐项确认。"
	default:
		return "元数据、安装计划与启动参数均可用。"
	}
}
