package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestReadActiveLeasesWithoutFile 验证没有租约表时返回空集合。
func TestReadActiveLeasesWithoutFile(t *testing.T) {
	leases, err := ReadActiveLeases(t.TempDir())
	if err != nil {
		t.Fatalf("缺失租约表不应报错: %v", err)
	}
	if len(leases) != 0 {
		t.Fatalf("期望空集合，实际 %v", leases)
	}
}

// TestReadActiveLeasesReadsTable 验证从磁盘读出全部在用的版本。
//
// 该函数供**跨包只读查询**使用（实例侧判断能否删除版本）。它必须读磁盘
// 而不是内存快照：实例侧从不登记租约，内存里永远是空的。
func TestReadActiveLeasesReadsTable(t *testing.T) {
	root := t.TempDir()
	table := leaseTable{
		SchemaVersion: leaseSchemaVersion,
		Leases: []LeaseRecord{
			{VersionID: "26.3", PID: 100, ProcessStartMs: 1000},
			{VersionID: "26.3-forge-66.1.0", PID: 200, ProcessStartMs: 2000},
		},
	}
	encoded, err := json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(indexDirName), leaseFileName)
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		t.Fatal(mkErr)
	}
	if writeErr := os.WriteFile(path, encoded, 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	leases, readErr := ReadActiveLeases(root)
	if readErr != nil {
		t.Fatalf("读取失败: %v", readErr)
	}
	if len(leases) != 2 {
		t.Fatalf("期望 2 条租约，实际 %v", leases)
	}
	for _, want := range []string{"26.3", "26.3-forge-66.1.0"} {
		if _, ok := leases[want]; !ok {
			t.Fatalf("缺少 %q：%v", want, leases)
		}
	}
}

// TestReadActiveLeasesRejectsBrokenTable 验证损坏的租约表返回错误。
//
// 返回错误而不是空集合：调用方据此保守处理（按「仍在使用」处理），
// 空集合会让删除操作放行。
func TestReadActiveLeasesRejectsBrokenTable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(indexDirName), leaseFileName)
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		t.Fatal(mkErr)
	}
	if writeErr := os.WriteFile(path, []byte("{not json"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, err := ReadActiveLeases(root); err == nil {
		t.Fatal("损坏的租约表期望返回错误")
	}
}
