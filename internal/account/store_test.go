package account

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"example.com/ninevoxel/launcher/internal/database"
)

// storeTestProfileID 生成 32 位小写十六进制档案 UUID（满足契约要求的形式）。
func storeTestProfileID(seed byte) string {
	return strings.Repeat(string("0123456789abcdef"[seed%16]), 32)
}

// storeTestAccount 构造一个带完整凭证的微软账号样本。
//
// id 为本地账号标识；seed 决定档案 UUID 与展示名，保证同一测试内身份不重复。
func storeTestAccount(id string, seed byte) StoredAccount {
	name := fmt.Sprintf("Player%d", seed)
	return StoredAccount{
		Account: Account{
			ID:          id,
			Kind:        KindMicrosoft,
			ProviderID:  "microsoft",
			DisplayName: name,
			Profile: Profile{
				ID:        storeTestProfileID(seed),
				Name:      name,
				SkinModel: "classic",
				SkinURL:   "https://example.invalid/skin.png",
				CapeURL:   "https://example.invalid/cape.png",
			},
			CreatedAtMs:  1000,
			UpdatedAtMs:  1000,
			LastUsedAtMs: 0,
			Selected:     false,
		},
		Credentials: Credentials{
			AccessToken:  "access-" + id,
			ClientToken:  "client-" + id,
			RefreshToken: "refresh-" + id,
			ExpiresAtMs:  1735689600000,
			UserType:     UserTypeMSA,
			XUID:         "xuid-" + id,
		},
	}
}

// storeTestOfflineAccount 构造一个没有任何凭证的离线账号样本（验证 NULL 凭证列）。
func storeTestOfflineAccount(id string, seed byte) StoredAccount {
	name := fmt.Sprintf("Offline%d", seed)
	return StoredAccount{
		Account: Account{
			ID:          id,
			Kind:        KindOffline,
			ProviderID:  "",
			DisplayName: name,
			Profile: Profile{
				ID:   storeTestProfileID(seed),
				Name: name,
			},
			CreatedAtMs: 2000,
			UpdatedAtMs: 2000,
		},
	}
}

