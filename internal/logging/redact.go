// 本文件实现日志脱敏规则。
//
// 脱敏是强制要求而非可选优化：《错误码与日志规范》与 AGENTS.md 5.7 禁止把
// Token / Cookie / 密码 / 邮箱 / 未脱敏绝对路径写入任何持久化位置。
// 因此所有落盘与外发的日志内容必须经过本文件的函数。
//
// 与前端 services/errors.ts 的脱敏思路保持一致（同一类风险、同样的替换语义），
// 但 Go 侧独立实现：前端只处理界面文案，后端要处理真正落盘的结构化字段。
package logging

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// redactedPlaceholder 是所有敏感值被替换后的统一占位符。
const redactedPlaceholder = "***"

// pathPlaceholder 是绝对路径被替换后的占位符。
//
// 使用中文占位符而不是 ASCII：它出现在面向用户的 message 中，
// 与前端 errors.ts 的「（已隐藏的路径）」保持一致，便于用户理解。
const pathPlaceholder = "（已隐藏的路径）"

// maxMessageRunes 是单条 message 的最大字符数。
//
// 取值 500：足够记录一条完整的人可读说明，又能防止第三方原始响应整段写入日志。
const maxMessageRunes = 500

// sensitiveKeyPattern 匹配需要整体替换值的键名。
//
// 覆盖规范要求的 token / secret / password / passwd / cookie / authorization /
// credential / refresh_token / access_token，并补充 id_token 与 session 两个常见变体。
var sensitiveKeyPattern = regexp.MustCompile(
	`token|secret|password|passwd|cookie|authorization|credential|session`,
	/* 说明：Go 的 regexp 不支持内联 (?i) 之外的写法，这里在匹配前统一转小写，
	   因此不需要在模式串里重复大小写变体。 */
)

// keyValueSecretPattern 匹配文本中形如 key=value / key: value 的凭证片段。
//
// 捕获组 1 为键与分隔符（原样保留），组 2 为需要替换的值。
// 该模式用于 message 这类自由文本，避免把凭证粘在句子里逃过键名检查。
//
// 组 2 允许带可选的 `Bearer ` 前缀：本规则在 Bearer 规则之后执行，
// 此时文本已是 `authorization: Bearer ***`，把整段 `Bearer ***` 一起收掉
// 才能得到唯一的 `authorization: ***`；否则会残留成 `authorization: *** ***`。
var keyValueSecretPattern = regexp.MustCompile(
	`((?:access_token|refresh_token|id_token|token|password|passwd|secret|authorization|credential|cookie)\s*[=:]\s*)((?:Bearer\s+)?[^\s&,;"']+)`,
)

// bearerPattern 匹配 HTTP Authorization 头里的 Bearer 凭证。
var bearerPattern = regexp.MustCompile(`Bearer\s+[A-Za-z0-9._~+/=-]+`)

// jwtPattern 匹配 JWT 形态的 Token。
//
// JWT 的第一段固定是 eyJ 开头的 Base64URL 编码 JSON，因此用 eyJ 作为锚点即可识别，
// 无需校验签名段。长度下限 10 是为了避免误伤普通文本里的短标识。
var jwtPattern = regexp.MustCompile(`eyJ[A-Za-z0-9._-]{10,}`)

// windowsPathPattern 匹配 Windows 盘符绝对路径与 UNC 共享路径。
var windowsPathPattern = regexp.MustCompile(`[A-Za-z]:[\\/][^\s"'<>|]*|\\\\[^\s"'<>|]*`)

// posixPathPattern 匹配常见的 POSIX 绝对目录。
//
// 不匹配所有以 / 开头的字符串：那会把正常的 URL 路径与比例表达误伤。
var posixPathPattern = regexp.MustCompile(`/(?:home|Users|root|tmp|var|usr|opt|mnt|private|etc|srv|data)/[^\s"']*`)

// fileURLPattern 匹配 file:// 协议地址。
var fileURLPattern = regexp.MustCompile(`file://[^\s"']*`)

// urlPattern 匹配带 scheme 的 URL，用于移除查询串。
//
// 组 1 为 scheme，组 2 为 authority（可能不存在），组 3 为 path 及其后的查询串与片段。
// 第三组特意**包含** `?` 与 `#`：若像 `[^?#\s]*` 那样在 `?` 之前就停止匹配，
// 查询串会落在匹配范围之外，替换后原样保留，脱敏就形同虚设。
var urlPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*)://([^/?#\s]*)([^#\s]*)`)

