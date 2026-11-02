package download

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"example.com/ninevoxel/launcher/internal/artifact"
)

// 资源（Assets）管理：索引解析、对象展开、三种布局的目标路径计算。
//
// 本文件只做「纯逻辑 + 受限文件系统操作」，**不发起任何网络请求**，
// 也不自建调度器：索引与对象的下载一律交给 P3-05/P3-06 的队列
// （`Queue.EnqueuePlan`）完成，本包只负责把索引翻译成 `PlanItem`。
//
// 为什么不在本文件里下载索引：卡面第 1 条要求索引本身也当作制品
// 走传输与校验，如果这里另写一遍 HTTP 下载，就会出现两套重试、
// 两套断点续传与两套校验，最终两份代码各自漂移。因此这里只产出
// 计划项，由调用方把它们并入同一条下载计划。

const (
	// assetMaxIndexBytes 是资产索引文件的字节上限（卡面第 2 条）。
	//
	// 真实索引在 1.21 上约 400 KiB，64 MiB 是两数量级的余量；
	// 超过这个值只可能是恶意或损坏的索引，直接拒绝比流式解析更安全。
	assetMaxIndexBytes = 64 << 20

	// assetMaxObjects 是索引允许声明的对象条目上限（卡面第 3 条）。
	//
	// 200000 是经验防护值：真实资产索引在不同版本间大约 2k~4k 条，
	// 这个上限既不限制正常版本，也能挡住「用一个巨大 map 撑爆内存」的攻击。
	assetMaxObjects = 200000

	// assetMaxObjectBytes 是单个对象允许声明的字节上限（卡面第 3 条）。
	assetMaxObjectBytes = 512 << 20

	// assetProgressThrottleMs 是父任务进度事件的最小间隔（D-45，≥500ms）。
	assetProgressThrottleMs int64 = 500

	// assetFailureThresholdPercent 是子任务失败率阈值（卡面第 7 条，5%）。
	assetFailureThresholdPercent = 5

	// assetFailureSampleLimit 是进度载荷里携带的失败哈希样例上限。
	assetFailureSampleLimit = 5

	// AssetVirtualSubdir 是虚拟布局在版本私有目录下的子目录名。
	//
	// C-15/D-29 已冻结：virtual 组装产物必须写**版本私有目录**，
	// 不得写共享内容寻址缓存（否则不同版本的同名逻辑资源会互相覆盖）。
	// 因此最终形态是 `versions/<versionId>/virtual/<indexId>/<逻辑名>`。
	AssetVirtualSubdir = "virtual"

	// OfficialAssetObjectBaseURL 是官方资源对象的基址。
	//
	// 本常量只用于**拼接 URL 字符串**，不做任何网络请求；白名单校验
	// 由 `artifact` 与下载源层（P3-05）负责。镜像重写属于 P3-05 的
	// SourceRegistry，本文件不感知。
	OfficialAssetObjectBaseURL = "https://resources.download.minecraft.net/"
)

// 资产索引相关的稳定告警码。
//
// 告警不是失败：它们进入 `AssetIndexSummary.Warnings` 或装配报告的
// `Warnings`，由界面提示用户，但不阻止任务继续。
const (
	// WarningAssetTotalSizeMismatch 表示索引声明的 totalSize 与累加值不一致。
	WarningAssetTotalSizeMismatch = "ASSET_TOTAL_SIZE_MISMATCH"

	// WarningAssetVirtualSourceMissing 表示虚拟组装时对象源文件缺失。
	WarningAssetVirtualSourceMissing = "ASSET_VIRTUAL_SOURCE_MISSING"

	// WarningAssetMapToResourcesIgnored 表示索引要求 map_to_resources，
	// 但 D-29 已冻结 P3 不启用该布局，因此本次安装忽略该字段。
	WarningAssetMapToResourcesIgnored = "ASSET_MAP_TO_RESOURCES_IGNORED"
)

// AssetObject 是资产索引中的一个逻辑条目。
//
// 多个逻辑名可以指向同一个 `Hash`（不同语言、不同扩展名共用同一份
// 二进制内容），因此真正的下载单位是「去重后的 Hash」。
type AssetObject struct {
	// LogicalName 是索引里的逻辑名，例如 "minecraft/lang/en_US.lang"。
	LogicalName string

	// Hash 是对象内容的 SHA-1（小写 40 位十六进制），即寻址键（D-27）。
	Hash string

	// Size 是对象字节数。
	Size int64
}

