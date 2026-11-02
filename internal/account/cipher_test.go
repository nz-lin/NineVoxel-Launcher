package account

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixedTestKey 返回一把确定性的 32 字节测试密钥（仅测试使用，不用于生产）。
func fixedTestKey(seed byte) []byte {
	key := make([]byte, cipherKeyBytes)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

// newTestCipher 用固定密钥构造 Cipher，失败时直接终止测试。
func newTestCipher(t *testing.T, seed byte) *Cipher {
	t.Helper()
	c, cryptoErr := NewCipherWithKey(fixedTestKey(seed))
	if cryptoErr != nil {
		t.Fatalf("构造测试 Cipher 失败: %v", cryptoErr)
	}
	return c
}

// TestCipherRoundTrip 验证加解密往返（含空值、中文与较长令牌）。
func TestCipherRoundTrip(t *testing.T) {
	c := newTestCipher(t, 0x10)

	cases := []string{
		"",
		"token",
		"中文凭证-测试-🔐",
		strings.Repeat("a", 1024),
		"含\"引号\"与\\反斜杠以及换行\n的令牌",
	}

	for _, plaintext := range cases {
		ciphertext, cryptoErr := c.Encrypt(plaintext)
		if cryptoErr != nil {
			t.Fatalf("加密失败（明文已隐去）: %v", cryptoErr)
		}

		if plaintext == "" {
			// 空串是「没有该凭证」的语义：不产生密文，落库写 NULL。
			if ciphertext != nil {
				t.Fatalf("空串加密应返回 nil，实际 %v", ciphertext)
			}
			continue
		}
		if len(ciphertext) == 0 {
			t.Fatal("非空明文加密后密文为空")
		}

		decrypted, decryptErr := c.Decrypt(ciphertext)
		if decryptErr != nil {
			t.Fatalf("解密失败（明文已隐去）: %v", decryptErr)
		}
		if decrypted != plaintext {
			t.Fatal("加解密往返不一致（明文已隐去）")
		}
	}
}

// TestCipherEncryptUsesRandomNonce 验证相同明文两次加密得到不同密文（nonce 随机）。
func TestCipherEncryptUsesRandomNonce(t *testing.T) {
	c := newTestCipher(t, 0x20)
	plaintext := "same-token-must-differ"

	first, cryptoErr := c.Encrypt(plaintext)
	if cryptoErr != nil {
		t.Fatalf("首次加密失败: %v", cryptoErr)
	}
	second, cryptoErr := c.Encrypt(plaintext)
	if cryptoErr != nil {
		t.Fatalf("第二次加密失败: %v", cryptoErr)
	}

	if bytes.Equal(first, second) {
		t.Fatal("相同明文两次加密得到相同密文，nonce 可能没有随机化")
	}
}

// TestCipherDecryptEmpty 验证空切片/nil 解密为空串且不报错。
func TestCipherDecryptEmpty(t *testing.T) {
	c := newTestCipher(t, 0x30)

	for _, input := range [][]byte{nil, {}} {
		decrypted, cryptoErr := c.Decrypt(input)
		if cryptoErr != nil {
			t.Fatalf("空密文解密应成功，实际报错: %v", cryptoErr)
		}
		if decrypted != "" {
			t.Fatalf("空密文应解密为空串，实际 %q", decrypted)
		}
	}
}

// TestCipherRejectsTamperedCiphertext 验证被篡改的密文被拒绝，且错误文案不泄露明文。
func TestCipherRejectsTamperedCiphertext(t *testing.T) {
	c := newTestCipher(t, 0x40)
	const plaintext = "PLAINTEXT-TOKEN-MUST-NOT-LEAK-7f3a9c"

	ciphertext, cryptoErr := c.Encrypt(plaintext)
	if cryptoErr != nil {
		t.Fatalf("加密失败: %v", cryptoErr)
	}

	tampered := make([][]byte, 0, 4)

	// 篡改最后一个字节（落在认证标签内）。
	flipLast := bytes.Clone(ciphertext)
	flipLast[len(flipLast)-1] ^= 0xFF
	tampered = append(tampered, flipLast)

	// 篡改 nonce 的首字节。
	flipNonce := bytes.Clone(ciphertext)
	flipNonce[0] ^= 0xFF
	tampered = append(tampered, flipNonce)

	// 截断到 nonce 长度以内（不可能包含完整认证标签）。
	tampered = append(tampered, bytes.Clone(ciphertext[:4]))

	// 在末尾追加一个字节（改变认证标签边界）。
	appended := append(bytes.Clone(ciphertext), 0x00)
	tampered = append(tampered, appended)

	for i, input := range tampered {
		decrypted, decryptErr := c.Decrypt(input)
		if decryptErr == nil {
			t.Fatalf("第 %d 种篡改密文未被拒绝，解密结果 %q", i, decrypted)
		}
		if decryptErr.Code != ErrorCodeCrypto {
			t.Fatalf("第 %d 种篡改密文错误码期望 %s，实际 %s", i, ErrorCodeCrypto, decryptErr.Code)
		}
		if strings.Contains(decryptErr.Error(), plaintext) {
			t.Fatal("错误文案泄露了明文 token（内容已隐去）")
		}
	}
}

// TestCipherRejectsWrongKey 验证用错密钥无法解密（GCM 认证失败）。
func TestCipherRejectsWrongKey(t *testing.T) {
	encryptor := newTestCipher(t, 0x50)
	decryptor := newTestCipher(t, 0x60)

	ciphertext, cryptoErr := encryptor.Encrypt("cross-key-token")
	if cryptoErr != nil {
		t.Fatalf("加密失败: %v", cryptoErr)
	}

	decrypted, decryptErr := decryptor.Decrypt(ciphertext)
	if decryptErr == nil {
		t.Fatalf("错误密钥不应解密成功（解出内容已隐去，长度 %d）", len(decrypted))
	}
	if decryptErr.Code != ErrorCodeCrypto {
		t.Fatalf("错误码期望 %s，实际 %s", ErrorCodeCrypto, decryptErr.Code)
	}
}

// TestCipherRejectsInvalidKeyLength 验证非法密钥长度被拒绝且不 panic。
func TestCipherRejectsInvalidKeyLength(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		make([]byte, 16),
		make([]byte, 31),
		make([]byte, 33),
	}

	for _, key := range cases {
		cipherInstance, cryptoErr := NewCipherWithKey(key)
		if cryptoErr == nil {
			t.Fatalf("密钥长度 %d 期望报错，实际成功返回 %v", len(key), cipherInstance)
		}
		if cryptoErr.Code != ErrorCodeCrypto {
			t.Fatalf("密钥长度 %d 错误码期望 %s，实际 %s", len(key), ErrorCodeCrypto, cryptoErr.Code)
		}
	}
}

