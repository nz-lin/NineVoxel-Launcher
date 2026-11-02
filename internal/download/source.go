// 本文件实现下载来源注册表与镜像重写规则（P3-05 第 14 条）。
//
// 边界：
//   - 官方白名单只有 5 个 host，任何 http、任何其它 host 都不允许下载；
//   - 镜像只是**默认候选**，不是信任根：它同样受 host 白名单约束，
//     且只有「镜像开关打开」时才会进入候选列表；
//   - 镜像 host 只出现在本文件，不得散落到 transport.go / resume.go，
//     这样替换镜像列表只需要改一处。
package download

import (
	"net/url"
	"strings"
	"sync"
)

const (
	// officialScheme 是唯一允许的下载协议。
	officialScheme = "https"

	// mirrorMountPrefix 是镜像的挂载前缀规则：镜像把官方 host 当作第一段路径，
	// 例如 https://libraries.minecraft.net/com/x/y.jar 重写为
	// https://bmclapi2.bangbang93.com/libraries.minecraft.net/com/x/y.jar。
	mirrorMountPrefix = "/"

	// defaultMirrorHost 是内置参考镜像 host（D-38）。
	//
	// 它只是默认候选：镜像开关默认关闭，且用户可在设置里改用其它镜像或不用镜像。
	defaultMirrorHost = "bmclapi2.bangbang93.com"

	// MirrorEnabledSettingKey 是镜像开关的设置键（D-37）。
	//
	// 本卡只定义键名与语义（布尔、默认 false），不读写设置存储：
	// 读取与持久化属于设置服务，接线归 P3-07。
	MirrorEnabledSettingKey = "download.mirrorEnabled"

	// logPathSegmentCount 是日志里保留的路径段数。
	//
	// 只保留前两段：足够区分「版本清单/资源索引/资源对象」等类别，
	// 又不会把完整文件路径（含版本号、哈希、文件名）写进日志。
	logPathSegmentCount = 2
)

// rewritableOfficialHosts 是可被镜像重写的官方 host 白名单。
//
// 与 internal/version 的 manifestAllowedHosts 保持一致（该包禁止修改，
// 因此这里按语义复制一份；两边一旦分叉，下载会退化为「只走官方」而不会静默放行）。
var rewritableOfficialHosts = map[string]bool{
	"piston-meta.mojang.com":           true,
	"launcher.mojang.com":              true,
	"launchermeta.mojang.com":          true,
	"resources.download.minecraft.net": true,
	"libraries.minecraft.net":          true,
}

// MirrorEndpoint 描述一个镜像源。
type MirrorEndpoint struct {
	// Host 为镜像的 host（可带端口），例如 bmclapi2.bangbang93.com。
	Host string
}

// ===== 镜像开关的 Provider 注入（D-57）=====

// mirrorProviderMu 保护 mirrorEnabledProvider 的读写。
var (
	mirrorProviderMu      sync.RWMutex
	mirrorEnabledProvider func() bool
)

// SetMirrorEnabledProvider 注入镜像开关读取函数。
//
// 为什么用 Provider 而不是直接 import settings：本包是下载能力层，
// 直接依赖设置服务会把「业务配置」的依赖方向倒置；由装配层
// （internal/app/bootstrap.go）把设置快照包成函数注入，与 internal/version 的
// SetDataRootProvider 是同一模式。设置值的权威来源始终是设置服务。
//
// 参数 provider 为读取函数；传 nil 表示恢复默认（镜像关闭）。
func SetMirrorEnabledProvider(provider func() bool) {
	mirrorProviderMu.Lock()
	defer mirrorProviderMu.Unlock()

	mirrorEnabledProvider = provider
}

// MirrorEnabledFromProvider 读取当前镜像开关。
//
// 未注入 Provider（例如单元测试或尚未装配）时返回 false：镜像默认关闭是安全默认，
// 关闭状态下只会走官方源，不会因为读不到设置而放行任何额外 host。
func MirrorEnabledFromProvider() bool {
	mirrorProviderMu.RLock()
	provider := mirrorEnabledProvider
	mirrorProviderMu.RUnlock()

	if provider == nil {
		return false
	}
	return provider()
}