// AssetIndex 是校验通过后的资产索引。
type AssetIndex struct {
	// ID 是索引标识，例如 "1.21"。
	ID string

	// Objects 是按逻辑名升序排列的条目列表（排序保证计划确定性）。
	Objects []AssetObject

	// UniqueHashes 是去重后的对象数。
	UniqueHashes int

	// TotalBytes 是全部条目声明大小之和（含重复哈希）。
	TotalBytes int64

	// DeclaredTotalSize 是版本元数据声明的 totalSize；0 表示未声明。
	DeclaredTotalSize int64

	// RawSize 是索引文件自身的字节数。
	RawSize int64

	// Virtual 表示索引要求使用虚拟布局。
	Virtual bool

	// MapToResources 表示索引声明了 map_to_resources（P3 不启用）。
	MapToResources bool

	// Warnings 是解析期的非致命提示。
	Warnings []AssetIndexWarning
}

// AssetIndexExpectation 是调用方对索引的已知信息。
//
// 这些值来自版本元数据（P3-02 的 assetIndex 条目），用于交叉校验；
// 索引文件本身不携带 totalSize，因此只能由调用方传入。
type AssetIndexExpectation struct {
	// IndexID 是期望的索引标识，必须与调用方要安装的版本一致。
	IndexID string

	// DeclaredTotalSize 是元数据里声明的 totalSize；0 表示未知。
	DeclaredTotalSize int64
}

// assetIndexDocument 是资产索引的磁盘结构。
type assetIndexDocument struct {
	// Objects 是「逻辑名 → 对象描述」的映射。
	Objects map[string]assetIndexObjectEntry `json:"objects"`

	// Virtual 表示该索引要求虚拟布局。
	Virtual bool `json:"virtual"`

	// MapToResources 表示该索引要求把资源复制到版本目录（P3 不启用）。
	MapToResources bool `json:"map_to_resources"`
}

// assetIndexObjectEntry 是索引中单个对象的描述。
type assetIndexObjectEntry struct {
	// Hash 是 SHA-1（小写 40 位十六进制）。
	Hash string `json:"hash"`

	// Size 是对象字节数。
	Size int64 `json:"size"`
}

// AssetObjectState 是「共享缓存中某对象当前状态」的判定结果。
//
// D-47：`artifacts` 表暂不建立，因此判定只能依赖磁盘实际文件。
// 这带来重复校验的开销（每次启动安装都要把已有对象重新算一遍 SHA-1），
// 换来的是「不依赖任何数据库状态」的简单性与可恢复性。
type AssetObjectState string

const (
	// AssetObjectMissing 表示目标文件不存在，需要下载。
	AssetObjectMissing AssetObjectState = "missing"

	// AssetObjectVerified 表示目标文件存在且大小与 SHA-1 均通过校验。
	AssetObjectVerified AssetObjectState = "verified"

	// AssetObjectCorrupt 表示目标文件存在但不满足声明（大小或哈希不符）。
	AssetObjectCorrupt AssetObjectState = "corrupt"
)

// AssetExpansion 是「索引 → 对象下载计划」的展开结果。
type AssetExpansion struct {
	// Summary 是索引摘要，供界面与诊断使用。
	Summary AssetIndexSummary

	// Items 是需要交给队列的对象计划项（按目标路径升序，确定性）。
	Items []PlanItem

	// UniqueObjects 是去重后的对象总数。
	UniqueObjects int

	// AlreadyPresent 是「已存在且校验通过、直接跳过」的对象数。
	AlreadyPresent int

	// CorruptObjects 是「存在但校验不通过、需要重新下载」的对象数。
	CorruptObjects int
}

// AssetExpandOptions 是对象展开的可选参数。
type AssetExpandOptions struct {
	// VerifyExisting 为 true 时，先校验共享缓存里已有对象（卡面第 8 条）。
	//
	// 关闭它可以把「校验」推迟到提交阶段（P3-06 的 CommitFunc 本来
	// 就会拒绝覆盖校验不通过的目标），从而避免启动时长时间哈希。
	VerifyExisting bool

	// ObjectBaseURL 是对象下载 URL 的基址；空串表示使用官方基址。
	ObjectBaseURL string
}

// LinkMode 是虚拟布局实际采用的链接方式。
type LinkMode string