// openTestAccountStore 在 t.TempDir() 下打开一个真实文件数据库的 Store。
//
// 返回 Store 与数据库路径；测试结束自动 Close。测试全程不联网。
func openTestAccountStore(t *testing.T) (Store, string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.db")
	cipherInstance := newTestCipher(t, 0x90)
	store, storeErr := OpenSQLiteStore(path, cipherInstance)
	if storeErr != nil {
		t.Fatalf("打开账号存储失败: %v", storeErr)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

// requireStoreError 断言 err 是带指定错误码的 *Error。
func requireStoreError(t *testing.T, err error, wantCode string) *Error {
	t.Helper()
	if err == nil {
		t.Fatalf("期望返回错误码 %s，实际 nil", wantCode)
	}
	var accountErr *Error
	if !errors.As(err, &accountErr) {
		t.Fatalf("错误类型期望 *Error，实际 %T: %v", err, err)
	}
	if accountErr.Code != wantCode {
		t.Fatalf("错误码期望 %s，实际 %s（%v）", wantCode, accountErr.Code, accountErr)
	}
	return accountErr
}

// requireStoreOK 断言 err 为 nil。
func requireStoreOK(t *testing.T, err error, action string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s失败: %v", action, err)
	}
}

// findStoredAccount 在列表中按 id 查找，找不到时终止测试。
func findStoredAccount(t *testing.T, items []StoredAccount, id string) StoredAccount {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("列表中找不到账号 %s，实际返回 %d 个账号", id, len(items))
	return StoredAccount{}
}

// TestStoreCRUD 验证账号的增删改查与空列表行为。
func TestStoreCRUD(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestAccountStore(t)

	// 空列表必须可用（返回空切片，不是 nil）。
	empty, err := store.List(ctx)
	requireStoreOK(t, err, "读取空账号列表")
	if empty == nil {
		t.Fatal("空列表期望非 nil 空切片")
	}
	if len(empty) != 0 {
		t.Fatalf("空列表期望 0 条，实际 %d", len(empty))
	}

	item := storeTestAccount("acc-crud-1", 1)
	item.CreatedAtMs = 1000
	item.UpdatedAtMs = 1500
	requireStoreOK(t, store.Upsert(ctx, item), "写入账号")

	got, err := store.Get(ctx, item.ID)
	requireStoreOK(t, err, "读取账号")
	if got == nil {
		t.Fatal("Get 返回 nil")
	}
	if got.ID != item.ID || got.Kind != item.Kind || got.ProviderID != item.ProviderID {
		t.Fatalf("账号身份字段不一致: %+v", got.Account)
	}
	if got.DisplayName != item.DisplayName {
		t.Fatalf("展示名期望 %q，实际 %q", item.DisplayName, got.DisplayName)
	}
	if got.Profile != item.Profile {
		t.Fatalf("档案不一致：期望 %+v，实际 %+v", item.Profile, got.Profile)
	}
	if got.CreatedAtMs != item.CreatedAtMs || got.UpdatedAtMs != item.UpdatedAtMs {
		t.Fatalf("时间戳不一致: created=%d updated=%d", got.CreatedAtMs, got.UpdatedAtMs)
	}
	if got.LastUsedAtMs != 0 || got.Selected {
		t.Fatalf("新账号不应被选中或标记使用: lastUsed=%d selected=%v", got.LastUsedAtMs, got.Selected)
	}
	if got.Credentials != item.Credentials {
		// 失败信息刻意不回显凭证内容（测试输出同样不得打印 token）。
		t.Fatal("凭证往返不一致（凭证内容已隐去）")
	}

	// 同时覆盖「没有凭证」的离线账号：空凭证必须以 NULL 落库并读回空凭证。
	offline := storeTestOfflineAccount("acc-crud-offline", 9)
	requireStoreOK(t, store.Upsert(ctx, offline), "写入离线账号")
	gotOffline, err := store.Get(ctx, offline.ID)
	requireStoreOK(t, err, "读取离线账号")
	if gotOffline.Credentials != (Credentials{}) {
		t.Fatal("离线账号凭证期望为空")
	}

	list, err := store.List(ctx)
	requireStoreOK(t, err, "读取账号列表")
	if len(list) != 2 {
		t.Fatalf("列表期望 2 条，实际 %d", len(list))
	}

	// 删除存在的账号。
	requireStoreOK(t, store.Delete(ctx, item.ID), "删除账号")
	_, err = store.Get(ctx, item.ID)
	requireStoreError(t, err, ErrorCodeNotFound)

	// 删除不存在的账号 → ACCOUNT_NOT_FOUND_001。
	requireStoreError(t, store.Delete(ctx, "acc-not-exist"), ErrorCodeNotFound)

	// Get 不存在的账号 → ACCOUNT_NOT_FOUND_001。
	_, err = store.Get(ctx, "acc-not-exist")
	requireStoreError(t, err, ErrorCodeNotFound)
}

// TestStoreListOrdering 验证列表排序：选中优先 → 最近使用倒序 → 创建时间正序。
func TestStoreListOrdering(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestAccountStore(t)

	accountA := storeTestAccount("acc-order-a", 1)
	accountA.CreatedAtMs = 100
	accountB := storeTestAccount("acc-order-b", 2)
	accountB.CreatedAtMs = 200
	accountC := storeTestAccount("acc-order-c", 3)
	accountC.CreatedAtMs = 300

	for _, item := range []StoredAccount{accountA, accountB, accountC} {
		requireStoreOK(t, store.Upsert(ctx, item), "写入排序账号")
	}

	// C 被选中（last_used=5000）；B、A 分别被使用于 7000、6000。
	requireStoreOK(t, store.SetSelected(ctx, accountC.ID, 5000), "选中 C")
	requireStoreOK(t, store.Touch(ctx, accountB.ID, 7000), "更新 B 使用时间")
	requireStoreOK(t, store.Touch(ctx, accountA.ID, 6000), "更新 A 使用时间")

	list, err := store.List(ctx)
	requireStoreOK(t, err, "读取排序列表")
	if len(list) != 3 {
		t.Fatalf("列表期望 3 条，实际 %d", len(list))
	}

	wantOrder := []string{accountC.ID, accountB.ID, accountA.ID}
	for i, want := range wantOrder {
		if list[i].ID != want {
			ids := make([]string, 0, len(list))
			for _, item := range list {
				ids = append(ids, item.ID)
			}
			t.Fatalf("排序期望 %v，实际 %v", wantOrder, ids)
		}
	}
	if !list[0].Selected || list[1].Selected || list[2].Selected {
		t.Fatalf("选中标记不符合预期: %v/%v/%v", list[0].Selected, list[1].Selected, list[2].Selected)
	}
	if list[0].LastUsedAtMs != 5000 || list[1].LastUsedAtMs != 7000 || list[2].LastUsedAtMs != 6000 {
		t.Fatalf("最近使用时间不符合预期: %d/%d/%d",
			list[0].LastUsedAtMs, list[1].LastUsedAtMs, list[2].LastUsedAtMs)
	}
}

// TestStoreSetSelectedIsExclusive 验证全局只选中一个账号，且失败时回滚。
func TestStoreSetSelectedIsExclusive(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestAccountStore(t)

	accountA := storeTestAccount("acc-sel-a", 1)
	accountB := storeTestAccount("acc-sel-b", 2)
	for _, item := range []StoredAccount{accountA, accountB} {
		requireStoreOK(t, store.Upsert(ctx, item), "写入账号")
	}

	// 先选中 A。
	requireStoreOK(t, store.SetSelected(ctx, accountA.ID, 111), "选中 A")
	gotA, err := store.Get(ctx, accountA.ID)
	requireStoreOK(t, err, "读取 A")
	if !gotA.Selected || gotA.LastUsedAtMs != 111 {
		t.Fatalf("A 期望选中且 last_used=111，实际 selected=%v lastUsed=%d",
			gotA.Selected, gotA.LastUsedAtMs)
	}

	// 再选中 B：A 必须自动取消选中，且 A 的 last_used 保留。
	requireStoreOK(t, store.SetSelected(ctx, accountB.ID, 222), "选中 B")
	gotA, err = store.Get(ctx, accountA.ID)
	requireStoreOK(t, err, "再次读取 A")
	gotB, err := store.Get(ctx, accountB.ID)
	requireStoreOK(t, err, "读取 B")
	if gotA.Selected {
		t.Fatal("选中 B 后 A 仍处于选中状态")
	}
	if gotA.LastUsedAtMs != 111 {
		t.Fatalf("取消选中不应改写 A 的 last_used，期望 111，实际 %d", gotA.LastUsedAtMs)
	}
	if !gotB.Selected || gotB.LastUsedAtMs != 222 {
		t.Fatalf("B 期望选中且 last_used=222，实际 selected=%v lastUsed=%d",
			gotB.Selected, gotB.LastUsedAtMs)
	}

	// 全局选中数必须恰好为 1。
	list, err := store.List(ctx)
	requireStoreOK(t, err, "读取列表")
	selectedCount := 0
	for _, item := range list {
		if item.Selected {
			selectedCount++
		}
	}
	if selectedCount != 1 {
		t.Fatalf("全局选中数期望 1，实际 %d", selectedCount)
	}

	// 选中不存在的 id → 回滚事务，B 仍是当前账号。
	requireStoreError(t, store.SetSelected(ctx, "acc-missing", 333), ErrorCodeNotFound)
	gotB, err = store.Get(ctx, accountB.ID)
	requireStoreOK(t, err, "回滚后读取 B")
	if !gotB.Selected || gotB.LastUsedAtMs != 222 {
		t.Fatalf("失败的 SetSelected 不应改变 B：selected=%v lastUsed=%d",
			gotB.Selected, gotB.LastUsedAtMs)
	}

	// 空 id 同样按不存在处理。
	requireStoreError(t, store.SetSelected(ctx, "", 444), ErrorCodeNotFound)
}

// TestStoreUpsertDedupPreservesCreatedAt 验证身份去重、保留 created_at/is_selected。
func TestStoreUpsertDedupPreservesCreatedAt(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestAccountStore(t)

	first := storeTestAccount("acc-dedup-1", 4)
	first.CreatedAtMs = 1000
	first.UpdatedAtMs = 1000
	requireStoreOK(t, store.Upsert(ctx, first), "首次写入")

	// 同一身份（kind, provider_id, profile_id）、新的资料与凭证：
	// created_at 必须保留，资料/凭证/updated_at 必须更新。
	second := first
	second.DisplayName = "Renamed玩家"
	second.Profile.Name = "Renamed"
	second.CreatedAtMs = 9999
	second.UpdatedAtMs = 2000
	second.LastUsedAtMs = 8888
	second.Selected = true
	second.Credentials.AccessToken = "access-rotated"
	requireStoreOK(t, store.Upsert(ctx, second), "重复身份写入")

	list, err := store.List(ctx)
	requireStoreOK(t, err, "读取列表")
	if len(list) != 1 {
		t.Fatalf("同一身份期望只保留 1 行，实际 %d 行", len(list))
	}

	got := list[0]
	if got.ID != first.ID {
		t.Fatalf("冲突更新后应保留首次 id %q，实际 %q", first.ID, got.ID)
	}
	if got.CreatedAtMs != 1000 {
		t.Fatalf("created_at 期望保留 1000，实际 %d", got.CreatedAtMs)
	}
	if got.UpdatedAtMs != 2000 {
		t.Fatalf("updated_at 期望更新为 2000，实际 %d", got.UpdatedAtMs)
	}
	if got.DisplayName != "Renamed玩家" || got.Profile.Name != "Renamed" {
		t.Fatalf("资料未更新: displayName=%q profileName=%q", got.DisplayName, got.Profile.Name)
	}
	if got.Credentials.AccessToken != "access-rotated" {
		t.Fatal("凭证未在重复 Upsert 时更新（凭证内容已隐去）")
	}
	// 选中态与 last_used 不由 Upsert 改写：保持首次写入的值（false / 0）。
	if got.Selected {
		t.Fatal("Upsert 不应改写选中态（选中态由 SetSelected 维护）")
	}
	if got.LastUsedAtMs != 0 {
		t.Fatalf("Upsert 不应改写 last_used，实际 %d", got.LastUsedAtMs)
	}

	// 用不同本地 id 但同一身份再次写入：仍然只更新原行，不产生新行。
	third := first
	third.ID = "acc-dedup-shifted"
	third.Credentials.AccessToken = "access-third"
	requireStoreOK(t, store.Upsert(ctx, third), "不同本地 id 同一身份写入")

	list, err = store.List(ctx)
	requireStoreOK(t, err, "再次读取列表")
	if len(list) != 1 || list[0].ID != first.ID {
		t.Fatalf("身份去重失效: len=%d id=%q", len(list), list[0].ID)
	}
	if list[0].Credentials.AccessToken != "access-third" {
		t.Fatal("凭证未按第三次写入更新（凭证内容已隐去）")
	}
	_, err = store.Get(ctx, "acc-dedup-shifted")
	requireStoreError(t, err, ErrorCodeNotFound)

	// 已选中的账号被 Upsert 更新后，选中状态必须保留。
	requireStoreOK(t, store.SetSelected(ctx, first.ID, 4000), "选中账号")
	fourth := first
	fourth.Credentials.AccessToken = "access-fourth"
	requireStoreOK(t, store.Upsert(ctx, fourth), "选中后再次写入")

	gotAfter, err := store.Get(ctx, first.ID)
	requireStoreOK(t, err, "读取更新后的账号")
	if !gotAfter.Selected {
		t.Fatal("Upsert 后选中状态丢失")
	}
	if gotAfter.LastUsedAtMs != 4000 {
		t.Fatalf("Upsert 后 last_used 期望保留 4000，实际 %d", gotAfter.LastUsedAtMs)
	}
}

// TestStoreDeleteSelected 验证删除当前选中账号后的状态。
//
// 契约：Delete 只删行，不自动改选；由服务层随后调用 SetSelected。
func TestStoreDeleteSelected(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestAccountStore(t)

	accountA := storeTestAccount("acc-del-a", 1)
	accountB := storeTestAccount("acc-del-b", 2)
	for _, item := range []StoredAccount{accountA, accountB} {
		requireStoreOK(t, store.Upsert(ctx, item), "写入账号")
	}
	requireStoreOK(t, store.SetSelected(ctx, accountA.ID, 100), "选中 A")

	// 删除当前选中账号。
	requireStoreOK(t, store.Delete(ctx, accountA.ID), "删除选中账号")
	_, err := store.Get(ctx, accountA.ID)
	requireStoreError(t, err, ErrorCodeNotFound)

	list, err := store.List(ctx)
	requireStoreOK(t, err, "读取列表")
	if len(list) != 1 || list[0].ID != accountB.ID {
		t.Fatalf("删除后期望只剩 B: %+v", list)
	}
	if list[0].Selected {
		t.Fatal("删除当前选中账号后不应留下悬空选中")
	}

	// 服务层语义：删除后自行选中剩余账号。
	requireStoreOK(t, store.SetSelected(ctx, accountB.ID, 200), "选中剩余账号")
	gotB, err := store.Get(ctx, accountB.ID)
	requireStoreOK(t, err, "读取 B")
	if !gotB.Selected || gotB.LastUsedAtMs != 200 {
		t.Fatalf("剩余账号应被选中: selected=%v lastUsed=%d", gotB.Selected, gotB.LastUsedAtMs)
	}

	// 删光所有账号后列表为空，选中数为 0。
	requireStoreOK(t, store.Delete(ctx, accountB.ID), "删除最后一个账号")
	list, err = store.List(ctx)
	requireStoreOK(t, err, "读取空列表")
	if len(list) != 0 {
		t.Fatalf("删除全部后列表期望为空，实际 %d 条", len(list))
	}
}

// TestStoreTouch 验证 Touch 只更新最近使用时间，不改选中态与资料。
func TestStoreTouch(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestAccountStore(t)

	item := storeTestAccount("acc-touch-1", 1)
	item.CreatedAtMs = 1000
	item.UpdatedAtMs = 1000
	requireStoreOK(t, store.Upsert(ctx, item), "写入账号")

	requireStoreOK(t, store.Touch(ctx, item.ID, 1234), "更新使用时间")
	got, err := store.Get(ctx, item.ID)
	requireStoreOK(t, err, "读取账号")
	if got.LastUsedAtMs != 1234 {
		t.Fatalf("last_used 期望 1234，实际 %d", got.LastUsedAtMs)
	}
	if got.Selected {
		t.Fatal("Touch 不应设置选中态")
	}
	if got.UpdatedAtMs != 1000 {
		t.Fatalf("Touch 不应改写 updated_at，实际 %d", got.UpdatedAtMs)
	}

	requireStoreError(t, store.Touch(ctx, "acc-touch-missing", 9999), ErrorCodeNotFound)
}

// TestStoreCredentialsNotPlaintextOnDisk 验证凭证以密文落库：
// 直接读取 app.db（以及同目录所有文件）的原始字节，确认 token 明文不出现。
func TestStoreCredentialsNotPlaintextOnDisk(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "account.key")
	dbPath := filepath.Join(dir, "app.db")

	cipherInstance, cryptoErr := OpenCipher(keyPath)
	if cryptoErr != nil {
		t.Fatalf("OpenCipher 失败: %v", cryptoErr)
	}
	store, storeErr := OpenSQLiteStore(dbPath, cipherInstance)
	if storeErr != nil {
		t.Fatalf("打开账号存储失败: %v", storeErr)
	}

	// 用足够长、足够独特的 marker，避免偶然字节序列误判。
	const (
		accessToken  = "ACCESS-TOKEN-PLAINTEXT-MARKER-4f7a1c9e"
		clientToken  = "CLIENT-TOKEN-PLAINTEXT-MARKER-9b2d8e1f"
		refreshToken = "REFRESH-TOKEN-PLAINTEXT-MARKER-7c3e5a2b"
		displayName  = "EncryptedDiskPlayer"
	)

	item := storeTestAccount("acc-enc-1", 5)
	item.DisplayName = displayName
	item.Profile.Name = displayName
	item.Credentials.AccessToken = accessToken
	item.Credentials.ClientToken = clientToken
	item.Credentials.RefreshToken = refreshToken
	requireStoreOK(t, store.Upsert(ctx, item), "写入账号")
	requireStoreOK(t, store.Close(), "关闭账号存储")

	// 把 WAL 内容合并回主库，确保断言读到的就是 app.db 的真实内容。
	rawDB, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("重新打开原始数据库失败: %v", err)
	}
	var busy, logFrames, checkpointed int
	if err := rawDB.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &logFrames, &checkpointed); err != nil {
		t.Fatalf("执行 wal_checkpoint 失败: %v", err)
	}
	requireStoreOK(t, rawDB.Close(), "关闭原始数据库")

	dbBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("读取 app.db 原始字节失败: %v", err)
	}

	// 前置断言：非敏感资料确实在库里，证明我们读的是正确的数据库文件。
	if !bytes.Contains(dbBytes, []byte(displayName)) {
		t.Fatal("app.db 中找不到账号展示名，落库断言可能读错了文件")
	}

	// 核心断言：三个 token 的明文都不能出现在主库文件中。
	for name, token := range map[string]string{
		"access_token":  accessToken,
		"client_token":  clientToken,
		"refresh_token": refreshToken,
	} {
		if bytes.Contains(dbBytes, []byte(token)) {
			t.Fatalf("app.db 中发现了 %s 的明文，违反「凭证不得明文落库」约束", name)
		}
	}

	// 更强断言：同目录下的任何文件（含密钥文件、残留 WAL/SHM）都不得含明文 token。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取数据目录失败: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		fileBytes, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("读取文件 %s 失败: %v", entry.Name(), err)
		}
		for name, token := range map[string]string{
			"access_token":  accessToken,
			"client_token":  clientToken,
			"refresh_token": refreshToken,
		} {
			if bytes.Contains(fileBytes, []byte(token)) {
				t.Fatalf("数据目录文件 %s 中发现了 %s 的明文", entry.Name(), name)
			}
		}
	}

	// 用同一把密钥文件重新打开，凭证必须可解密读回（证明密文格式自洽）。
	reopenedCipher, cryptoErr := OpenCipher(keyPath)
	if cryptoErr != nil {
		t.Fatalf("重新打开 Cipher 失败: %v", cryptoErr)
	}
	reopenedStore, storeErr := OpenSQLiteStore(dbPath, reopenedCipher)
	if storeErr != nil {
		t.Fatalf("重新打开账号存储失败: %v", storeErr)
	}
	t.Cleanup(func() { _ = reopenedStore.Close() })

	got, err := reopenedStore.Get(ctx, item.ID)
	requireStoreOK(t, err, "重新打开后读取账号")
	if got.Credentials.AccessToken != accessToken ||
		got.Credentials.ClientToken != clientToken ||
		got.Credentials.RefreshToken != refreshToken {
		t.Fatal("重新打开后凭证解密结果与写入值不一致")
	}
}