// emailPattern 匹配邮箱地址。
//
// 组 1 为 @ 前的首字符（脱敏后保留，便于区分是哪个账号），组 2 为域名。
var emailPattern = regexp.MustCompile(`([A-Za-z0-9._%+-]+)@([A-Za-z0-9.-]+)`)

// emailLocalPartKeepRunes 是邮箱本地部分脱敏后允许保留的前缀字符数。
const emailLocalPartKeepRunes = 1

// Redact 对一段自由文本做脱敏。
//
// 处理顺序按风险与误伤两个因素确定，顺序本身有正确性要求：
//  1. 先剥离 URL 查询串：查询串里的凭证要在任何路径规则生效前移除；
//  2. 再隐藏绝对路径：路径最容易夹带用户名，且必须在凭证替换前处理，
//     否则被凭证规则替换后的占位符会与路径规则的匹配范围互相干扰；
//  3. 先替换 Bearer 头，再替换 key=value 形式的凭证：
//     `authorization: Bearer xyz` 必须整体匹配，若顺序相反会残留 Token 片段；
//  4. 最后处理邮箱、主目录前缀与截断。
//
// 参数 raw 为原始文本；返回值为可安全落盘的文本（可能被截断）。
func Redact(raw string) string {
	if raw == "" {
		return ""
	}

	text := raw

	// 第一步：移除 URL 中的查询串与片段，避免把 ?token=... 之类带到日志。
	text = stripURLQuery(text)

	// 第二步：隐藏绝对路径（Windows 盘符、UNC、POSIX 常见目录、file:// 地址）。
	text = replaceWindowsPaths(text)
	text = fileURLPattern.ReplaceAllString(text, pathPlaceholder)
	text = replacePosixPaths(text)

	// 第三步：先替换 Bearer 头，再替换凭证类键值对，最后替换裸 JWT。
	text = bearerPattern.ReplaceAllString(text, "Bearer "+redactedPlaceholder)
	text = keyValueSecretPattern.ReplaceAllString(text, "$1"+redactedPlaceholder)
	text = jwtPattern.ReplaceAllString(text, redactedPlaceholder)

	// 第四步：邮箱按 a***@example.com 形式脱敏，保留域名便于判断来源服务。
	text = emailPattern.ReplaceAllStringFunc(text, redactEmail)

	// 第五步：把用户主目录前缀替换为 ~，避免同类路径以相对形式泄漏用户名。
	text = replaceHomePrefix(text)

	// 第六步：折叠空白并截断，避免超长内容写满日志文件。
	text = collapseSpaces(text)
	return truncateRunes(text, maxMessageRunes)
}

// replaceWindowsPaths 隐藏 Windows 盘符路径与 UNC 共享路径。
//
// 为什么不能用简单的 ReplaceAllString：`[A-Za-z]:[\\/]` 会把 URL 的协议部分
// 误判为盘符——例如 `https://api.example.com/v1` 中的 `s://` 会被整体替换成
// 路径占位符，把正常 URL 破坏成 `http（已隐藏的路径）`。
// 因此这里逐处检查匹配的前一个字符：紧跟字母或数字时视为协议片段，跳过不替换。
func replaceWindowsPaths(text string) string {
	matches := windowsPathPattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}

	var builder strings.Builder
	last := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		// 匹配紧跟在字母或数字之后：属于 URL 协议（如 https 的 s:），跳过。
		if start > 0 && isAlphaNumeric(text[start-1]) {
			continue
		}
		builder.WriteString(text[last:start])
		builder.WriteString(pathPlaceholder)
		last = end
	}
	builder.WriteString(text[last:])
	return builder.String()
}

// replacePosixPaths 隐藏常见 POSIX 绝对目录。
//
// 与 Windows 路径同理：URL 里的 `/data/xxx` 这类片段不应被当成磁盘路径，
// 因此同样要求匹配前一个字符不是字母或数字（URL 中路径前缀必然是字母之后）。
func replacePosixPaths(text string) string {
	matches := posixPathPattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}

	var builder strings.Builder
	last := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		if start > 0 && isAlphaNumeric(text[start-1]) {
			continue
		}
		builder.WriteString(text[last:start])
		builder.WriteString(pathPlaceholder)
		last = end
	}
	builder.WriteString(text[last:])
	return builder.String()
}

// isAlphaNumeric 判断字符是否为 ASCII 字母或数字。
//
// 只处理 ASCII：路径与协议前缀都是 ASCII 字符，中文等非 ASCII 字符
// 一律视为「分隔符」，不会阻止路径替换。
func isAlphaNumeric(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	default:
		return false
	}
}

