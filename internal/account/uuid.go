// 本文件提供账号子系统通用的 UUID / 玩家标识工具。
//
// 所有 UUID 在包内统一使用**无连字符小写**形式（Minecraft 协议要求），
// 只有面向用户展示时才格式化出连字符。

package account

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// NewUUIDv4 生成一个带连字符的 UUID v4。
//
// 密码学随机源不可用时返回错误——账号 id 不允许退化成可预测值。
func NewUUIDv4() (string, *Error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", newSystemError(ErrorCodeCrypto, "无法生成随机标识，请重试")
	}
	// RFC 4122：设置版本号与变体位。
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return formatUUID(buf[:]), nil
}

// formatUUID 把 16 字节格式化为 8-4-4-4-12 的连字符形式。
func formatUUID(raw []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

// NormalizeMinecraftID 把任意 UUID 文本规范化为无连字符小写形式。
//
// 接受带连字符与不带连字符两种输入；长度不是 32 位十六进制时返回输入错误。
func NormalizeMinecraftID(raw string) (string, *Error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	stripped := strings.ReplaceAll(trimmed, "-", "")
	if len(stripped) != 32 {
		return "", newInputError(ErrorCodeInput, "账号 UUID 格式不正确")
	}
	if _, err := hex.DecodeString(stripped); err != nil {
		return "", newInputError(ErrorCodeInput, "账号 UUID 格式不正确")
	}
	return stripped, nil
}

// FormatHyphenated 把无连字符 UUID 格式化为 8-4-4-4-12 形式，供界面展示。
//
// 输入不是 32 位十六进制时原样返回，避免展示层因脏数据崩溃。
func FormatHyphenated(id string) string {
	if len(id) != 32 {
		return id
	}
	if _, err := hex.DecodeString(id); err != nil {
		return id
	}
	return id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:32]
}

// OfflinePlayerUUID 按离线模式规则由玩家名派生 UUID。
//
// 规则来自 authlib-injector 与 Mojang 的既有行为：
// UUID.nameUUIDFromBytes(("OfflinePlayer:" + name).getBytes(UTF_8))，即
// MD5 后设置版本位为 3、变体位为 IETF。返回无连字符小写形式。
func OfflinePlayerUUID(name string) string {
	sum := md5.Sum([]byte("OfflinePlayer:" + name))
	sum[6] = (sum[6] & 0x0f) | 0x30
	sum[8] = (sum[8] & 0x3f) | 0x80
	return hex.EncodeToString(sum[:])
}

// validOfflineUsername 校验离线玩家名：3-16 位，仅字母数字下划线。
//
// 这是 Minecraft 用户名的通行规则；离线模式下必须由启动器自己把关，
// 否则会生成游戏无法接受的名字。
func validOfflineUsername(name string) bool {
	if len(name) < 3 || len(name) > 16 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}