const (
	// LinkModeHardLink 表示硬链接（首选：零拷贝且与共享缓存同生共死）。
	LinkModeHardLink LinkMode = "hardlink"

	// LinkModeSymlink 表示符号链接（硬链接不可用时的回退）。
	LinkModeSymlink LinkMode = "symlink"

	// LinkModeCopy 表示复制（前两者都不可用时的最后手段，占用双份空间）。
	LinkModeCopy LinkMode = "copy"
)

// VirtualAssembly 是虚拟布局的组装报告。
//
// 卡面要求「记录实际选择了哪种方式」：`Mode` 是首个成功文件所用的方式，
// 其余计数用于观测回退是否普遍（若 `Copied` 很高，说明该机器不支持链接，
// 用户会突然多占一份磁盘，需要在界面上提示）。
type VirtualAssembly struct {
	// IndexID 是被组装的索引标识。
	IndexID string

	// Root 是组装目标目录（版本私有目录下的绝对路径）。
	Root string

	// Mode 是首个成功文件采用的方式；没有任何文件被组装时为空串。
	Mode LinkMode

	// Linked 是成功建立链接（硬链接或符号链接）的文件数。
	Linked int

	// HardLinked 是硬链接数。
	HardLinked int

	// Symlinked 是符号链接数。
	Symlinked int

	// Copied 是复制数。
	Copied int

	// Skipped 是源对象缺失而跳过的文件数。
	Skipped int

	// AlreadyPresent 是目标已存在且内容一致、因此跳过的文件数。
	AlreadyPresent int

	// Warnings 是组装期非致命提示。
	Warnings []AssetIndexWarning
}

// ParseAssetIndex 解析并校验资产索引内容。
//
// 校验口径（卡面第 3 条，全部失败都返回 ASSET_INDEX_001，不 panic）：
//   - 内容非空、合法 JSON、`objects` 非空；
//   - 条目数不超过 assetMaxObjects；
//   - 每个逻辑名必须是非越界的相对路径；
//   - 每个 hash 必须是 40 位小写十六进制（SHA-1，D-27），
//     严禁把 SHA-256 填进这个字段；
//   - 每个 size 必须大于 0 且不超过 assetMaxObjectBytes。
//
// 只有 totalSize 不一致这一项是告警而不是拒绝：它常见于官方索引的
// 历史误差，拒绝会让整批次安装失败，而实际对象仍然可用。
func ParseAssetIndex(raw []byte, expected AssetIndexExpectation) (AssetIndex, *Error) {
	index := AssetIndex{
		ID:                expected.IndexID,
		DeclaredTotalSize: expected.DeclaredTotalSize,
		RawSize:           int64(len(raw)),
	}
	if len(raw) == 0 {
		return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetIndexEmpty)
	}
	if len(raw) > assetMaxIndexBytes {
		return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetIndexTooLarge)
	}

	var document assetIndexDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetIndexMalformed)
	}
	if len(document.Objects) == 0 {
		return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetIndexNoObjects)
	}
	if len(document.Objects) > assetMaxObjects {
		return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetTooManyObjects)
	}

	objects := make([]AssetObject, 0, len(document.Objects))
	uniqueHashes := make(map[string]struct{}, len(document.Objects))
	for logicalName, entry := range document.Objects {
		if logicalName == "" {
			return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetLogicalNameEmpty)
		}
		if _, pathErr := artifact.ValidateRelativePath(logicalName); pathErr != nil {
			return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetLogicalNameInvalid)
		}
		if !isAssetSHA1(entry.Hash) {
			return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetHashInvalid)
		}
		if entry.Size <= 0 {
			return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetObjectSizeInvalid)
		}
		if entry.Size > assetMaxObjectBytes {
			return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetObjectTooLarge)
		}
		objects = append(objects, AssetObject{
			LogicalName: logicalName,
			Hash:        entry.Hash,
			Size:        entry.Size,
		})
		index.TotalBytes += entry.Size
		uniqueHashes[entry.Hash] = struct{}{}
	}

	// 按逻辑名排序：计划项与虚拟组装的顺序都依赖它，必须与磁盘枚举顺序无关。
	sort.Slice(objects, func(left, right int) bool {
		return objects[left].LogicalName < objects[right].LogicalName
	})

	index.Objects = objects
	index.UniqueHashes = len(uniqueHashes)
	index.Virtual = document.Virtual
	index.MapToResources = document.MapToResources

	if expected.DeclaredTotalSize > 0 && expected.DeclaredTotalSize != index.TotalBytes {
		index.Warnings = append(index.Warnings, AssetIndexWarning{
			Code:    WarningAssetTotalSizeMismatch,
			Message: messageAssetTotalSizeMismatch,
		})
	}
	if index.MapToResources {
		// D-29：P3 不启用 map_to_resources，只提示并忽略，绝不写资源目录。
		index.Warnings = append(index.Warnings, AssetIndexWarning{
			Code:    WarningAssetMapToResourcesIgnored,
			Message: messageAssetMapToResourcesIgnored,
		})
	}
	return index, nil
}

