package artifact

import (
	"sort"
	"strings"
)

// TrustLevel 是制品在安装计划中的信任等级。
//
// 卡面第 48 条要求「无任何哈希的制品必须标记低信任」：无哈希意味着下载完成后
// 无法校验内容，只能作为「尽力而为」的制品安装，且**不得**进入共享缓存，
// 因为它会污染其他版本的复用链路。
type TrustLevel string

const (
	// TrustLevelLow 表示制品没有任何可用哈希（只能版本私有、标记低信任）。
	TrustLevelLow TrustLevel = "low"

	// TrustLevelUnverified 表示制品带哈希但尚未校验（verified_at_ms 为 0）。
	TrustLevelUnverified TrustLevel = "unverified"

	// TrustLevelVerified 表示制品带哈希且已校验通过。
	TrustLevelVerified TrustLevel = "verified"
)

// DefaultImmutable 返回某类制品的默认不可变标记（卡面第 8 条）。
//
// 客户端、依赖库、资源、本地库与 Runtime 是「内容由上游决定」的制品，落盘后不应被改动，
// 因此默认 true；mod / resource_pack / shader / plugin 属用户可替换内容（P5），
// 必须默认 false，否则会被共享缓存错误地复用。
func DefaultImmutable(kind ArtifactKind) bool {
	switch kind {
	case KindMod, KindResourcePack, KindShader, KindPlugin:
		return false
	case KindClient, KindLibrary, KindAsset, KindNative, KindRuntime:
		return true
	default:
		// 未知类型按可变处理：宁可少共享，也不让来源不明的制品污染共享缓存。
		return false
	}
}

// IsSharedKind 判断某类制品在结构上是否属于共享缓存。
//
// 该判定只看类别，不看校验状态：mod / resource_pack / shader / plugin 是实例级内容
// （P5），即使调用方手工把 Immutable 置为 true 也不得共享。
func IsSharedKind(kind ArtifactKind) bool {
	switch kind {
	case KindLibrary, KindAsset, KindNative, KindRuntime:
		return true
	default:
		return false
	}
}

// IsVerifiable 判断制品是否带可用哈希（能否进入校验流程）。
//
// 只要 SHA-1 或 SHA-256 有一个非空即视为可校验：
// 客户端 JAR 与 libraries 通常有 SHA-1，部分制品元数据只有 SHA-256。
// SHA-512 不计入可校验依据（决策 D-27：它只作为附加信息记录，不是寻址键）。
func IsVerifiable(artifact Artifact) bool {
	return strings.TrimSpace(artifact.Hashes.SHA1) != "" || strings.TrimSpace(artifact.Hashes.SHA256) != ""
}

// TrustLevelOf 计算制品当前所处的信任等级。
func TrustLevelOf(artifact Artifact) TrustLevel {
	if !IsVerifiable(artifact) {
		return TrustLevelLow
	}
	if artifact.VerifiedAtMs != 0 {
		return TrustLevelVerified
	}
	return TrustLevelUnverified
}

// CanShare 判断制品能否进入共享内容寻址缓存（卡面第 50 条）。
//
// 必须**同时**满足：
//  1. `Immutable` 为 true（可变内容不得共享，否则改动会污染其他版本）；
//  2. `VerifiedAtMs != 0`（未校验的制品不得共享，避免把半成品当成品复用）；
//  3. 带可用哈希（无哈希无法校验，只能版本私有，见卡面第 48 条）；
//  4. 类别属于共享缓存（mod 等实例级内容即使标了 immutable 也不共享）。
func CanShare(artifact Artifact) bool {
	if !IsSharedKind(artifact.Kind) {
		return false
	}
	if !artifact.Immutable {
		return false
	}
	if artifact.VerifiedAtMs == 0 {
		return false
	}
	return IsVerifiable(artifact)
}

// RequiresPrivateTarget 判断制品是否只能落在版本私有目录。
//
// 语义上是 CanShare 的补集，但单独提供便于调用方表达意图：
// 「这个制品不能共享，因此目标根必须是 versions/ 而不是 artifacts/objects/」。
func RequiresPrivateTarget(artifact Artifact) bool {
	return !CanShare(artifact)
}

