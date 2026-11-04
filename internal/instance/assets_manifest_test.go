package instance

import (
	"testing"
)

// 本文件覆盖「资源对象进清单」的规则。
//
// 规则：资源对象必须按索引的全部对象记录，而不是只记录本次下载的那些。
// ExpandAssetObjects 会跳过已存在的对象，因此在资源齐全的环境里安装第二个
// 版本时，若按「本次下载」记录，清单就不含任何资源——重建会把这些对象
// 当残留删掉，游戏随后找不到贴图与音效。

// TestExcludePaths 验证路径过滤保持顺序且不误伤。
func TestExcludePaths(t *testing.T) {
	paths := []string{"a.jar", "b.jar", "c.jar"}
	got := excludePaths(paths, []string{"b.jar"})
	if len(got) != 2 || got[0] != "a.jar" || got[1] != "c.jar" {
		t.Fatalf("过滤结果 = %v，期望 [a.jar c.jar]", got)
	}

	// 没有要排除的项时原样返回。
	if got := excludePaths(paths, nil); len(got) != 3 {
		t.Fatalf("空排除列表不应改动结果：%v", got)
	}

	// 排除不存在的路径不应影响其余项。
	if got := excludePaths(paths, []string{"absent.jar"}); len(got) != 3 {
		t.Fatalf("排除不存在的路径不应改变数量：%v", got)
	}

	// 全部排除 → 空切片，不能是 nil（下游按长度判断）。
	got = excludePaths(paths, paths)
	if got == nil || len(got) != 0 {
		t.Fatalf("全部排除应返回空切片，实际 %v", got)
	}
}