// TestStoreDecryptFailurePolicyOnList 验证单个账号凭证损坏时列表不整体失败。
//
// 选择的策略：列表返回摘要（凭证置空），Get 返回明确的 ACCOUNT_CRYPTO_001。
func TestStoreDecryptFailurePolicyOnList(t *testing.T) {
	ctx := context.Background()
	store, dbPath := openTestAccountStore(t)

	accountA := storeTestAccount("acc-corrupt-a", 1)
	accountB := storeTestAccount("acc-corrupt-b", 2)
	for _, item := range []StoredAccount{accountA, accountB} {
		requireStoreOK(t, store.Upsert(ctx, item), "写入账号")
	}

	// 直接篡改 A 的 access_token 密文列（模拟数据库被外部破坏）。
	rawDB, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("打开原始数据库失败: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx,
		`UPDATE accounts SET access_token_enc = ? WHERE id = ?`,
		[]byte{0xDE, 0xAD, 0xBE, 0xEF}, accountA.ID); err != nil {
		t.Fatalf("篡改密文失败: %v", err)
	}
	requireStoreOK(t, rawDB.Close(), "关闭原始数据库")

	// List 不整体失败：A 仍返回摘要，且三条凭证都被清空。
	list, err := store.List(ctx)
	requireStoreOK(t, err, "读取含损坏凭证的列表")
	if len(list) != 2 {
		t.Fatalf("列表期望 2 条，实际 %d", len(list))
	}
	corrupted := findStoredAccount(t, list, accountA.ID)
	if corrupted.DisplayName != accountA.DisplayName || corrupted.Profile != accountA.Profile {
		t.Fatalf("损坏账号的摘要应保留: %+v", corrupted.Account)
	}
	if corrupted.Credentials.AccessToken != "" ||
		corrupted.Credentials.ClientToken != "" ||
		corrupted.Credentials.RefreshToken != "" {
		t.Fatal("损坏账号的三条凭证应全部置空")
	}
	// 非敏感的凭证元数据仍应保留（帮助界面/日志判断过期状态）。
	if corrupted.Credentials.XUID != accountA.Credentials.XUID {
		t.Fatalf("损坏账号的 XUID 元数据应保留，实际 %q", corrupted.Credentials.XUID)
	}

	// 另一个账号不受影响。
	intact := findStoredAccount(t, list, accountB.ID)
	if intact.Credentials != accountB.Credentials {
		t.Fatal("未损坏账号的凭证应完整（凭证内容已隐去）")
	}

	// Get 是明确需要凭证的路径，必须返回明确错误。
	requireStoreError(t, func() error {
		_, getErr := store.Get(ctx, accountA.ID)
		return getErr
	}(), ErrorCodeCrypto)
}