// kindOrder 是制品种类在稳定排序中的固定次序。
//
// 顺序按「元数据 → 客户端 → 依赖库 → 资源 → 本地库 → Runtime → 实例内容」排列，
// 目的是让安装计划的列表顺序与安装依赖顺序一致，便于用户阅读与排障。
// 该次序是**代码常量**，不依赖 map 迭代顺序（卡面第 9 条确定性要求）。
var kindOrder = map[ArtifactKind]int{
	KindClient:       0,
	KindLibrary:      1,
	KindAsset:        2,
	KindNative:       3,
	KindRuntime:      4,
	KindMod:          5,
	KindResourcePack: 6,
	KindShader:       7,
	KindPlugin:       8,
}

// kindRank 返回制品类别的排序权重；未知类别排到最后并按字符串稳定区分。
func kindRank(kind ArtifactKind) int {
	if rank, ok := kindOrder[kind]; ok {
		return rank
	}
	return len(kindOrder)
}

// SortArtifacts 原地对制品列表做**确定性**稳定排序（卡面第 9 条）。
//
// 排序键依次为：类别权重 → LogicalKey → Version → ID。
// 三级字符键保证「相同类别 + 相同逻辑键」的制品仍有确定顺序，
// 不依赖输入顺序，也不依赖 map 迭代顺序，因此同一输入在任何机器上结果一致。
func SortArtifacts(artifacts []Artifact) {
	sort.SliceStable(artifacts, func(left, right int) bool {
		return compareArtifacts(artifacts[left], artifacts[right]) < 0
	})
}

// compareArtifacts 比较两个制品的排序先后，返回负数表示 left 在前。
func compareArtifacts(left, right Artifact) int {
	if leftRank, rightRank := kindRank(left.Kind), kindRank(right.Kind); leftRank != rightRank {
		return leftRank - rightRank
	}
	if left.Kind != right.Kind {
		// 权重相同但类别不同（未知类别之间），按类别字符串排序保证确定性。
		return strings.Compare(string(left.Kind), string(right.Kind))
	}
	if diff := strings.Compare(left.LogicalKey, right.LogicalKey); diff != 0 {
		return diff
	}
	if diff := strings.Compare(left.Version, right.Version); diff != 0 {
		return diff
	}
	return strings.Compare(left.ID, right.ID)
}

// ReferenceCount 是某个共享制品的引用次数。
type ReferenceCount struct {
	// ArtifactID 是被引用的制品 ID。
	ArtifactID string

	// Count 是引用次数（对同一制品的重复引用会累计）。
	Count int
}

// CountReferences 统计每个制品被引用的次数。
//
// 引用计数语义（决策 D-29）：P3 只预留，不落库、不参与删除决策。
// 共享缓存的对象在 P3 **永不被自动删除**——是否删除要等 P5 引入实例引用后才成立，
// 在此之前「没有引用的对象」并不等于「可删除的对象」（可能是用户手动放置的缓存）。
//
// 结果按 ArtifactID 升序返回切片而不是 map：map 的迭代顺序在 Go 中不稳定，
// 直接返回会让调用方的输出（日志、计划 JSON）不可复现（卡面第 9 条）。
func CountReferences(references []ArtifactReference) []ReferenceCount {
	counts := make(map[string]int, len(references))
	for _, reference := range references {
		id := strings.TrimSpace(reference.ArtifactID)
		if id == "" {
			// 空引用没有意义，跳过而不是计到空键上。
			continue
		}
		counts[id]++
	}

	result := make([]ReferenceCount, 0, len(counts))
	for id, count := range counts {
		result = append(result, ReferenceCount{ArtifactID: id, Count: count})
	}

	sort.SliceStable(result, func(left, right int) bool {
		return result[left].ArtifactID < result[right].ArtifactID
	})
	return result
}

// SortSources 对来源列表做确定性排序（Priority 升序，相同则按 ID）。
//
// 来源候选顺序直接影响下载选路，必须可复现：相同配置在任何机器上
// 都应得到相同的候选序列（决策 D-38 的镜像地址只是候选之一，不是信任根）。
func SortSources(sources []ArtifactSource) {
	sort.SliceStable(sources, func(left, right int) bool {
		if sources[left].Priority != sources[right].Priority {
			return sources[left].Priority < sources[right].Priority
		}
		return sources[left].ID < sources[right].ID
	})
}