// LoadAssetIndex 从数据根读取索引文件并解析。
//
// 路径固定为 `artifacts/indexes/<indexId>.json`（卡面第 2 条），
// 由 `artifact.IndexRelPath` 生成，确保索引名不越界。
func LoadAssetIndex(dataRoot string, indexID string) (AssetIndex, *Error) {
	relPath, pathErr := artifact.IndexRelPath(indexID)
	if pathErr != nil {
		return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetIndexPathInvalid)
	}
	absolute := filepath.Join(dataRoot, filepath.FromSlash(relPath))

	info, statErr := os.Stat(absolute)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetIndexMissing)
		}
		return AssetIndex{}, newSystemError(ErrorCodeAssetIndex, messageAssetIndexUnreadable, false)
	}
	if info.IsDir() {
		return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetIndexUnreadable)
	}
	if info.Size() > assetMaxIndexBytes {
		return AssetIndex{}, newInputError(ErrorCodeAssetIndex, messageAssetIndexTooLarge)
	}

	raw, readErr := os.ReadFile(absolute)
	if readErr != nil {
		return AssetIndex{}, newSystemError(ErrorCodeAssetIndex, messageAssetIndexUnreadable, false)
	}
	return ParseAssetIndex(raw, AssetIndexExpectation{IndexID: indexID})
}

// AssetIndexPlanItem 生成索引自身的下载计划项。
//
// 索引是 kind=asset 的制品（卡面第 1 条），由 P3-05/P3-06 传输与校验；
// `TargetRelPath` 用 `artifact.IndexRelPath` 生成，与 `LoadAssetIndex`
// 的读取路径严格一致——否则会出现「下载到 A、校验读 B」的静默错位。
func AssetIndexPlanItem(indexID string, sourceURI string, size int64, sha1 string) (PlanItem, *Error) {
	relPath, pathErr := artifact.IndexRelPath(indexID)
	if pathErr != nil {
		return PlanItem{}, newInputError(ErrorCodeAssetIndex, messageAssetIndexPathInvalid)
	}
	if !isAssetSHA1(sha1) {
		return PlanItem{}, newInputError(ErrorCodeAssetIndex, messageAssetHashInvalid)
	}
	if size <= 0 {
		return PlanItem{}, newInputError(ErrorCodeAssetIndex, messageAssetObjectSizeInvalid)
	}
	return PlanItem{
		ArtifactID:    "asset_index:" + indexID,
		TaskType:      TaskTypeAssetIndex,
		SourceURI:     sourceURI,
		TargetRelPath: relPath,
		TargetKind:    TargetKindShared,
		ExpectedSize:  size,
		ExpectedSHA1:  sha1,
	}, nil
}

// InspectAssetObject 判定共享缓存中某对象的当前状态。
//
// 三种结果的用途：
//   - missing：需要生成下载计划项；
//   - verified：直接标记完成，不重复下载（卡面第 8 条）；
//   - corrupt：需要重新下载（队列的提交阶段会校验并拒绝覆盖坏文件，
//     因此这里只报告状态，不主动删除）。
//
// 大小不符时不再计算哈希：`VerifyFile` 本来也会先比大小，
// 但显式短路能让大文件的失败判定在毫秒级完成。
func InspectAssetObject(dataRoot string, hash string, size int64) (AssetObjectState, *Error) {
	relPath, pathErr := artifact.ObjectRelPath(artifact.HashAlgorithmSHA1, hash)
	if pathErr != nil {
		return AssetObjectMissing, newInputError(ErrorCodeAssetIndex, messageAssetHashInvalid)
	}
	absolute := filepath.Join(dataRoot, filepath.FromSlash(relPath))

	info, statErr := os.Stat(absolute)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return AssetObjectMissing, nil
		}
		return AssetObjectCorrupt, newSystemError(ErrorCodeAssetIndex, messageAssetObjectUnreadable, false)
	}
	if info.IsDir() || info.Size() != size {
		return AssetObjectCorrupt, nil
	}
	if _, verifyErr := VerifyFile(absolute, VerifyExpectation{ExpectedSize: size, ExpectedSHA1: hash}); verifyErr != nil {
		return AssetObjectCorrupt, nil
	}
	return AssetObjectVerified, nil
}

