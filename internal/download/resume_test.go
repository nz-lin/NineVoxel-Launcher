// 本文件测试断点续传决策（P3-05 第 5~8 条）：本地决策、Content-Range 解析与
// 「本地状态 × 服务器响应」的完整决策表。
//
// 全部纯函数测试：不联网、不写文件、不读时钟。
package download

import "testing"

// TestPlanResumeBranches 覆盖本地 .part 状态到请求决策的映射。
func TestPlanResumeBranches(t *testing.T) {
	cases := []struct {
		name         string
		part         PartState
		expectedSize int64
		wantAction   ResumeAction
		wantRange    string
		wantOffset   int64
	}{
		{
			name:         "无本地字节走探测",
			part:         PartState{},
			expectedSize: 100,
			wantAction:   ResumeActionFresh,
			wantRange:    "bytes=0-",
			wantOffset:   0,
		},
		{
			name:         "本地字节超过期望全量重启",
			part:         PartState{Size: 120, ETag: `"a"`},
			expectedSize: 100,
			wantAction:   ResumeActionRestart,
			wantRange:    "",
			wantOffset:   0,
		},
		{
			name:         "本地字节恰好等于期望视为完成",
			part:         PartState{Size: 100, ETag: `"a"`},
			expectedSize: 100,
			wantAction:   ResumeActionComplete,
			wantRange:    "",
			wantOffset:   100,
		},
		{
			name:         "有字节但无实体标记必须重启",
			part:         PartState{Size: 40},
			expectedSize: 100,
			wantAction:   ResumeActionRestart,
			wantRange:    "",
			wantOffset:   0,
		},
		{
			name:         "有字节且有 ETag 走续传",
			part:         PartState{Size: 40, ETag: `"a"`},
			expectedSize: 100,
			wantAction:   ResumeActionResume,
			wantRange:    "bytes=40-",
			wantOffset:   40,
		},
		{
			name:         "只有 Last-Modified 也可续传",
			part:         PartState{Size: 40, LastModified: "Wed, 21 Oct 2015 07:28:00 GMT"},
			expectedSize: 100,
			wantAction:   ResumeActionResume,
			wantRange:    "bytes=40-",
			wantOffset:   40,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			plan := PlanResume(testCase.part, testCase.expectedSize)
			if plan.Action != testCase.wantAction {
				t.Fatalf("Action = %q，期望 %q", plan.Action, testCase.wantAction)
			}
			if plan.RangeHeader != testCase.wantRange {
				t.Fatalf("RangeHeader = %q，期望 %q", plan.RangeHeader, testCase.wantRange)
			}
			if plan.Offset != testCase.wantOffset {
				t.Fatalf("Offset = %d，期望 %d", plan.Offset, testCase.wantOffset)
			}
			if plan.Reason == "" {
				t.Fatalf("Reason 不应为空：决策需要可解释")
			}
		})
	}
}

// TestParseContentRange 覆盖 Content-Range 的接受与拒绝。
func TestParseContentRange(t *testing.T) {
	t.Run("完整形态", func(t *testing.T) {
		parsed, ok := ParseContentRange("bytes 10-99/1000")
		if !ok {
			t.Fatalf("解析失败")
		}
		if parsed.Start != 10 || !parsed.HasEnd || parsed.End != 99 || !parsed.HasTotal || parsed.Total != 1000 {
			t.Fatalf("解析结果不符：%+v", parsed)
		}
	})

	t.Run("结束值为星号", func(t *testing.T) {
		parsed, ok := ParseContentRange("bytes 0-*/100")
		if !ok {
			t.Fatalf("解析失败")
		}
		if parsed.HasEnd {
			t.Fatalf("HasEnd 应为 false")
		}
		if !parsed.HasTotal || parsed.Total != 100 {
			t.Fatalf("总大小不符：%+v", parsed)
		}
	})

	t.Run("总大小未知", func(t *testing.T) {
		parsed, ok := ParseContentRange("bytes 0-9/*")
		if !ok {
			t.Fatalf("解析失败")
		}
		if parsed.HasTotal {
			t.Fatalf("HasTotal 应为 false")
		}
	})

	rejected := []string{
		"",
		"bytes",
		"items 0-1/2",
		"bytes abc-1/2",
		"bytes 5-1/2",
		"bytes 0-1/",
		"/2",
	}
	for _, raw := range rejected {
		if _, ok := ParseContentRange(raw); ok {
			t.Fatalf("ParseContentRange(%q) 应被拒绝", raw)
		}
	}
}