// TestStoreKeyMismatchPolicy 验证密钥文件被替换后：List 可降级、Get 明确报错。
func TestStoreKeyMismatchPolicy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "account.key")
	dbPath := filepath.Join(dir, "app.db")

	originalCipher, cryptoErr := OpenCipher(keyPath)
	if cryptoErr != nil {
		t.Fatalf("OpenCipher 失败: %v", cryptoErr)
	}
	store, storeErr := OpenSQLiteStore(dbPath, originalCipher)
	if storeErr != nil {
		t.Fatalf("打开账号存储失败: %v", storeErr)
	}
	item := storeTestAccount("acc-key-1", 1)
	requireStoreOK(t, store.Upsert(ctx, item), "写入账号")
	requireStoreOK(t, store.Close(), "关闭原存储")

	// 用另一把密钥打开同一数据库（模拟密钥文件被替换/丢失后重建）。
	otherCipher := newTestCipher(t, 0xE0)
	mismatchedStore, storeErr := OpenSQLiteStore(dbPath, otherCipher)
	if storeErr != nil {
		t.Fatalf("用不同密钥打开账号存储失败: %v", storeErr)
	}
	t.Cleanup(func() { _ = mismatchedStore.Close() })

	list, err := mismatchedStore.List(ctx)
	requireStoreOK(t, err, "密钥不匹配时读取列表")
	if len(list) != 1 {
		t.Fatalf("列表期望 1 条，实际 %d", len(list))
	}
	if list[0].Credentials.AccessToken != "" ||
		list[0].Credentials.ClientToken != "" ||
		list[0].Credentials.RefreshToken != "" {
		t.Fatal("密钥不匹配时三条 token 应为空")
	}
	if list[0].Credentials.UserType != item.Credentials.UserType ||
		list[0].Credentials.XUID != item.Credentials.XUID ||
		list[0].Credentials.ExpiresAtMs != item.Credentials.ExpiresAtMs {
		t.Fatal("密钥不匹配时非敏感元数据应保留")
	}
	requireStoreError(t, func() error {
		_, getErr := mismatchedStore.Get(ctx, item.ID)
		return getErr
	}(), ErrorCodeCrypto)
}

