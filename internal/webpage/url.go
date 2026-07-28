package webpage

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

// trackingParams 是除 utm_* 前缀外要剥掉的追踪参数。
// 口径来自 page-context-design.md §4.4，四端必须一致。
var trackingParams = map[string]bool{
	"fbclid":  true,
	"gclid":   true,
	"mc_cid":  true,
	"mc_eid":  true,
	"ref_src": true,
}

// NormalizeURL 把 URL 规范化成决定落盘文件名的 canonicalUrl。
//
// 规则（page-context-design.md §4.4，唯一来源；用例见 testdata/web-page-vectors.json）：
// 去 fragment、去 utm_*/追踪参数、query 按名稳定排序并按 urlencoded 重新序列化、
// 去 path 末尾斜杠、scheme/host 小写、去默认端口。**不**升级 http→https、
// **不**去 www.、**不**动 path 大小写、**不**去分页参数——那些可能是不同内容。
//
// 规则跨端漂移的后果是同一网页落成两个文件、去重失效，所以这里不做任何
// "顺手更干净一点"的额外处理。
func NormalizeURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("url 不能为空")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("url 无法解析：%w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("只接受 http/https，收到 %q", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("url 缺少 host")
	}

	var b strings.Builder
	b.WriteString(scheme)
	b.WriteString("://")
	if u.User != nil {
		b.WriteString(u.User.String())
		b.WriteString("@")
	}
	b.WriteString(host)
	if port := u.Port(); port != "" && !isDefaultPort(scheme, port) {
		b.WriteString(":")
		b.WriteString(port)
	}
	b.WriteString(normalizePath(u.EscapedPath()))
	if query := normalizeQuery(u.RawQuery); query != "" {
		b.WriteString("?")
		b.WriteString(query)
	}
	return b.String(), nil
}

func isDefaultPort(scheme, port string) bool {
	return (scheme == "http" && port == "80") || (scheme == "https" && port == "443")
}

func normalizePath(path string) string {
	if path == "" {
		return "/"
	}
	if path == "/" {
		return path
	}
	trimmed := strings.TrimRight(path, "/")
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

// normalizeQuery 复刻 URLSearchParams 的行为：解析 → 剥追踪参数 →
// 按名稳定排序（同名参数保持原相对顺序）→ 用 urlencoded 序列化器重新编码。
func normalizeQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	type pair struct {
		name  string
		value string
	}
	var pairs []pair
	for _, chunk := range strings.Split(rawQuery, "&") {
		if chunk == "" {
			continue
		}
		name, value, _ := strings.Cut(chunk, "=")
		decodedName := decodeQueryComponent(name)
		if isTrackingParam(decodedName) {
			continue
		}
		pairs = append(pairs, pair{name: decodedName, value: decodeQueryComponent(value)})
	}
	// URLSearchParams.sort() 只按 name 排序且稳定；同名参数的先后是有意义的
	// （?tag=a&tag=b 与 ?tag=b&tag=a 是不同的 URL），不能一起排掉。
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].name < pairs[j].name })

	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteString("&")
		}
		b.WriteString(encodeQueryComponent(p.name))
		b.WriteString("=")
		b.WriteString(encodeQueryComponent(p.value))
	}
	return b.String()
}

func isTrackingParam(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "utm_") || trackingParams[lower]
}

func decodeQueryComponent(s string) string {
	// 坏转义（单独的 % 等）在这里按字面量保留，与浏览器一致——拒收整个 URL
	// 只会让一次收藏失败，而这条链路上收藏是用户已经点过的。
	decoded, err := url.QueryUnescape(s)
	if err != nil {
		return strings.ReplaceAll(s, "+", " ")
	}
	return decoded
}

// encodeQueryComponent 是 WHATWG application/x-www-form-urlencoded 序列化器：
// 保留 ALPHA / DIGIT / *-._，空格写 +，其余按 UTF-8 逐字节大写百分号编码。
//
// 不能用 url.QueryEscape：它转义 * 而保留 ~，与浏览器相反，会让扩展与 CLI
// 对同一 URL 算出不同的 hash。
func encodeQueryComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '*', c == '-', c == '.', c == '_':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// slugMaxRunes 是 slug 的长度上限。加上 8 位 hash 前缀与扩展名后，
// 即使全是 3 字节的 CJK 也不会撞上文件系统的 255 字节名字上限。
const slugMaxRunes = 60

// Slug 把标题转成文件名里可读的那一段：小写，只留字母与十进制数字
// （CJK 属于字母，予以保留），其余一律折成单个连字符。
//
// slug 不承担唯一性——那是 hash 前缀的职责。标题改了会算出新 slug，
// 由 Ingest 改名而不是新建文件。
func Slug(title string) string {
	var b strings.Builder
	appended := 0
	pendingDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if appended >= slugMaxRunes {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			appended++
			pendingDash = false
			continue
		}
		if !pendingDash && b.Len() > 0 {
			b.WriteRune('-')
			appended++
			pendingDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "untitled"
	}
	return slug
}
