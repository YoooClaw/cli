package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/errs"
)

func TestResolveFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		requested string
		isTTY     bool
		want      Format
		wantErr   bool
	}{
		{"explicit json", "json", true, JSON, false},
		{"explicit table", "table", false, Table, false},
		{"explicit ndjson", "ndjson", true, NDJSON, false},
		{"explicit pretty", "pretty", false, Pretty, false},
		{"auto on tty -> pretty", "auto", true, Pretty, false},
		{"auto off tty -> json", "auto", false, JSON, false},
		{"empty on tty -> pretty", "", true, Pretty, false},
		{"empty off tty -> json", "", false, JSON, false},
		{"unsupported", "yaml", true, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveFormat(tt.requested, tt.isTTY)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.requested)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderResultJSON(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	RenderResult(&b, map[string]any{"ok": true, "n": 1}, JSON)
	got := strings.TrimSpace(b.String())
	if got != `{"n":1,"ok":true}` {
		t.Errorf("json render = %q", got)
	}
}

func TestRenderResultJSONNoHTMLEscape(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	RenderResult(&b, map[string]any{"url": "a&b<c>"}, JSON)
	if !strings.Contains(b.String(), "a&b<c>") {
		t.Errorf("HTML should not be escaped: %q", b.String())
	}
}

func TestRenderResultNDJSON(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	RenderResult(&b, []any{map[string]any{"a": 1}, map[string]any{"a": 2}}, NDJSON)
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 || lines[0] != `{"a":1}` || lines[1] != `{"a":2}` {
		t.Errorf("ndjson render = %q", b.String())
	}
}

func TestRenderResultTable(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	RenderResult(&b, []any{
		map[string]any{"id": "a", "n": 1},
		map[string]any{"id": "bb", "n": 22},
	}, Table)
	out := b.String()
	if !strings.Contains(out, "id") || !strings.Contains(out, "n") {
		t.Errorf("table missing header: %q", out)
	}
	if !strings.Contains(out, "--") {
		t.Errorf("table missing separator row: %q", out)
	}
}

func TestRenderResultTableAlignsWideCharacters(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	RenderResult(&b, []any{
		map[string]any{"name": "测试录音", "status": "ready"},
		map[string]any{"name": "ascii", "status": "ready"},
	}, Table)
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("table lines = %d, want 4: %q", len(lines), b.String())
	}
	firstStatus := strings.Index(lines[2], "ready")
	secondStatus := strings.Index(lines[3], "ready")
	if firstStatus < 0 || secondStatus < 0 {
		t.Fatalf("table is missing status values: %q", b.String())
	}
	if firstWidth, secondWidth := displayWidth(lines[2][:firstStatus]), displayWidth(lines[3][:secondStatus]); firstWidth != secondWidth {
		t.Errorf("wide-character columns are not aligned: widths %d and %d\n%s", firstWidth, secondWidth, b.String())
	}
}

func TestRenderResultTableUnwrapsArrayEnvelope(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	RenderResult(&b, map[string]any{
		"ok":    true,
		"total": 2,
		"recordings": []any{
			map[string]any{"id": "r1", "name": "first"},
			map[string]any{"id": "r2", "name": "second"},
		},
	}, Table)
	out := b.String()
	for _, want := range []string{"id", "name", "r1", "first", "r2", "second"} {
		if !strings.Contains(out, want) {
			t.Errorf("unwrapped table missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, `"recordings"`) {
		t.Errorf("array envelope fell back to JSON: %s", out)
	}
}

func TestRenderResultTableUnwrapsObjectEnvelope(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	RenderResult(&b, map[string]any{
		"ok":        true,
		"recording": map[string]any{"id": "r1", "name": "latest"},
	}, Table)
	out := b.String()
	for _, want := range []string{"id", "name", "r1", "latest"} {
		if !strings.Contains(out, want) {
			t.Errorf("unwrapped detail table missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, `"recording"`) {
		t.Errorf("object envelope fell back to JSON: %s", out)
	}
}

func TestRenderResultTableUnwrapsEmptyArrayEnvelope(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	RenderResult(&b, map[string]any{
		"ok":         true,
		"total":      0,
		"recordings": []any{},
	}, Table)
	if got := strings.TrimSpace(b.String()); got != "(no data)" {
		t.Errorf("empty envelope table = %q, want %q", got, "(no data)")
	}
}

func TestRenderResultTableFallsBackToJSON(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	RenderResult(&b, map[string]any{"not": "array"}, Table)
	if !strings.Contains(b.String(), `"not": "array"`) {
		t.Errorf("non-array table should fall back to pretty JSON: %q", b.String())
	}
}

func TestRenderResultTableAmbiguousEnvelopeFallsBackToJSON(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	RenderResult(&b, map[string]any{
		"installed": []any{map[string]any{"name": "one"}},
		"skipped":   []any{map[string]any{"name": "two"}},
	}, Table)
	if !strings.Contains(b.String(), `"installed"`) || !strings.Contains(b.String(), `"skipped"`) {
		t.Errorf("ambiguous envelope should fall back to pretty JSON: %q", b.String())
	}
}

func TestRenderErrorStructured(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	RenderError(&b, errs.New(errs.CodeNotFound, "missing").WithHint("check id"), JSON)
	out := b.String()
	for _, want := range []string{`"ok":false`, `"code":"YOOOCLAW_NOT_FOUND"`, `"message":"missing"`, `"hint":"check id"`} {
		if !strings.Contains(out, want) {
			t.Errorf("error render missing %q: %s", want, out)
		}
	}
}

func TestRenderErrorPlainError(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	RenderError(&b, errsErrorf("boom"), JSON)
	out := b.String()
	if !strings.Contains(out, `"code":"YOOOCLAW_UNKNOWN"`) || !strings.Contains(out, `"message":"boom"`) {
		t.Errorf("plain error should map to UNKNOWN: %s", out)
	}
}

// errsErrorf 返回一个非 *errs.Error 的普通 error，用于验证 RenderError 的 fallback 分支。
func errsErrorf(msg string) error { return &plainErr{msg} }

type plainErr struct{ s string }

func (e *plainErr) Error() string { return e.s }