// TestStoreMigrationRepeatable 验证同一数据库连续 OpenSQLiteStore 两次可正常运行。
func TestStoreMigrationRepeatable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")
	cipherInstance := newTestCipher(t, 0xA0)

	firstStore, storeErr := OpenSQLiteStore(dbPath, cipherInstance)
	if storeErr != nil {
		t.Fatalf("首次打开账号存储失败: %v", storeErr)
	}
	item := storeTestAccount("acc-migrate-1", 1)
	requireStoreOK(t, firstStore.Upsert(ctx, item), "首次写入")
	requireStoreOK(t, firstStore.Close(), "关闭首次存储")

	// 第二次打开：迁移全部已应用，不得重复执行或报 checksum 错误。
	secondStore, storeErr := OpenSQLiteStore(dbPath, cipherInstance)
	if storeErr != nil {
		t.Fatalf("第二次打开账号存储失败: %v", storeErr)
	}
	t.Cleanup(func() { _ = secondStore.Close() })

	got, err := secondStore.Get(ctx, item.ID)
	requireStoreOK(t, err, "第二次打开后读取账号")
	if got.Credentials.AccessToken != item.Credentials.AccessToken {
		t.Fatal("迁移重复执行后凭证不可解密")
	}

	// 第三次打开与第二次并存：迁移仍应是 no-op。
	thirdStore, storeErr := OpenSQLiteStore(dbPath, cipherInstance)
	if storeErr != nil {
		t.Fatalf("第三次打开账号存储失败: %v", storeErr)
	}
	t.Cleanup(func() { _ = thirdStore.Close() })

	list, err := thirdStore.List(ctx)
	requireStoreOK(t, err, "第三次打开后读取列表")
	if len(list) != 1 {
		t.Fatalf("迁移重复执行后列表期望 1 条，实际 %d", len(list))
	}
}