// RedactAttributes 对扁平属性表逐项脱敏。
//
// 规则：键名命中敏感词时值整体替换为 ***（不保留任何片段）；
// 其余项按 Redact 处理值，键名本身也做一次 Redact，防止键名里夹带凭证。
// 参数 attrs 为原始属性；返回新的映射，不修改入参；入参为 nil 时返回 nil。
func RedactAttributes(attrs map[string]string) map[string]string {
	if attrs == nil {
		return nil
	}

	result := make(map[string]string, len(attrs))
	for key, value := range attrs {
		safeKey := Redact(key)
		if IsSensitiveKey(key) {
			result[safeKey] = redactedPlaceholder
			continue
		}
		result[safeKey] = Redact(value)
	}
	return result
}

// IsSensitiveKey 判断键名是否属于需要整体替换的敏感键。
//
// 判断方式：转小写后按子串匹配，因此 accessToken、X-Api-Token 等变体都会命中。
// 参数 key 为待判断的键名；返回 true 表示该键的值必须整体替换。
func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return false
	}
	return sensitiveKeyPattern.MatchString(lower)
}

// stripURLQuery 移除文本中所有 URL 的查询串与片段。
//
// 保留 scheme + authority + path：这些部分足以定位是哪个服务、哪个接口，
// 而查询串常带 access_token、code、state 等一次性凭证。
// 实现要点：匹配范围包含查询串，替换时只回填 scheme://authority + 去掉查询串的 path。
func stripURLQuery(text string) string {
	return urlPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := urlPattern.FindStringSubmatch(match)
		if len(parts) < 4 {
			return match
		}
		scheme, authority, rest := parts[1], parts[2], parts[3]
		// rest 可能含 "?query" 或 "#fragment"，在第一个这类分隔符处截断。
		if idx := strings.IndexAny(rest, "?#"); idx >= 0 {
			rest = rest[:idx]
		}
		if authority == "" {
			return scheme + "://" + rest
		}
		return scheme + "://" + authority + rest
	})
}

// redactEmail 把邮箱本地部分按「保留首字符 + ***」脱敏。
//
// 保留首字符是有意取舍：既不暴露完整账号，又能在同一日志中区分不同来源账号。
func redactEmail(match string) string {
	parts := emailPattern.FindStringSubmatch(match)
	if len(parts) < 3 {
		return redactedPlaceholder
	}
	local, domain := parts[1], parts[2]
	if len([]rune(local)) <= emailLocalPartKeepRunes {
		return redactedPlaceholder + "@" + domain
	}
	prefix := string([]rune(local)[:emailLocalPartKeepRunes])
	return prefix + redactedPlaceholder + "@" + domain
}

// replaceHomePrefix 把用户主目录前缀替换为 ~。
//
// 该步骤单独取出是因为 os.UserHomeDir() 是运行时值，无法写进正则字面量。
// 只有当主目录路径确实出现在文本中时才做替换，且替换为最常见的 ~ 语义，
// 既不丢失「这是在用户目录下」的信息，也不泄漏用户名。
func replaceHomePrefix(text string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return text
	}

	// 同时匹配原始形式与正斜杠形式，覆盖不同平台与不同来源拼接出的路径。
	candidates := []string{home}
	if slashForm := filepath.ToSlash(home); slashForm != home {
		candidates = append(candidates, slashForm)
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		text = strings.ReplaceAll(text, candidate, "~")
	}
	return text
}

// collapseSpaces 把连续空白折叠为单个空格并去除首尾空白。
//
// 目的：防止换行把一条 JSON Lines 记录劈成两行，破坏日志文件的可解析性。
func collapseSpaces(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// truncateRunes 按字符数截断文本，超出部分以省略号标记。
//
// 使用 rune 而非 byte：中文说明按字节截断会产生半个字符的乱码。
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// RedactURL 返回移除查询串后的 URL，供需要记录地址但不得泄漏凭证的场景使用。
//
// 该函数供其他模块直接调用（例如下载器记录请求地址）：
// 只保留 scheme + host + path，参数部分整体丢弃而不是替换，
// 因为查询串的语义无法逐项判断是否敏感，整体丢弃是最安全的策略。
func RedactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		// 解析失败时退回文本脱敏，绝不在无法判断的情况下原样输出。
		return Redact(raw)
	}

	// 清空查询串与片段，再拼回原来的 scheme/host/path。
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return Redact(parsed.String())
}
