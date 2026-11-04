package runtime

import (
	"testing"
)

// 本文件覆盖「按版本要求的 Java 大版本自动匹配」的选型逻辑。
//
// 选型表只关心「挑哪个运行时」这一层，不启动真实 Java 进程。

// TestPickRuntimeForMajorPrefersExactMatch 验证优先选大版本完全相等的运行时。
func TestPickRuntimeForMajorPrefersExactMatch(t *testing.T) {
	rows := []runtimeCandidate{
		{id: "java-25", major: 25, full: "25.0.1"},
		{id: "java-21", major: 21, full: "21.0.2"},
		{id: "java-8", major: 8, full: "1.8.0_401"},
	}
	if got := pickRuntimeForMajor(rows, "", 21); got != "java-21" {
		t.Fatalf("要求 21 时应选 java-21，实际 %q", got)
	}
}

// TestPickRuntimeForMajorUsesSmallestHigherVersion 验证没有精确匹配时取最小的更高版本。
//
// Java 向后兼容：用 25 跑要求 21 的游戏通常可用，但绝不能用低于要求的版本。
func TestPickRuntimeForMajorUsesSmallestHigherVersion(t *testing.T) {
	rows := []runtimeCandidate{
		{id: "java-8", major: 8, full: "1.8.0_401"},
		{id: "java-25", major: 25, full: "25.0.1"},
		{id: "java-17", major: 17, full: "17.0.9"},
	}
	if got := pickRuntimeForMajor(rows, "", 21); got != "java-25" {
		t.Fatalf("要求 21 且只有 17/25 时应选 25（不能降级），实际 %q", got)
	}
	if got := pickRuntimeForMajor(rows, "", 8); got != "java-8" {
		t.Fatalf("要求 8 时应选 java-8，实际 %q", got)
	}
}

// TestPickRuntimeForMajorNeverDowngrades 验证不会选低于要求的版本。
//
// 目标版本 8 太少时，宁可报错也不能拿 Java 8 去跑要求 Java 21 的游戏。
func TestPickRuntimeForMajorNeverDowngrades(t *testing.T) {
	rows := []runtimeCandidate{
		{id: "java-8", major: 8, full: "1.8.0_401"},
	}
	if got := pickRuntimeForMajor(rows, "", 21); got != "" {
		t.Fatalf("只有更低版本时应返回空，实际 %q", got)
	}
}

// TestPickRuntimeForMajorHonorsSelected 验证同大版本时优先当前已选运行时。
func TestPickRuntimeForMajorHonorsSelected(t *testing.T) {
	rows := []runtimeCandidate{
		{id: "java-21-a", major: 21, full: "21.0.1"},
		{id: "java-21-b", major: 21, full: "21.0.5"},
	}
	// 未选中时取版本号更高的。
	if got := pickRuntimeForMajor(rows, "", 21); got != "java-21-b" {
		t.Fatalf("未选中时应取最高补丁版本，实际 %q", got)
	}
	// 已选中时尊重用户选择，即使它补丁版本更低。
	if got := pickRuntimeForMajor(rows, "java-21-a", 21); got != "java-21-a" {
		t.Fatalf("已选中时应尊重选择，实际 %q", got)
	}
}

// TestPickRuntimeForMajorWithoutCandidates 验证没有候选时返回空。
func TestPickRuntimeForMajorWithoutCandidates(t *testing.T) {
	if got := pickRuntimeForMajor(nil, "", 21); got != "" {
		t.Fatalf("没有候选时应返回空，实际 %q", got)
	}
}

// TestCompareJavaFullVersion 验证版本号比较忽略构建号与后缀。
func TestCompareJavaFullVersion(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"21.0.5", "21.0.1", 1},
		{"21.0.1", "21.0.5", -1},
		{"21.0.1", "21.0.1", 0},
		{"21.0.1+12", "21.0.1", 0},
		{"25.0.1", "21.0.9", 1},
		{"1.8.0_401", "1.8.0_392", 1},
	}
	for _, item := range cases {
		got := compareJavaFullVersion(item.left, item.right)
		if normalizeSign(got) != item.want {
			t.Fatalf("compareJavaFullVersion(%q, %q) = %d，期望符号 %d",
				item.left, item.right, got, item.want)
		}
	}
}

// normalizeSign 把比较结果归一化为 -1 / 0 / 1。
func normalizeSign(value int) int {
	switch {
	case value < 0:
		return -1
	case value > 0:
		return 1
	default:
		return 0
	}
}