// TestCipherNilReceiverDoesNotPanic 验证 nil Cipher 调用返回错误而不是 panic。
func TestCipherNilReceiverDoesNotPanic(t *testing.T) {
	var nilCipher *Cipher

	if _, cryptoErr := nilCipher.Encrypt("token"); cryptoErr == nil {
		t.Fatal("nil Cipher 加密应返回错误")
	}
	if _, cryptoErr := nilCipher.Decrypt([]byte("ciphertext")); cryptoErr == nil {
		t.Fatal("nil Cipher 解密应返回错误")
	}
}

// TestOpenCipherPersistsKeyFile 验证密钥文件被创建且重复打开复用同一把密钥。
func TestCipherOpenPersistsKeyFile(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "secrets", "account.key")

	first, cryptoErr := OpenCipher(keyPath)
	if cryptoErr != nil {
		t.Fatalf("首次 OpenCipher 失败: %v", cryptoErr)
	}

	// 密钥文件权限：Windows 上 0600/0700 位不生效，改由平台助手直接读回 DACL
	// 校验受托者集合（仅当前用户 + SYSTEM）；非 Windows 保留原权限位断言。
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("密钥文件未创建: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(keyPath)); err != nil {
		t.Fatalf("密钥目录未创建: %v", err)
	}
	assertCipherKeyPermissions(t, keyPath)

	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("读取密钥文件失败: %v", err)
	}
	if len(keyBefore) != cipherKeyBytes {
		t.Fatalf("密钥长度期望 %d，实际 %d", cipherKeyBytes, len(keyBefore))
	}

	ciphertext, cryptoErr := first.Encrypt("persisted-token")
	if cryptoErr != nil {
		t.Fatalf("加密失败: %v", cryptoErr)
	}

	// 第二次打开必须复用同一把密钥，才能解开第一次写的密文。
	second, cryptoErr := OpenCipher(keyPath)
	if cryptoErr != nil {
		t.Fatalf("第二次 OpenCipher 失败: %v", cryptoErr)
	}
	decrypted, cryptoErr := second.Decrypt(ciphertext)
	if cryptoErr != nil {
		t.Fatalf("用重新打开的 Cipher 解密失败: %v", cryptoErr)
	}
	if decrypted != "persisted-token" {
		t.Fatalf("解密结果期望 %q，实际 %q", "persisted-token", decrypted)
	}

	keyAfter, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("再次读取密钥文件失败: %v", err)
	}
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("第二次 OpenCipher 改写了密钥文件，历史凭证将无法解密")
	}
}

