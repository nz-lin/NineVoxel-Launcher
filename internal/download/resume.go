// 本文件实现断点续传决策：Range 能力探测、ETag / Last-Modified 一致性校验、
// 以及「服务器忽略 Range」时的安全全量重启（C-8 / D-34，卡面第 5~8 条）。
//
// 这里全部是纯函数：不读时钟、不做 IO、不访问网络。传输层只负责按决策结果
// 发起请求与写字节，所有「该追加还是该重写」的判断都集中在 DecideResponse。
//
// 关键的缺陷来源（本文件存在的理由）：服务器对 `Range` 请求返回 200 时，
// 响应体是**完整文件**。若把它追加到已有 .part 之后，就会得到一个
// 「前半段是旧内容、后半段是完整文件」的坏文件，且大小恰好可能通过后续校验前的
// 粗略检查。因此该分支必须强制丢弃已写字节并从头写。
package download

import (
	"strconv"
	"strings"
)

// PartState 描述本地 `.part` 文件的已知状态。
//
// 字段来源：download_tasks 表的 range_start / etag / last_modified（D-25 冻结列）。
// 本卡只读这些记录，不负责写入（落库归 P3-06）。
type PartState struct {
	// Size 为 `.part` 已写入的字节数。
	Size int64

	// ETag 为上一次响应记录的 ETag，空表示没有可信记录。
	ETag string

	// LastModified 为上一次响应记录的 Last-Modified，空表示没有可信记录。
	LastModified string
}

// HasEntityTag 报告是否存在可用于一致性校验的记录。
//
// ETag 与 Last-Modified 有一个即可：有些 CDN 只给其中一个。
func (p PartState) HasEntityTag() bool {
	return strings.TrimSpace(p.ETag) != "" || strings.TrimSpace(p.LastModified) != ""
}

// ResumeAction 是「发起请求前」的本地决策。
type ResumeAction string

const (
	// ResumeActionFresh 表示本地没有字节，从头下载（仍带 `Range: bytes=0-` 做能力探测）。
	ResumeActionFresh ResumeAction = "fresh"

	// ResumeActionResume 表示本地有字节且有可信记录，带 `Range: bytes=<offset>-` 续传。
	ResumeActionResume ResumeAction = "resume"

	// ResumeActionRestart 表示本地有字节但无法安全续传，必须清空后从头下载。
	ResumeActionRestart ResumeAction = "restart"

	// ResumeActionComplete 表示本地字节已达期望大小，无需再下载（交 P3-06 校验）。
	ResumeActionComplete ResumeAction = "complete"
)

// ResumePlan 是一次传输的本地决策结果。
type ResumePlan struct {
	// Action 为决策类型。
	Action ResumeAction

	// Offset 为续传起点（Append 时的写入偏移）。
	Offset int64

	// RangeHeader 为要发送的 Range 头；为空表示不带该头。
	RangeHeader string

	// Reason 为内部原因（写日志用，不面向用户）。
	Reason string
}

// PlanResume 依据本地 `.part` 状态决定如何发起请求。
//
// 判定顺序（顺序即优先级，改动会直接影响续传安全性）：
//  1. 无本机字节 → Fresh（带 `Range: bytes=0-` 探测服务器是否支持续传）；
//  2. 本地字节超过期望大小 → Restart（数据不可信，宁可重下）；
//  3. 本地字节恰好等于期望大小 → Complete（不重复下载，交校验环节判定）；
//  4. 本地有字节但没有 ETag / Last-Modified 记录 → Restart（无法证明是同一个文件）；
//  5. 其余 → Resume（带 `Range: bytes=<size>-`）。
func PlanResume(part PartState, expectedSize int64) ResumePlan {
	size := part.Size
	if size <= 0 {
		return ResumePlan{
			Action:      ResumeActionFresh,
			Offset:      0,
			RangeHeader: "bytes=0-",
			Reason:      "本地没有已下载字节，从头下载并探测续传能力",
		}
	}
	if expectedSize > 0 && size > expectedSize {
		return ResumePlan{
			Action: ResumeActionRestart,
			Offset: 0,
			Reason: "本地已下载字节超过期望大小，数据不可信，全量重启",
		}
	}
	if expectedSize > 0 && size == expectedSize {
		return ResumePlan{
			Action: ResumeActionComplete,
			Offset: size,
			Reason: "本地已下载字节等于期望大小，无需重复下载",
		}
	}
	if !part.HasEntityTag() {
		return ResumePlan{
			Action: ResumeActionRestart,
			Offset: 0,
			Reason: "本地缺少 ETag / Last-Modified 记录，无法确认是同一个文件，全量重启",
		}
	}
	return ResumePlan{
		Action:      ResumeActionResume,
		Offset:      size,
		RangeHeader: "bytes=" + strconv.FormatInt(size, 10) + "-",
		Reason:      "本地有可信记录，从断点续传",
	}
}

