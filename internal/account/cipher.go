// 本文件提供账号凭证的对称加密（AES-256-GCM）。
//
// 设计要点：
//   - 密钥是 32 字节随机数，保存在独立密钥文件中（目录 0700、文件 0600）；
//   - 权限收紧由 secureCipherKeyPath 负责：POSIX 上 0600/0700 权限位即生效，
//     Windows 忽略这些位，必须显式设置只含当前用户与 SYSTEM 的受保护 DACL；
//     加载已存在的密钥时也会幂等收敛 ACL，而不是只在首次创建时设置；
//   - 密钥文件不存在时用 O_CREATE|O_EXCL|O_WRONLY 创建：并发启动时只有一个
//     进程能创建成功，另一个读到同一把密钥，不会出现「两个进程各写一半」；
//   - 密文布局为 nonce || ciphertext+tag，nonce 每次加密随机生成并随密文保存；
//   - 解密失败（密文被截断/篡改、密钥不匹配、密钥长度非法）统一返回
//     ErrorCodeCrypto 的 *Error，**绝不 panic**；
//   - 本文件不写任何日志：明文 token 与密钥都不允许出现在日志或错误文案里。
package account

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// cipherKeyBytes 是 AES-256 的密钥长度（字节）。
	cipherKeyBytes = 32

	// cipherKeyFileMode 是密钥文件权限：仅当前用户可读写。
	cipherKeyFileMode os.FileMode = 0o600

	// cipherKeyDirMode 是密钥目录权限：仅当前用户可访问。
	cipherKeyDirMode os.FileMode = 0o700

	// cipherAssociatedData 是 AEAD 的固定关联数据。
	//
	// 它把密文绑定到「账号凭证」这一用途：即使将来同一把密钥被复用到别处，
	// 跨用途搬运的密文也会解密失败，而不是被静默接受。
	cipherAssociatedData = "ninevoxel.account.credentials.v1"
)

// Cipher 负责凭证的加密与解密。
//
// 并发约束：底层 crypto/cipher.AEAD 的 Seal/Open 不修改自身状态，可以安全地
// 被多个 goroutine 同时调用（Wails 可能从不同 goroutine 读写账号）。
type Cipher struct {
	// aead 为 AES-256-GCM 的 AEAD 实现；nil 表示 Cipher 未初始化。
	aead cipher.AEAD
}

// OpenCipher 打开（必要时创建）密钥文件并返回 Cipher。
//
// 参数 keyPath 为密钥文件路径；父目录不存在时按 0700 创建。
// 文件不存在时生成 32 字节随机密钥并以 0600 写入；文件已存在时读取并校验长度，
// 因此同一路径的多次调用拿到的是同一把密钥（重启后仍能解密历史凭证）。
//
// 任何失败（目录不可写、随机源不可用、已有密钥长度非法）都返回
// ErrorCodeCrypto 的 *Error，不 panic、不输出密钥。
func OpenCipher(keyPath string) (*Cipher, *Error) {
	path := strings.TrimSpace(keyPath)
	if path == "" {
		return nil, newSystemError(ErrorCodeCrypto, "凭证密钥路径为空，无法初始化加密")
	}

	key, cryptoErr := loadOrCreateCipherKey(path)
	if cryptoErr != nil {
		return nil, cryptoErr
	}
	return NewCipherWithKey(key)
}

// NewCipherWithKey 用给定密钥构造 Cipher（测试与密钥轮换使用）。
//
// 密钥必须恰好 32 字节；长度不对时返回 ErrorCodeCrypto 错误，
// 而不是把错误长度的密钥交给 AES（那会 panic 或得到非预期强度）。
// 传入的切片不会被保留引用，调用方可以在返回后复用/清零自己的缓冲区。
func NewCipherWithKey(key []byte) (*Cipher, *Error) {
	if len(key) != cipherKeyBytes {
		return nil, newSystemError(ErrorCodeCrypto, "凭证密钥长度不合法，需要 32 字节")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		// 长度已校验，这里只可能是底层实现异常；仍按错误返回，不 panic。
		return nil, newSystemError(ErrorCodeCrypto, "初始化凭证加密失败")
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, newSystemError(ErrorCodeCrypto, "初始化凭证加密失败")
	}

	return &Cipher{aead: aead}, nil
}

// Encrypt 加密明文凭证；空字符串返回 nil。
//
// 返回 nil 表示「没有该凭证」，落库时写 NULL，而不是写一个空串的密文——
// 这样数据库里就不会出现无意义的密文行。返回字节为 nonce || ciphertext+tag。
func (c *Cipher) Encrypt(plaintext string) ([]byte, *Error) {
	if c == nil || c.aead == nil {
		return nil, newSystemError(ErrorCodeCrypto, "凭证加密尚未初始化")
	}
	if plaintext == "" {
		return nil, nil
	}

	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		// 随机源不可用时宁可失败也不能退化成可预测 nonce。
		return nil, newSystemError(ErrorCodeCrypto, "无法生成凭证加密随机数")
	}

	// Seal 把密文追加在 nonce 之后，得到自包含的 nonce || ciphertext+tag。
	return c.aead.Seal(nonce, nonce, []byte(plaintext), []byte(cipherAssociatedData)), nil
}