// ExpandAssetObjects 把索引展开成对象下载计划项。
//
// 去重键是 hash（卡面第 4 条）：同一个哈希在索引里出现多次时只生成
// 一个计划项，其余逻辑名在虚拟布局或运行时解析时共享同一份内容。
//
// 顺序保证：结果按目标相对路径升序，输入索引的排序变化不会改变输出，
// 这样同一次安装重复展开会得到完全相同的计划（幂等键依赖这一点）。
func ExpandAssetObjects(dataRoot string, index AssetIndex, options AssetExpandOptions) (AssetExpansion, *Error) {
	expansion := AssetExpansion{
		Summary:       indexSummaryOf(index),
		UniqueObjects: index.UniqueHashes,
	}

	// 按 hash 聚合：同一个对象可能对应多个逻辑名，第一个出现的名字
	// 只用于日志与诊断，不影响目标路径（目标路径只由 hash 决定）。
	unique := make(map[string]AssetObject, len(index.Objects))
	for _, object := range index.Objects {
		if _, exists := unique[object.Hash]; exists {
			continue
		}
		unique[object.Hash] = object
	}

	baseURL := strings.TrimSuffix(options.ObjectBaseURL, "/")
	if baseURL == "" {
		baseURL = strings.TrimSuffix(OfficialAssetObjectBaseURL, "/")
	}

	// 固定遍历顺序：先排序，再生成计划项。
	hashes := make([]string, 0, len(unique))
	for hash := range unique {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)

	for _, hash := range hashes {
		object := unique[hash]
		if options.VerifyExisting {
			state, inspectErr := InspectAssetObject(dataRoot, object.Hash, object.Size)
			if inspectErr != nil {
				return AssetExpansion{}, inspectErr
			}
			switch state {
			case AssetObjectVerified:
				expansion.AlreadyPresent++
				continue
			case AssetObjectCorrupt:
				expansion.CorruptObjects++
			}
		}

		relPath, pathErr := artifact.ObjectRelPath(artifact.HashAlgorithmSHA1, object.Hash)
		if pathErr != nil {
			return AssetExpansion{}, newInputError(ErrorCodeAssetIndex, messageAssetHashInvalid)
		}
		expansion.Items = append(expansion.Items, PlanItem{
			ArtifactID:    "asset:" + object.Hash,
			TaskType:      TaskTypeAsset,
			SourceURI:     baseURL + "/" + object.Hash[:2] + "/" + object.Hash,
			TargetRelPath: relPath,
			TargetKind:    TargetKindShared,
			ExpectedSize:  object.Size,
			ExpectedSHA1:  object.Hash,
		})
	}

	sort.Slice(expansion.Items, func(left, right int) bool {
		return expansion.Items[left].TargetRelPath < expansion.Items[right].TargetRelPath
	})
	return expansion, nil
}

// AssetVirtualTargetDir 计算虚拟布局的目标目录（版本私有）。
//
// C-15/D-29：虚拟布局的组装产物必须落在版本私有目录，因为逻辑名
// （如 `minecraft/lang/en_US.lang`）在不同版本间内容不同，写进共享
// 缓存会互相覆盖。最终形态：`versions/<versionId>/virtual/<indexId>/`。
func AssetVirtualTargetDir(dataRoot string, versionID string, indexID string) (string, *Error) {
	if versionID == "" || indexID == "" {
		return "", newInputError(ErrorCodeAssetLayout, messageAssetLayoutTargetInvalid)
	}
	relPath := artifact.DirVersions + "/" + versionID + "/" + AssetVirtualSubdir + "/" + indexID
	cleaned, pathErr := artifact.ValidateRelativePath(relPath)
	if pathErr != nil {
		return "", newInputError(ErrorCodeAssetLayout, messageAssetLayoutTargetInvalid)
	}
	return filepath.Join(dataRoot, filepath.FromSlash(cleaned)), nil
}