// TestStoreOpenErrors 验证 OpenSQLiteStore 的参数与路径错误分类。
func TestStoreOpenErrors(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "app.db")

	// 没有 Cipher 时不允许打开存储（绝不能退化成明文落库）。
	if _, storeErr := OpenSQLiteStore(validPath, nil); storeErr == nil {
		t.Fatal("cipher 为 nil 时期望报错")
	} else if storeErr.Code != ErrorCodeCrypto {
		t.Fatalf("cipher 为 nil 时错误码期望 %s，实际 %s", ErrorCodeCrypto, storeErr.Code)
	}

	// 空路径 → 存储错误。
	cipherInstance := newTestCipher(t, 0xB0)
	for _, path := range []string{"", "   "} {
		if _, storeErr := OpenSQLiteStore(path, cipherInstance); storeErr == nil {
			t.Fatalf("空路径 %q 期望报错", path)
		} else if storeErr.Code != ErrorCodeStorage {
			t.Fatalf("空路径 %q 错误码期望 %s，实际 %s", path, ErrorCodeStorage, storeErr.Code)
		}
	}

	// 目录不存在 → 打开失败，归类为存储错误且不 panic。
	missingDirPath := filepath.Join(dir, "no-such-dir", "app.db")
	if _, storeErr := OpenSQLiteStore(missingDirPath, cipherInstance); storeErr == nil {
		t.Fatal("父目录不存在时期望报错")
	} else if storeErr.Code != ErrorCodeStorage {
		t.Fatalf("父目录不存在时错误码期望 %s，实际 %s", ErrorCodeStorage, storeErr.Code)
	}
}

