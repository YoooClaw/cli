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

// renderTable 极简表格渲染：对象数组 → 列对齐文本；其余 fallback 到 pretty JSON。
func renderTable(data any) string {
	arr, ok := data.([]any)
	if !ok || len(arr) == 0 {
		return marshalJSON(data, true)
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
		widths[i] = len(col)
		for _, row := range rows {
			if l := len(cell(row[col])); l > widths[i] {
				widths[i] = l
			}
		}
	}
	pad := func(s string, w int) string {
		if len(s) < w {
			return s + strings.Repeat(" ", w-len(s))
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
