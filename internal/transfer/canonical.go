package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// 迁移包的 recordId / recordVersion 是对 canonical JSON 取 SHA-256，必须与
// phone-notifications 插件（src/transfer/package.ts 的 canonical）逐字节一致，
// 否则两端互导时校验必然失败。这里复刻的是 JS 语义：
//   - 对象按 key 的 UTF-16 码元序排序（Array.prototype.sort 默认比较）；
//   - 字符串按 JSON.stringify 转义（不转义 < > & 与 U+2028/2029，控制字符用小写 \u00xx）；
//   - 数字按 Number.prototype.toString 输出（1e21 以上/1e-6 以下走指数形式）。
// 输入是 encoding/json 解出来的 any（map[string]any / []any / string / float64 / bool / nil）。

func canonical(value any) string {
	var b strings.Builder
	writeCanonical(&b, value)
	return b.String()
}

func writeCanonical(b *strings.Builder, value any) {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case float64:
		b.WriteString(jsNumber(v))
	case int:
		b.WriteString(jsNumber(float64(v)))
	case int64:
		b.WriteString(jsNumber(float64(v)))
	case string:
		writeJSString(b, v)
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonical(b, item)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSString(b, k)
			b.WriteByte(':')
			writeCanonical(b, v[k])
		}
		b.WriteByte('}')
	default:
		// 结构体等非 JSON 原生值：先走一遍 JSON 再规范化。
		writeCanonical(b, toAny(v))
	}
}

// toAny 把任意可序列化值转成 encoding/json 的通用表示。
func toAny(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func utf16Less(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

const hexDigits = "0123456789abcdef"

func writeJSString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[r>>4])
				b.WriteByte(hexDigits[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// jsNumber 复刻 JS Number.prototype.toString（JSON.stringify 对有限数的输出）。
func jsNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // 例如 1e+21、1.5e-07
	mant, exp, _ := strings.Cut(s, "e")
	sign := exp[:1]
	digits := strings.TrimLeft(exp[1:], "0")
	if digits == "" {
		digits = "0"
	}
	return mant + "e" + sign + digits
}

// jsString 复刻 JS String(value) 对 JSON 标量的结果（身份字段用）。
func jsString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return jsNumber(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			if item != nil {
				parts[i] = jsString(item)
			}
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

func sha(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