// TestStoreUpsertValidation 验证 Upsert 对非法输入的拒绝（返回 *Error，不 panic）。
func TestStoreUpsertValidation(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestAccountStore(t)

	// 缺少本地 id。
	noID := storeTestAccount("", 1)
	requireStoreError(t, store.Upsert(ctx, noID), ErrorCodeInput)

	// 未知账号类型。
	badKind := storeTestAccount("acc-bad-kind", 2)
	badKind.Kind = Kind("not-a-real-kind")
	requireStoreError(t, store.Upsert(ctx, badKind), ErrorCodeProviderUnknown)
}

// TestStoreConcurrentSetSelected 验证并发切换账号后全局选中数仍为 1。
func TestStoreConcurrentSetSelected(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestAccountStore(t)

	const accountCount = 6
	ids := make([]string, 0, accountCount)
	for i := 0; i < accountCount; i++ {
		item := storeTestAccount(fmt.Sprintf("acc-conc-%d", i), byte(i))
		requireStoreOK(t, store.Upsert(ctx, item), "写入并发账号")
		ids = append(ids, item.ID)
	}

	const goroutines = 60
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			id := ids[index%accountCount]
			if err := store.SetSelected(ctx, id, int64(index+1)); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("并发 SetSelected 失败: %v", err)
	}

	list, err := store.List(ctx)
	requireStoreOK(t, err, "并发后读取列表")
	selectedCount := 0
	for _, item := range list {
		if item.Selected {
			selectedCount++
		}
	}
	if selectedCount != 1 {
		t.Fatalf("并发切换后选中数期望 1，实际 %d", selectedCount)
	}
}