// AssembleVirtualAssets 按虚拟布局把共享缓存里的对象组织成逻辑名目录树。
//
// 卡面第 5 条：硬链接 → 软链接 → 复制，逐级回退，且**链接失败不得导致
// 任务失败**。因此只有「复制也失败」才会返回错误（那通常是磁盘空间或
// 权限问题，继续执行只会留下残缺目录）。
//
// 幂等性：目标已存在时先比对（大小与 SHA-1），一致则跳过并计入
// `AlreadyPresent`；不一致则删除后重建，避免升级版本后留下旧内容。
//
// 参数 dataRoot 是数据根（与本文件其它函数一致），共享缓存的对象目录由
// artifact.DirArtifactObjects 派生，调用方不必自己拼接。
func AssembleVirtualAssets(dataRoot string, targetDir string, index AssetIndex) (VirtualAssembly, *Error) {
	assembly := VirtualAssembly{IndexID: index.ID, Root: targetDir}
	if dataRoot == "" || targetDir == "" {
		return assembly, newInputError(ErrorCodeAssetLayout, messageAssetLayoutTargetInvalid)
	}
	// 共享缓存的对象目录：artifacts/objects/<前两位>/<hash>。
	objectsRoot := filepath.Join(dataRoot, filepath.FromSlash(artifact.DirArtifactObjects))

	// 按逻辑名排序：保证同样的索引得到同样的组装顺序与报告。
	objects := append([]AssetObject(nil), index.Objects...)
	sort.Slice(objects, func(left, right int) bool {
		return objects[left].LogicalName < objects[right].LogicalName
	})

	missingWarned := false
	for _, object := range objects {
		relPath, pathErr := artifact.ValidateRelativePath(object.LogicalName)
		if pathErr != nil {
			return assembly, newInputError(ErrorCodeAssetLayout, messageAssetLogicalNameInvalid)
		}
		target := filepath.Join(targetDir, filepath.FromSlash(relPath))
		source := filepath.Join(objectsRoot, object.Hash[:2], object.Hash)

		if _, statErr := os.Stat(source); statErr != nil {
			assembly.Skipped++
			if !missingWarned {
				// 只提示一次：索引里可能成千上万条都缺源文件，
				// 逐条告警会把日志与界面淹没。
				assembly.Warnings = append(assembly.Warnings, AssetIndexWarning{
					Code:    WarningAssetVirtualSourceMissing,
					Message: messageAssetVirtualSourceMissing,
				})
				missingWarned = true
			}
			continue
		}

		// 目标已存在：内容一致则跳过，否则删除重建。
		if state, inspectErr := inspectExistingFile(target, object.Hash, object.Size); inspectErr != nil {
			return assembly, inspectErr
		} else if state == AssetObjectVerified {
			assembly.AlreadyPresent++
			continue
		} else if state == AssetObjectCorrupt {
			if removeErr := os.Remove(target); removeErr != nil && !os.IsNotExist(removeErr) {
				return assembly, newSystemError(ErrorCodeAssetLayout, messageAssetLayoutWriteFailed, false)
			}
		}

		if mkdirErr := os.MkdirAll(filepath.Dir(target), 0o755); mkdirErr != nil {
			return assembly, newSystemError(ErrorCodeAssetLayout, messageAssetLayoutWriteFailed, false)
		}

		mode, linkErr := linkOrCopy(source, target)
		if linkErr != nil {
			return assembly, linkErr
		}
		if assembly.Mode == "" {
			assembly.Mode = mode
		}
		switch mode {
		case LinkModeHardLink:
			assembly.HardLinked++
			assembly.Linked++
		case LinkModeSymlink:
			assembly.Symlinked++
			assembly.Linked++
		case LinkModeCopy:
			assembly.Copied++
		}
	}
	return assembly, nil
}

// AssetProgressTracker 聚合资产父任务的进度。
//
// 事件分工（D-45）：父任务进度走 `download.task.changed.v1`，载荷带
// progress 字段；子任务完全不发事件。本结构只负责「何时该发」与
// 「发什么」，实际发送由调用方用 `Sink` 完成——这样单元测试不需要
// Wails 运行时也能断言节流行为。
type AssetProgressTracker struct {
	mu   sync.Mutex
	now  func() int64
	task string

	totalObjects   int
	totalBytes     int64
	completed      int
	failed         int
	completedBytes int64
	failureSamples []string

	lastEmitMs int64
}

// NewAssetProgressTracker 创建一个父任务进度聚合器。
func NewAssetProgressTracker(taskID string, index AssetIndex) *AssetProgressTracker {
	return NewAssetProgressTrackerWithClock(taskID, index, defaultAssetClock)
}