// ContentRange 是 `Content-Range` 响应头的解析结果。
type ContentRange struct {
	// Start 为本次响应体的起始字节偏移。
	Start int64

	// End 为本次响应体的结束偏移；HasEnd 为 false 时无意义（形如 `bytes 0-1023/*`）。
	End int64

	// HasEnd 表示 End 是否有效。
	HasEnd bool

	// Total 为文件总大小；HasTotal 为 false 时表示服务器用 `*` 声明未知。
	Total int64

	// HasTotal 表示 Total 是否有效。
	HasTotal bool
}

// ParseContentRange 解析 `bytes <start>-<end>/<total>` 形式的响应头。
//
// 只接受本卡需要的这一种形态：单位必须是 bytes，结束值允许为 `*`，
// 总大小允许为 `*`。其他形态一律返回 false，由调用方按「无法确认」处理。
func ParseContentRange(raw string) (ContentRange, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ContentRange{}, false
	}
	const prefix = "bytes "
	if !strings.HasPrefix(strings.ToLower(value), prefix) {
		return ContentRange{}, false
	}
	rest := strings.TrimSpace(value[len(prefix):])
	slash := strings.Index(rest, "/")
	if slash <= 0 || slash == len(rest)-1 {
		return ContentRange{}, false
	}
	rangePart := strings.TrimSpace(rest[:slash])
	totalPart := strings.TrimSpace(rest[slash+1:])

	dash := strings.Index(rangePart, "-")
	if dash <= 0 {
		return ContentRange{}, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(rangePart[:dash]), 10, 64)
	if err != nil || start < 0 {
		return ContentRange{}, false
	}
	result := ContentRange{Start: start}

	endPart := strings.TrimSpace(rangePart[dash+1:])
	if endPart != "*" {
		end, endErr := strconv.ParseInt(endPart, 10, 64)
		if endErr != nil || end < start {
			return ContentRange{}, false
		}
		result.End = end
		result.HasEnd = true
	}
	if totalPart != "*" {
		total, totalErr := strconv.ParseInt(totalPart, 10, 64)
		if totalErr != nil || total < 0 {
			return ContentRange{}, false
		}
		result.Total = total
		result.HasTotal = true
	}
	return result, true
}

// RangeResponse 是判定响应行为所需的响应摘要（不含响应体与敏感头）。
type RangeResponse struct {
	// StatusCode 为 HTTP 状态码。
	StatusCode int

	// ContentRange 为原始 `Content-Range` 头。
	ContentRange string

	// ETag 为原始 `ETag` 头。
	ETag string

	// LastModified 为原始 `Last-Modified` 头。
	LastModified string

	// ContentLength 为原始 `Content-Length` 头，0 表示未知。
	ContentLength int64
}

// ResponseOutcome 是对一次响应的处理结论。
type ResponseOutcome string

const (
	// OutcomeAppend 表示从决策偏移继续追加写入。
	OutcomeAppend ResponseOutcome = "append"

	// OutcomeRewrite 表示丢弃已写字节，从 0 开始重写。
	OutcomeRewrite ResponseOutcome = "rewrite"

	// OutcomeComplete 表示本地已完整，本轮无需写入。
	OutcomeComplete ResponseOutcome = "complete"

	// OutcomeFail 表示本轮失败，按 Failure 的码与可重试性交给调度器。
	OutcomeFail ResponseOutcome = "fail"
)

// ResponseDecision 是响应判定的完整结果。
type ResponseDecision struct {
	// Outcome 为处理结论。
	Outcome ResponseOutcome

	// Offset 为 Append 时的写入起点。
	Offset int64

	// TotalSize 为本次确认的文件总大小（0 表示未知）。
	TotalSize int64

	// ETag / LastModified 为本次响应头，供后续续传使用。
	ETag         string
	LastModified string

	// Reason 为内部原因（写日志用）。
	Reason string

	// Failure 在 Outcome 为 OutcomeFail 时非空。
	Failure *Error
}