// NewSourceRegistryFromProvider 用 Provider 的当前值构造来源注册表。
//
// 生产路径应使用它（或显式传入开关值），不要写死 NewSourceRegistry(false)：
// 后者会把开关固定在调用时刻，读不到用户后续修改的设置。
func NewSourceRegistryFromProvider() *SourceRegistry {
	return NewSourceRegistry(MirrorEnabledFromProvider())
}

// SourceRegistry 是下载来源注册表。
//
// 它同时承担两件事：决定「一个官方地址可以映射出哪些候选地址」，
// 以及决定「一个地址是否允许被请求」。两者共用同一份白名单，
// 避免出现「能重写成镜像地址但镜像地址不被校验」的漏洞。
type SourceRegistry struct {
	// mirrorEnabled 表示镜像开关是否打开。
	mirrorEnabled bool

	// mirrors 为已注册的镜像列表（顺序即回退顺序）。
	mirrors []MirrorEndpoint
}

// NewSourceRegistry 构造来源注册表，镜像列表取内置参考镜像。
func NewSourceRegistry(mirrorEnabled bool) *SourceRegistry {
	return NewSourceRegistryWithMirrors(mirrorEnabled, []MirrorEndpoint{{Host: defaultMirrorHost}})
}

// NewSourceRegistryWithMirrors 用自定义镜像列表构造来源注册表。
func NewSourceRegistryWithMirrors(mirrorEnabled bool, mirrors []MirrorEndpoint) *SourceRegistry {
	cleaned := make([]MirrorEndpoint, 0, len(mirrors))
	for _, mirror := range mirrors {
		host := strings.ToLower(strings.TrimSpace(mirror.Host))
		if host == "" {
			continue
		}
		cleaned = append(cleaned, MirrorEndpoint{Host: host})
	}
	return &SourceRegistry{
		mirrorEnabled: mirrorEnabled,
		mirrors:       cleaned,
	}
}

// MirrorEnabled 返回镜像开关状态。
func (r *SourceRegistry) MirrorEnabled() bool {
	if r == nil {
		return false
	}
	return r.mirrorEnabled
}

// Mirrors 返回已注册镜像的副本。
func (r *SourceRegistry) Mirrors() []MirrorEndpoint {
	if r == nil {
		return nil
	}
	mirrors := make([]MirrorEndpoint, len(r.mirrors))
	copy(mirrors, r.mirrors)
	return mirrors
}

// RewriteMirrorURL 按 registry 当前的开关状态重写官方地址。
//
// 镜像关闭时返回 (nil, false)：调用方据此只走官方源。
func (r *SourceRegistry) RewriteMirrorURL(official string) ([]string, bool) {
	if r == nil || !r.mirrorEnabled || len(r.mirrors) == 0 {
		return nil, false
	}
	parsed, ok := parseOfficialURL(official)
	if !ok {
		return nil, false
	}
	mirrors := make([]string, 0, len(r.mirrors))
	for _, mirror := range r.mirrors {
		if rewritten, built := mirrorURL(mirror.Host, parsed); built {
			mirrors = append(mirrors, rewritten)
		}
	}
	if len(mirrors) == 0 {
		return nil, false
	}
	return mirrors, true
}

// RewriteMirrorURL 是镜像重写规则的纯函数形式（卡面第 14 条要求可单测的纯函数）。
//
// 它只表达「规则」：https + 官方白名单 host → 按挂载前缀重写，path 逐字节不变。
// 是否真的启用镜像由 SourceRegistry.MirrorEnabled 决定，生产路径必须走
// registry 方法，避免绕过开关。
func RewriteMirrorURL(official string) ([]string, bool) {
	parsed, ok := parseOfficialURL(official)
	if !ok {
		return nil, false
	}
	rewritten, built := mirrorURL(defaultMirrorHost, parsed)
	if !built {
		return nil, false
	}
	return []string{rewritten}, true
}

// parseOfficialURL 校验并解析一个可重写的官方地址。
func parseOfficialURL(official string) (*url.URL, bool) {
	trimmed := strings.TrimSpace(official)
	if trimmed == "" {
		return nil, false
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return nil, false
	}
	if !strings.EqualFold(parsed.Scheme, officialScheme) {
		return nil, false
	}
	if parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		// 带查询串或片段的地址不做镜像重写：镜像通常不认签名参数，
		// 重写后反而会拿到 403，白白多一次失败尝试。
		return nil, false
	}
	if !rewritableOfficialHosts[strings.ToLower(parsed.Hostname())] {
		return nil, false
	}
	return parsed, true
}