// Decrypt 解密密文凭证；空切片返回空字符串。
//
// 密文被截断/篡改、或用错密钥解密时返回 ErrorCodeCrypto 错误。
// 错误文案只说明「不可用」，不包含明文、密钥或密文内容。
func (c *Cipher) Decrypt(ciphertext []byte) (string, *Error) {
	if c == nil || c.aead == nil {
		return "", newSystemError(ErrorCodeCrypto, "凭证加密尚未初始化")
	}
	if len(ciphertext) == 0 {
		return "", nil
	}

	nonceSize := c.aead.NonceSize()
	tagSize := c.aead.Overhead()
	if len(ciphertext) < nonceSize+tagSize {
		// 短于 nonce + tag 的密文不可能是合法密文，直接拒绝（避免切片越界 panic）。
		return "", newSystemError(ErrorCodeCrypto, "凭证密文已损坏，请重新登录")
	}

	nonce, body := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := c.aead.Open(nil, nonce, body, []byte(cipherAssociatedData))
	if err != nil {
		// GCM 的认证失败不区分「篡改」与「密钥不对」，对用户给出同一处置建议。
		return "", newSystemError(ErrorCodeCrypto, "凭证已失效或与本机密钥不匹配，请重新登录")
	}

	return string(plaintext), nil
}

// loadOrCreateCipherKey 读取既有密钥，不存在时生成并原子创建。
//
// 返回值是密钥原始字节；调用方（NewCipherWithKey）会校验长度。
// 本函数不记录任何内容，保证密钥不进入日志。
func loadOrCreateCipherKey(path string) ([]byte, *Error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, cipherKeyDirMode); err != nil {
			return nil, newSystemError(ErrorCodeCrypto, "无法创建凭证密钥目录，请检查数据目录权限")
		}
		// 目录已存在（含本次刚创建）时都收敛一次 DACL：Windows 上 0700 位不生效，
		// 且父目录的 ACE 可能已被外部放宽，必须显式收紧而不是只依赖创建时机。
		if err := secureCipherKeyPath(dir, true); err != nil {
			return nil, newSystemError(ErrorCodeCrypto, "无法收紧凭证密钥目录权限，请检查数据目录权限")
		}
	}

	key, err := os.ReadFile(path)
	switch {
	case err == nil:
		// 已存在的密钥文件同样要收敛 ACL：历史版本或外部工具可能留下宽泛 ACE，
		// 只在首次创建时收紧会让旧密钥一直对同机其他用户可读。
		if secureErr := secureCipherKeyPath(path, false); secureErr != nil {
			return nil, newSystemError(ErrorCodeCrypto, "无法收紧凭证密钥文件权限，请检查数据目录权限")
		}
		if len(key) != cipherKeyBytes {
			// 已有密钥长度非法时不能覆盖：覆盖会让历史密文永久不可解，
			// 正确处置是报错让用户确认后手工处理密钥文件。
			return nil, newSystemError(ErrorCodeCrypto, "凭证密钥文件内容不合法，无法解密已有凭证")
		}
		return key, nil

	case errors.Is(err, fs.ErrNotExist):
		return createCipherKeyFile(path)

	default:
		return nil, newSystemError(ErrorCodeCrypto, "无法读取凭证密钥文件，请检查数据目录权限")
	}
}

// createCipherKeyFile 生成 32 字节随机密钥并以 0600 原子创建密钥文件。
//
// 使用 O_CREATE|O_EXCL 而不是「先判断存在再写」：多进程同时首次启动时，
// 只有一个进程创建成功，其余进程走 ErrExist 分支读取同一份密钥。
//
// 创建成功后立即调用 secureCipherKeyPath 收紧 DACL：Windows 上新文件会继承
// 父目录 ACE，仅靠创建模式无法保证只对当前用户可读。收紧失败时保留文件不删除——
// 并发的另一个进程可能已经读到这把密钥并加密了凭证，删除会造成永久数据丢失。
func createCipherKeyFile(path string) ([]byte, *Error) {
	key := make([]byte, cipherKeyBytes)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, newSystemError(ErrorCodeCrypto, "无法生成凭证加密密钥")
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, cipherKeyFileMode)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			// 并发创建竞态：另一个进程先创建成功，读取它的密钥。
			existing, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, newSystemError(ErrorCodeCrypto, "无法读取凭证密钥文件，请检查数据目录权限")
			}
			if secureErr := secureCipherKeyPath(path, false); secureErr != nil {
				return nil, newSystemError(ErrorCodeCrypto, "无法收紧凭证密钥文件权限，请检查数据目录权限")
			}
			if len(existing) != cipherKeyBytes {
				return nil, newSystemError(ErrorCodeCrypto, "凭证密钥文件内容不合法，无法解密已有凭证")
			}
			return existing, nil
		}
		return nil, newSystemError(ErrorCodeCrypto, "无法创建凭证密钥文件，请检查数据目录权限")
	}

	// 写入短密钥或关闭失败都视为失败：删除残留文件，避免下次启动读到长度不对的
	// 密钥文件后陷入「永久无法加密」的状态。
	written, writeErr := file.Write(key)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || written != len(key) {
		_ = os.Remove(path)
		return nil, newSystemError(ErrorCodeCrypto, "无法写入凭证密钥文件，请检查磁盘空间")
	}

	if secureErr := secureCipherKeyPath(path, false); secureErr != nil {
		return nil, newSystemError(ErrorCodeCrypto, "无法收紧凭证密钥文件权限，请检查数据目录权限")
	}

	return key, nil
}