// NewAssetProgressTrackerWithClock 用可注入的时钟创建聚合器（便于测试节流）。
func NewAssetProgressTrackerWithClock(taskID string, index AssetIndex, now func() int64) *AssetProgressTracker {
	tracker := &AssetProgressTracker{
		now:            now,
		task:           taskID,
		totalObjects:   index.UniqueHashes,
		failureSamples: make([]string, 0, assetFailureSampleLimit),
	}
	seen := make(map[string]struct{}, len(index.Objects))
	for _, object := range index.Objects {
		if _, exists := seen[object.Hash]; exists {
			continue
		}
		seen[object.Hash] = struct{}{}
		tracker.totalBytes += object.Size
	}
	if tracker.now == nil {
		tracker.now = defaultAssetClock
	}
	return tracker
}

// RecordObject 记录一个对象的终态，并返回当前进度与「是否应发事件」。
//
// 第二个返回值为 true 的三种情形：首条记录、距上次发送已满 500ms、
// 已处理完所有对象（终态必须送达，否则界面会停在中途）。
func (t *AssetProgressTracker) RecordObject(hash string, size int64, ok bool) (AssetTaskProgress, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	nowMs := t.now()
	if ok {
		t.completed++
		t.completedBytes += size
	} else {
		t.failed++
		if len(t.failureSamples) < assetFailureSampleLimit {
			t.failureSamples = append(t.failureSamples, shortHash(hash))
		}
	}

	shouldEmit := t.lastEmitMs == 0 ||
		nowMs-t.lastEmitMs >= assetProgressThrottleMs ||
		t.finishedLocked()
	if shouldEmit {
		t.lastEmitMs = nowMs
	}
	return t.snapshotLocked(nowMs), shouldEmit
}

// Snapshot 返回当前进度快照（不发事件，供查询接口使用）。
func (t *AssetProgressTracker) Snapshot() AssetTaskProgress {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked(t.now())
}

// FailureThresholdExceeded 判断失败率是否**超过** 5% 阈值。
//
// 返回是否超阈值、失败数量与最多 5 个失败哈希前缀。
// 「超过」是严格大于：卡面第 7 条写的是「超过 5%」，恰好 5%（例如 20 个
// 对象里失败 1 个）属于可容忍范围，不应把父任务判为失败。
// 判定用整数乘法（failed*100 > total*5），避免浮点误差让 5% 恰好
// 落在边界上时判定抖动。
func (t *AssetProgressTracker) FailureThresholdExceeded() (bool, int, []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	exceeded := t.totalObjects > 0 && t.failed > 0 &&
		t.failed*100 > t.totalObjects*assetFailureThresholdPercent
	samples := append([]string(nil), t.failureSamples...)
	return exceeded, t.failed, samples
}

// snapshotLocked 在持锁状态下构造进度载荷。
func (t *AssetProgressTracker) snapshotLocked(nowMs int64) AssetTaskProgress {
	progress := 0.0
	switch {
	case t.totalBytes > 0:
		progress = float64(t.completedBytes) / float64(t.totalBytes)
	case t.totalObjects > 0:
		progress = float64(t.completed) / float64(t.totalObjects)
	}
	if progress > 1 {
		progress = 1
	}
	failureRate := 0.0
	if t.totalObjects > 0 {
		failureRate = float64(t.failed) / float64(t.totalObjects)
	}
	state := "downloading"
	switch {
	case t.totalObjects > 0 && t.failed*100 >= t.totalObjects*assetFailureThresholdPercent && t.failed > 0:
		state = "failed"
	case t.finishedLocked():
		state = "completed"
	}
	return AssetTaskProgress{
		TaskID:           t.task,
		CompletedObjects: t.completed,
		FailedObjects:    t.failed,
		TotalObjects:     t.totalObjects,
		CompletedBytes:   t.completedBytes,
		TotalBytes:       t.totalBytes,
		Progress:         progress,
		FailureRate:      failureRate,
		State:            state,
		FailureSamples:   append([]string(nil), t.failureSamples...),
		UpdatedAtMs:      nowMs,
	}
}

// finishedLocked 判断是否已处理完全部对象。
func (t *AssetProgressTracker) finishedLocked() bool {
	return t.totalObjects > 0 && t.completed+t.failed >= t.totalObjects
}