// mirrorURL 把官方地址按挂载前缀拼到镜像 host 上。
//
// 拼装后重新解析并逐项比对，确保 path 与官方地址**逐字节相同**：
// 镜像只是换了 host，路径语义不能有任何变化。
func mirrorURL(mirrorHost string, official *url.URL) (string, bool) {
	host := strings.ToLower(strings.TrimSpace(mirrorHost))
	if host == "" || official == nil {
		return "", false
	}
	officialHost := strings.ToLower(official.Hostname())
	if officialHost == "" {
		return "", false
	}
	path := official.EscapedPath()
	if path == "" {
		path = "/"
	}
	rewritten := officialScheme + "://" + host + mirrorMountPrefix + officialHost + path

	parsed, err := url.Parse(rewritten)
	if err != nil || parsed == nil {
		return "", false
	}
	if !strings.EqualFold(parsed.Scheme, officialScheme) {
		return "", false
	}
	if !strings.EqualFold(parsed.Hostname(), host) {
		return "", false
	}
	if parsed.EscapedPath() != mirrorMountPrefix+officialHost+path {
		return "", false
	}
	return rewritten, true
}

// Candidates 返回一个官方地址的候选下载顺序：官方在前，镜像在后。
//
// 镜像开关关闭或地址不可重写时，只返回官方地址本身。
func (r *SourceRegistry) Candidates(official string) ([]string, *Error) {
	trimmed := strings.TrimSpace(official)
	if trimmed == "" {
		return nil, newInputError(ErrorCodeSource, messageSourceEmpty)
	}
	if appErr := r.ValidateURL(trimmed); appErr != nil {
		return nil, appErr
	}
	candidates := []string{trimmed}
	if mirrors, ok := r.RewriteMirrorURL(trimmed); ok {
		candidates = append(candidates, mirrors...)
	}
	return candidates, nil
}

// ValidateURL 校验一个下载地址是否允许请求。
//
// 允许的集合 = https + 5 个官方 host + 已启用镜像的 host。
// 注意镜像 host 只有在开关打开时才被接受，避免「关了镜像还能直连镜像地址」。
func (r *SourceRegistry) ValidateURL(raw string) *Error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return newInputError(ErrorCodeSource, messageSourceEmpty)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return newInputError(ErrorCodeSource, messageSourceInvalid)
	}
	if !strings.EqualFold(parsed.Scheme, officialScheme) {
		return newInputError(ErrorCodeSource, messageSourceNotHTTPS)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return newInputError(ErrorCodeSource, messageSourceInvalid)
	}
	if rewritableOfficialHosts[host] {
		return nil
	}
	if r != nil && r.mirrorEnabled {
		for _, mirror := range r.mirrors {
			if mirrorHostOnly(mirror.Host) == host {
				return nil
			}
		}
	}
	return newInputError(ErrorCodeSource, messageSourceHostNotAllowed)
}

// mirrorHostOnly 去掉镜像 host 里可能带的端口。
func mirrorHostOnly(host string) string {
	trimmed := strings.ToLower(strings.TrimSpace(host))
	if trimmed == "" {
		return ""
	}
	if parsed, err := url.Parse(officialScheme + "://" + trimmed); err == nil && parsed != nil {
		return strings.ToLower(parsed.Hostname())
	}
	return trimmed
}

// HostOf 返回地址的 host（小写），用于日志。
//
// 解析失败时返回空串：宁可日志少一个字段，也不要把原始地址打进日志。
func HostOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// PathPrefixOf 返回路径的前两段，用于日志。
//
// 完整路径包含版本号、哈希与文件名，属于「够用就好」里的「不该记」；
// 前两段足以看出是 libraries / assets / 版本清单 中的哪一类。
func PathPrefixOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return ""
	}
	// 没有 host 的地址不是真实的下载地址（日志里路径前缀也无从对照），
	// 直接返回空串，避免把任意文本当成路径写进日志。
	if strings.TrimSpace(parsed.Hostname()) == "" {
		return ""
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	kept := make([]string, 0, logPathSegmentCount)
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		kept = append(kept, segment)
		if len(kept) == logPathSegmentCount {
			break
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, "/")
}