// TestStoreDefaultDatabasePath 验证路径解析结果的形式（环境不可用时接受明确报错）。
func TestStoreDefaultDatabasePath(t *testing.T) {
	tempDir := t.TempDir()
	if goruntime.GOOS == "windows" {
		t.Setenv("AppData", tempDir)
	} else {
		t.Setenv("HOME", tempDir)
		t.Setenv("XDG_CONFIG_HOME", tempDir)
	}

	path, pathErr := DefaultDatabasePath()
	if pathErr != nil {
		// 沙箱/CI 中用户配置目录与 exe 目录都可能不可写，此时允许返回存储错误。
		if pathErr.Code != ErrorCodeStorage {
			t.Fatalf("路径不可用时错误码期望 %s，实际 %s", ErrorCodeStorage, pathErr.Code)
		}
		t.Logf("当前环境无法解析账号数据库路径（可接受）: %v", pathErr)
		return
	}
	if path == "" {
		t.Fatal("DefaultDatabasePath 返回空路径但无错误")
	}
	if !strings.HasSuffix(path, "app.db") {
		t.Fatalf("数据库路径期望以 app.db 结尾，实际 %q", path)
	}
	if !strings.HasPrefix(path, tempDir) {
		t.Fatalf("数据库路径未落在临时目录: %s", path)
	}
}
