package instance

import (
	"errors"
	"strings"
	"testing"
)

// TestDescribeRenameFailure 验证目录改名的失败归因。
//
// 这两类原因（占用 / 权限）的处置方式完全不同：前者让用户先关游戏，
// 后者要改目录权限。只回一句「归档失败」用户无从下手。
func TestDescribeRenameFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "Windows 占用",
			err:  errors.New("rename C:\\a C:\\b: The process cannot access the file because it is being used by another process."),
			want: "仍被占用",
		},
		{
			name: "POSIX 占用",
			err:  errors.New("rename /a /b: device or resource busy"),
			want: "仍被占用",
		},
		{
			name: "Windows 权限",
			err:  errors.New("rename C:\\a C:\\b: Access is denied."),
			want: "写入权限",
		},
		{
			name: "跨卷",
			err:  errors.New("rename /a /b: invalid cross-device link"),
			want: "同一磁盘卷",
		},
		{
			name: "未知原因",
			err:  errors.New("something unexpected"),
			want: "无法移动",
		},
	}
	for _, item := range cases {
		got := describeRenameFailure(item.err)
		if !strings.Contains(got, item.want) {
			t.Fatalf("%s：归因 = %q，期望包含 %q", item.name, got, item.want)
		}
	}
	if got := describeRenameFailure(nil); got != "" {
		t.Fatalf("nil 错误应返回空串，实际 %q", got)
	}
}
