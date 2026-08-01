// Package output 统一所有命令的 stdout 序列化。
//
// 支持 --format json|pretty|table|ndjson；错误与正常输出共用 {ok:false,error:{...}} schema。
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/YoooClaw/cli/internal/errs"
)

// Format 是输出格式。
type Format string

const (
	JSON   Format = "json"
	Pretty Format = "pretty"
	Table  Format = "table"
	NDJSON Format = "ndjson"
)

var allFormats = []Format{JSON, Pretty, Table, NDJSON}

// ResolveFormat 解析 --format；auto/空时按 TTY 判定（pretty / json）。
func ResolveFormat(requested string, isTTY bool) (Format, error) {
	if requested != "" && requested != "auto" {
		for _, f := range allFormats {
			if string(f) == requested {
				return f, nil
			}
		}
		return "", errs.New(errs.CodeInvalidArgument, "不支持的输出格式："+requested).
			WithHint("可选值：json | pretty | table | ndjson | auto")
	}
	if isTTY {
		return Pretty, nil
	}
	return JSON, nil
}

// StdoutIsTTY 报告 stdout 是否为终端。
func StdoutIsTTY() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// marshalJSON 序列化为 JSON，不转义 HTML（与 JS JSON.stringify 对齐）。
func marshalJSON(v any, indent bool) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("%v", v)
	}
	return strings.TrimRight(buf.String(), "\n")
}

func writeLine(w io.Writer, text string) {
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, _ = io.WriteString(w, text)
}

// RenderResult 渲染一次成功结果到 w。
func RenderResult(w io.Writer, data any, format Format) {
	switch format {
	case JSON:
		writeLine(w, marshalJSON(data, false))
	case Pretty:
		writeLine(w, marshalJSON(data, true))
	case NDJSON:
		for _, row := range toRows(data) {
			writeLine(w, marshalJSON(row, false))
		}
	case Table:
		writeLine(w, renderTable(data))
	}
}

// RenderError 渲染错误（统一 {ok:false,error:{...}} schema）。
func RenderError(w io.Writer, err error, format Format) {
	var payloadErr map[string]any
	var ye *errs.Error
	if e, ok := err.(*errs.Error); ok {
		ye = e
	}
	if ye != nil {
		payloadErr = ye.Payload()
	} else {
		payloadErr = map[string]any{"code": errs.CodeUnknown, "message": err.Error()}
	}
	payload := map[string]any{"ok": false, "error": payloadErr}
	if format == Pretty {
		writeLine(w, marshalJSON(payload, true))
	} else {
		writeLine(w, marshalJSON(payload, false))
	}
}

func toRows(data any) []any {
	if arr, ok := data.([]any); ok {
		return arr
	}
	return []any{data}
}

// renderTable 极简表格渲染：对象数组 → 列对齐文本。
//
// CLI 命令通常返回 {"ok":true,"total":N,"recordings":[...]} 这类标准结果包；
// 当结果包中只有一个数组字段时，自动把该字段作为表格行。单条详情命令常返回
// {"ok":true,"recording":{...}}，同样将唯一的嵌套对象渲染为单行表格。
// 其他结构仍 fallback 到 pretty JSON。
func renderTable(data any) string {
	arr, ok := tableRows(data)
	if !ok {
		return marshalJSON(data, true)
	}
	if len(arr) == 0 {
		return "(no data)"
	}
	rows := make([]map[string]any, 0, len(arr))
	for _, r := range arr {
		m, ok := r.(map[string]any)
		if !ok {
			return marshalJSON(data, true)
		}
		rows = append(rows, m)
	}
	var columns []string
	seen := map[string]bool{}
	for _, row := range rows {
		keys := make([]string, 0, len(row))
		for k := range row {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				columns = append(columns, k)
			}
		}
	}
	cell := func(v any) string {
		if v == nil {
			return ""
		}
		switch t := v.(type) {
		case string:
			return t
		case map[string]any, []any:
			return marshalJSON(t, false)
		default:
			return fmt.Sprintf("%v", t)
		}
	}
	widths := make([]int, len(columns))
	for i, col := range columns {
		widths[i] = displayWidth(col)
		for _, row := range rows {
			if l := displayWidth(cell(row[col])); l > widths[i] {
				widths[i] = l
			}
		}
	}
	pad := func(s string, w int) string {
		if current := displayWidth(s); current < w {
			return s + strings.Repeat(" ", w-current)
		}
		return s
	}
	var b strings.Builder
	for i, col := range columns {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(pad(col, widths[i]))
	}
	b.WriteString("\n")
	for i, w := range widths {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(strings.Repeat("-", w))
	}
	for _, row := range rows {
		b.WriteString("\n")
		for i, col := range columns {
			if i > 0 {
				b.WriteString("  ")
			}
			b.WriteString(pad(cell(row[col]), widths[i]))
		}
	}
	return b.String()
}

// displayWidth 返回字符串在常见等宽终端中的显示列宽。
// 中文、日文、韩文和全角字符占两列；组合字符不额外占列。
func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r):
			continue
		case isWideRune(r):
			width += 2
		default:
			width++
		}
	}
	return width
}

func isWideRune(r rune) bool {
	return r >= 0x1100 &&
		(r <= 0x115f ||
			r == 0x2329 || r == 0x232a ||
			(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
			(r >= 0xac00 && r <= 0xd7a3) ||
			(r >= 0xf900 && r <= 0xfaff) ||
			(r >= 0xfe10 && r <= 0xfe19) ||
			(r >= 0xfe30 && r <= 0xfe6f) ||
			(r >= 0xff00 && r <= 0xff60) ||
			(r >= 0xffe0 && r <= 0xffe6) ||
			(r >= 0x1f300 && r <= 0x1faff) ||
			(r >= 0x20000 && r <= 0x3fffd))
}

// tableRows 提取可渲染的表格行。
//
// 除顶层数组外，还支持标准结果包中唯一的数组或对象字段。只在候选字段唯一时
// 自动解包，避免对包含多个结果集合的复杂响应做含糊选择。
func tableRows(data any) ([]any, bool) {
	if arr, ok := data.([]any); ok {
		return arr, true
	}
	envelope, ok := data.(map[string]any)
	if !ok {
		return nil, false
	}

	var arrayRows []any
	arrayCandidates := 0
	for _, value := range envelope {
		if arr, ok := value.([]any); ok {
			arrayRows = arr
			arrayCandidates++
		}
	}
	if arrayCandidates == 1 {
		return arrayRows, true
	}
	if arrayCandidates > 1 {
		return nil, false
	}

	var objectRow map[string]any
	objectCandidates := 0
	for _, value := range envelope {
		if row, ok := value.(map[string]any); ok {
			objectRow = row
			objectCandidates++
		}
	}
	if objectCandidates == 1 {
		return []any{objectRow}, true
	}
	return nil, false
}