// indexSummaryOf 把索引转成对外摘要。
func indexSummaryOf(index AssetIndex) AssetIndexSummary {
	warnings := append([]AssetIndexWarning(nil), index.Warnings...)
	return AssetIndexSummary{
		IndexID:           index.ID,
		ObjectCount:       len(index.Objects),
		UniqueHashes:      index.UniqueHashes,
		TotalBytes:        index.TotalBytes,
		DeclaredTotalSize: index.DeclaredTotalSize,
		TotalSizeMismatch: index.DeclaredTotalSize > 0 && index.DeclaredTotalSize != index.TotalBytes,
		IndexSizeBytes:    index.RawSize,
		Virtual:           index.Virtual,
		MapToResources:    index.MapToResources,
		Warnings:          warnings,
	}
}

// inspectExistingFile 检查目标文件是否已是期望内容。
func inspectExistingFile(path string, hash string, size int64) (AssetObjectState, *Error) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return AssetObjectMissing, nil
		}
		return AssetObjectCorrupt, newSystemError(ErrorCodeAssetLayout, messageAssetLayoutWriteFailed, false)
	}
	if info.IsDir() || info.Size() != size {
		return AssetObjectCorrupt, nil
	}
	if _, verifyErr := VerifyFile(path, VerifyExpectation{ExpectedSize: size, ExpectedSHA1: hash}); verifyErr != nil {
		return AssetObjectCorrupt, nil
	}
	return AssetObjectVerified, nil
}

// assetLinkOperations 是链接系统调用的可注入替身。
//
// 生产环境直接使用 os.Link / os.Symlink。测试替换它来验证
// 「硬链接失败 → 软链接 → 复制」的回退链，而不需要真的构造
// 跨卷目录或修改文件系统权限（那会让测试依赖运行环境）。
var assetLinkOperations = struct {
	hardLink func(oldname string, newname string) error
	symlink  func(oldname string, newname string) error
}{
	hardLink: os.Link,
	symlink:  os.Symlink,
}

// linkOrCopy 依次尝试硬链接、软链接、复制。
//
// 三次尝试都失败的最终错误统一为 ASSET_LAYOUT_001：此时通常是磁盘
// 空间不足或目标目录不可写，继续组装只会留下半个目录树。
func linkOrCopy(source string, target string) (LinkMode, *Error) {
	if err := assetLinkOperations.hardLink(source, target); err == nil {
		return LinkModeHardLink, nil
	}
	// 硬链接在 Windows 需要 NTFS + 权限，跨卷时也会失败；
	// 这里不区分具体原因，一律回退软链接（卡面第 5 条）。
	if err := assetLinkOperations.symlink(source, target); err == nil {
		return LinkModeSymlink, nil
	}
	if err := copyAssetFile(source, target); err != nil {
		return "", err
	}
	return LinkModeCopy, nil
}

// copyAssetFile 复制单个文件（虚拟布局的最后回退）。
func copyAssetFile(source string, target string) *Error {
	sourceFile, openErr := os.Open(source)
	if openErr != nil {
		return newSystemError(ErrorCodeAssetLayout, messageAssetLayoutWriteFailed, false)
	}
	defer func() {
		_ = sourceFile.Close()
	}()

	targetFile, createErr := os.Create(target)
	if createErr != nil {
		return newSystemError(ErrorCodeAssetLayout, messageAssetLayoutWriteFailed, false)
	}
	if _, copyErr := io.Copy(targetFile, sourceFile); copyErr != nil {
		_ = targetFile.Close()
		return newSystemError(ErrorCodeAssetLayout, messageAssetLayoutWriteFailed, false)
	}
	if syncErr := targetFile.Sync(); syncErr != nil {
		_ = targetFile.Close()
		return newSystemError(ErrorCodeAssetLayout, messageAssetLayoutWriteFailed, false)
	}
	if closeErr := targetFile.Close(); closeErr != nil {
		return newSystemError(ErrorCodeAssetLayout, messageAssetLayoutWriteFailed, false)
	}
	return nil
}

// isAssetSHA1 判断是否为 40 位小写十六进制 SHA-1。
//
// 只接受小写：`artifact.ObjectRelPath` 同样只接受小写，若这里放宽，
// 大写哈希会在生成目标路径时被拒绝，用户拿到的错误会指向错误的层。
func isAssetSHA1(value string) bool {
	if len(value) != 40 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// shortHash 把哈希截短成 8 位，供进度载荷展示。
func shortHash(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}

// defaultAssetClock 是进度聚合器默认时钟（毫秒时间戳）。
//
// 用 Unix 毫秒而非 time.Time：队列与事件载荷全部以毫秒整数对齐，
// 直接产出整数可以省掉每层转换。
func defaultAssetClock() int64 {
	return time.Now().UnixMilli()
}