// TestDecideResponseTable 是交付报告「传输决策表」的机器版本：
// 每一行的「本地状态 × 服务器响应 → 动作」都必须与文档一致。
func TestDecideResponseTable(t *testing.T) {
	const expectedSize = int64(100)

	cases := []struct {
		name       string
		plan       ResumePlan
		part       PartState
		response   RangeResponse
		wantAction ResponseOutcome
		wantOffset int64
		wantCode   string
	}{
		{
			name:       "本地为空服务器返回200全量写",
			plan:       ResumePlan{Action: ResumeActionFresh, Offset: 0},
			part:       PartState{},
			response:   RangeResponse{StatusCode: 200, ContentLength: 100},
			wantAction: OutcomeRewrite,
			wantOffset: 0,
		},
		{
			name:       "本地有字节服务器忽略Range返回200必须重写",
			plan:       ResumePlan{Action: ResumeActionResume, Offset: 40},
			part:       PartState{Size: 40, ETag: `"a"`},
			response:   RangeResponse{StatusCode: 200, ContentLength: 100},
			wantAction: OutcomeRewrite,
			wantOffset: 0,
		},
		{
			name:       "206且位点与实体标记一致续传追加",
			plan:       ResumePlan{Action: ResumeActionResume, Offset: 40},
			part:       PartState{Size: 40, ETag: `"a"`},
			response:   RangeResponse{StatusCode: 206, ContentRange: "bytes 40-99/100", ETag: `"a"`},
			wantAction: OutcomeAppend,
			wantOffset: 40,
		},
		{
			name:       "206起始偏移不一致必须重写",
			plan:       ResumePlan{Action: ResumeActionResume, Offset: 40},
			part:       PartState{Size: 40, ETag: `"a"`},
			response:   RangeResponse{StatusCode: 206, ContentRange: "bytes 0-99/100", ETag: `"a"`},
			wantAction: OutcomeRewrite,
			wantOffset: 0,
		},
		{
			name:       "206总大小不一致必须重写",
			plan:       ResumePlan{Action: ResumeActionResume, Offset: 40},
			part:       PartState{Size: 40, ETag: `"a"`},
			response:   RangeResponse{StatusCode: 206, ContentRange: "bytes 40-199/200", ETag: `"a"`},
			wantAction: OutcomeRewrite,
			wantOffset: 0,
		},
		{
			name:       "206实体标记变化必须重写",
			plan:       ResumePlan{Action: ResumeActionResume, Offset: 40},
			part:       PartState{Size: 40, ETag: `"a"`},
			response:   RangeResponse{StatusCode: 206, ContentRange: "bytes 40-99/100", ETag: `"b"`},
			wantAction: OutcomeRewrite,
			wantOffset: 0,
		},
		{
			name:       "206缺少Content-Range无法确认必须重写",
			plan:       ResumePlan{Action: ResumeActionFresh, Offset: 0},
			part:       PartState{},
			response:   RangeResponse{StatusCode: 206},
			wantAction: OutcomeRewrite,
			wantOffset: 0,
		},
		{
			name:       "206首次探测且位点为0继续追加",
			plan:       ResumePlan{Action: ResumeActionFresh, Offset: 0},
			part:       PartState{},
			response:   RangeResponse{StatusCode: 206, ContentRange: "bytes 0-99/100"},
			wantAction: OutcomeAppend,
			wantOffset: 0,
		},
		{
			name:       "416本地已满视为完成",
			plan:       ResumePlan{Action: ResumeActionResume, Offset: 100},
			part:       PartState{Size: 100, ETag: `"a"`},
			response:   RangeResponse{StatusCode: 416},
			wantAction: OutcomeComplete,
			wantOffset: 100,
		},
		{
			name:       "416本地不完整必须重写",
			plan:       ResumePlan{Action: ResumeActionResume, Offset: 40},
			part:       PartState{Size: 40, ETag: `"a"`},
			response:   RangeResponse{StatusCode: 416},
			wantAction: OutcomeRewrite,
			wantOffset: 0,
		},
		{
			name:       "403不可重试失败",
			plan:       ResumePlan{Action: ResumeActionFresh, Offset: 0},
			part:       PartState{},
			response:   RangeResponse{StatusCode: 403},
			wantAction: OutcomeFail,
			wantCode:   ErrorCodeSource,
		},
		{
			name:       "404不可重试失败",
			plan:       ResumePlan{Action: ResumeActionFresh, Offset: 0},
			part:       PartState{},
			response:   RangeResponse{StatusCode: 404},
			wantAction: OutcomeFail,
			wantCode:   ErrorCodeSource,
		},
		{
			name:       "410不可重试失败",
			plan:       ResumePlan{Action: ResumeActionFresh, Offset: 0},
			part:       PartState{},
			response:   RangeResponse{StatusCode: 410},
			wantAction: OutcomeFail,
			wantCode:   ErrorCodeSource,
		},
		{
			name:       "429可重试失败",
			plan:       ResumePlan{Action: ResumeActionFresh, Offset: 0},
			part:       PartState{},
			response:   RangeResponse{StatusCode: 429},
			wantAction: OutcomeFail,
			wantCode:   ErrorCodeNetwork,
		},
		{
			name:       "503可重试失败",
			plan:       ResumePlan{Action: ResumeActionFresh, Offset: 0},
			part:       PartState{},
			response:   RangeResponse{StatusCode: 503},
			wantAction: OutcomeFail,
			wantCode:   ErrorCodeNetwork,
		},
		{
			name:       "418客户端错误不可重试",
			plan:       ResumePlan{Action: ResumeActionFresh, Offset: 0},
			part:       PartState{},
			response:   RangeResponse{StatusCode: 418},
			wantAction: OutcomeFail,
			wantCode:   ErrorCodeSource,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			decision := DecideResponse(testCase.plan, testCase.part, testCase.response, expectedSize)
			if decision.Outcome != testCase.wantAction {
				t.Fatalf("Outcome = %q，期望 %q（原因：%s）", decision.Outcome, testCase.wantAction, decision.Reason)
			}
			if testCase.wantAction != OutcomeFail && decision.Offset != testCase.wantOffset {
				t.Fatalf("Offset = %d，期望 %d", decision.Offset, testCase.wantOffset)
			}
			if testCase.wantCode != "" {
				if decision.Failure == nil {
					t.Fatalf("OutcomeFail 必须带 Failure")
				}
				if decision.Failure.Code != testCase.wantCode {
					t.Fatalf("Failure.Code = %q，期望 %q", decision.Failure.Code, testCase.wantCode)
				}
			}
			if decision.Reason == "" {
				t.Fatalf("Reason 不应为空")
			}
		})
	}
}

// TestDecideResponseMarksRetryability 验证可重试性标注与错误码一致。
func TestDecideResponseMarksRetryability(t *testing.T) {
	plan := ResumePlan{Action: ResumeActionFresh, Offset: 0}

	retryable := DecideResponse(plan, PartState{}, RangeResponse{StatusCode: 503}, 100)
	if retryable.Failure == nil || !retryable.Failure.Retryable {
		t.Fatalf("503 应标记为可重试")
	}

	fatal := DecideResponse(plan, PartState{}, RangeResponse{StatusCode: 404}, 100)
	if fatal.Failure == nil || fatal.Failure.Retryable {
		t.Fatalf("404 不应标记为可重试")
	}
}