// DecideResponse 依据本地决策与服务器响应给出最终动作。
//
// 这是本卡最核心的判定（见交付报告中的「传输决策表」），
// 覆盖：200 忽略 Range、206 正常续传、206 不一致、416 已满、4xx、5xx。
func DecideResponse(plan ResumePlan, part PartState, resp RangeResponse, expectedSize int64) ResponseDecision {
	base := ResponseDecision{
		Offset:       plan.Offset,
		ETag:         strings.TrimSpace(resp.ETag),
		LastModified: strings.TrimSpace(resp.LastModified),
	}

	switch {
	case resp.StatusCode == 200:
		// 服务器返回完整文件。无论是不是响应 Range 请求，都必须从头写：
		// 追加会把完整文件拼在半文件后面，产生「看起来大小正确」的坏文件。
		base.Outcome = OutcomeRewrite
		base.Offset = 0
		base.TotalSize = resp.ContentLength
		if plan.Action == ResumeActionResume {
			base.Reason = "服务器忽略 Range 返回 200，丢弃已写字节后重写"
		} else {
			base.Reason = "服务器返回 200，从头写入"
		}
		return base

	case resp.StatusCode == 206:
		return decidePartial(plan, part, resp, expectedSize, base)

	case resp.StatusCode == 416:
		// 请求的起点已经越界：只有「本地已满」这一种情况可以接受。
		if part.Size > 0 && (expectedSize <= 0 || part.Size >= expectedSize) {
			base.Outcome = OutcomeComplete
			base.Offset = part.Size
			base.TotalSize = expectedSize
			base.Reason = "服务器报告范围不可满足，本地已下载完整"
			return base
		}
		base.Outcome = OutcomeRewrite
		base.Offset = 0
		base.Reason = "服务器报告范围不可满足且本地数据不完整，全量重启"
		return base

	case resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 410:
		base.Outcome = OutcomeFail
		base.Failure = newInputError(ErrorCodeSource, messageSourceRejected)
		base.Reason = "源端拒绝访问或文件不存在，不重试"
		return base

	case resp.StatusCode == 429 || resp.StatusCode >= 500:
		base.Outcome = OutcomeFail
		base.Failure = newExternalError(ErrorCodeNetwork, messageNetworkInterrupted, true)
		base.Reason = "服务端限流或临时故障，可重试"
		return base

	case resp.StatusCode >= 400:
		base.Outcome = OutcomeFail
		base.Failure = newInputError(ErrorCodeSource, messageSourceRejected)
		base.Reason = "源端返回不可重试的客户端错误"
		return base

	default:
		base.Outcome = OutcomeFail
		base.Failure = newExternalError(ErrorCodeNetwork, messageNetworkInterrupted, true)
		base.Reason = "收到不符合预期的状态码"
		return base
	}
}

// decidePartial 处理 206 响应：校验起始偏移、总大小与实体标记是否与本地记录一致。
func decidePartial(
	plan ResumePlan,
	part PartState,
	resp RangeResponse,
	expectedSize int64,
	base ResponseDecision,
) ResponseDecision {
	contentRange, parsed := ParseContentRange(resp.ContentRange)
	if !parsed {
		base.Outcome = OutcomeRewrite
		base.Offset = 0
		base.Reason = "206 响应缺少可解析的 Content-Range，无法确认续传位点，全量重启"
		return base
	}

	// 起始偏移必须等于我们请求的起点，否则响应体不是这段字节。
	if contentRange.Start != plan.Offset {
		base.Outcome = OutcomeRewrite
		base.Offset = 0
		base.Reason = "Content-Range 起始偏移与请求不一致，全量重启"
		return base
	}

	total := contentRange.Total
	if contentRange.HasTotal {
		base.TotalSize = total
		if expectedSize > 0 && total != expectedSize {
			base.Outcome = OutcomeRewrite
			base.Offset = 0
			base.Reason = "Content-Range 总大小与期望不一致，全量重启"
			return base
		}
	} else if expectedSize > 0 {
		base.TotalSize = expectedSize
	}

	// 续传才有实体标记可比：Fresh 时本地没有记录，比对没有意义。
	if plan.Action == ResumeActionResume {
		if !sameEntityTag(part, base.ETag, base.LastModified) {
			base.Outcome = OutcomeRewrite
			base.Offset = 0
			base.Reason = "ETag / Last-Modified 与本地记录不一致，全量重启"
			return base
		}
	}

	base.Outcome = OutcomeAppend
	base.Offset = plan.Offset
	base.Reason = "续传位点与实体标记一致，继续追加"
	return base
}

// sameEntityTag 比较响应实体标记与本地记录。
//
// 判定为「不一致」比「一致」更保守：只要响应给出了与记录不同的值，
// 就认为文件已变化。响应完全没给标记时沿用本地记录（部分镜像不透传这些头）。
func sameEntityTag(part PartState, etag string, lastModified string) bool {
	respETag := strings.TrimSpace(etag)
	respLastModified := strings.TrimSpace(lastModified)
	if respETag != "" && part.ETag != "" && !strings.EqualFold(respETag, strings.TrimSpace(part.ETag)) {
		return false
	}
	if respLastModified != "" && part.LastModified != "" &&
		!strings.EqualFold(respLastModified, strings.TrimSpace(part.LastModified)) {
		return false
	}
	return true
}