// TestOpenCipherRejectsBrokenKeyFile 验证已有密钥文件长度非法时报错而不是覆盖。
func TestCipherOpenRejectsBrokenKeyFile(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "account.key")
	if err := os.WriteFile(keyPath, make([]byte, cipherKeyBytes-1), cipherKeyFileMode); err != nil {
		t.Fatalf("写入坏密钥文件失败: %v", err)
	}

	if _, cryptoErr := OpenCipher(keyPath); cryptoErr == nil {
		t.Fatal("密钥文件长度非法时期望报错")
	} else if cryptoErr.Code != ErrorCodeCrypto {
		t.Fatalf("错误码期望 %s，实际 %s", ErrorCodeCrypto, cryptoErr.Code)
	}

	// 关键断言：坏密钥文件不能被覆盖（覆盖会让历史密文永久不可解）。
	content, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("回读密钥文件失败: %v", err)
	}
	if len(content) != cipherKeyBytes-1 {
		t.Fatalf("坏密钥文件被改写，长度期望 %d，实际 %d", cipherKeyBytes-1, len(content))
	}
}

// TestOpenCipherRejectsUnusablePath 验证路径不可用（父路径是普通文件）时返回错误。
func TestCipherOpenRejectsUnusablePath(t *testing.T) {
	blockingFile := filepath.Join(t.TempDir(), "blocking-file")
	if err := os.WriteFile(blockingFile, []byte("x"), cipherKeyFileMode); err != nil {
		t.Fatalf("写入占位文件失败: %v", err)
	}

	keyPath := filepath.Join(blockingFile, "account.key")
	_, cryptoErr := OpenCipher(keyPath)
	if cryptoErr == nil {
		t.Fatal("父路径是普通文件时期望报错")
	}
	if cryptoErr.Code != ErrorCodeCrypto {
		t.Fatalf("错误码期望 %s，实际 %s", ErrorCodeCrypto, cryptoErr.Code)
	}
}

// TestOpenCipherRejectsEmptyPath 验证空路径被拒绝。
func TestCipherOpenRejectsEmptyPath(t *testing.T) {
	for _, path := range []string{"", "   "} {
		if _, cryptoErr := OpenCipher(path); cryptoErr == nil {
			t.Fatalf("空路径 %q 期望报错", path)
		} else if cryptoErr.Code != ErrorCodeCrypto {
			t.Fatalf("空路径 %q 错误码期望 %s，实际 %s", path, ErrorCodeCrypto, cryptoErr.Code)
		}
	}
}

// TestCipherKeyGeneratedWithCryptoRand 验证 OpenCipher 生成的密钥不是全零的伪随机值。
//
// 随机源不可取代性由 crypto/rand 保证；这里只做一个低成本的存在性检查，
// 防止将来误改成 make([]byte, 32) 这类退化实现。
func TestCipherKeyGeneratedWithCryptoRand(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "account.key")
	if _, cryptoErr := OpenCipher(keyPath); cryptoErr != nil {
		t.Fatalf("OpenCipher 失败: %v", cryptoErr)
	}

	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("读取密钥文件失败: %v", err)
	}

	// 32 字节全零的概率约为 2^-256，出现即说明没有使用随机源。
	allZero := true
	for _, b := range key {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("生成的密钥全为零，随机源不可用")
	}

	// 确保密钥文件确实落盘（后续启动才能复用）。
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("密钥文件应存在: %v", err)
	}
}
