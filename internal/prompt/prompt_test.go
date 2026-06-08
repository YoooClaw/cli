package prompt

import (
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/errs"
)

func TestAskCore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		def     string
		want    string
		wantOut string
	}{
		{"answer wins", "hello\n", "def", "hello", "Q [def]: "},
		{"empty uses default", "\n", "def", "def", "Q [def]: "},
		{"trims whitespace", "  spaced  \n", "", "spaced", "Q: "},
		{"empty no default", "\n", "", "", "Q: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			got, err := ask(strings.NewReader(tt.input), &out, "Q", tt.def)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ask = %q, want %q", got, tt.want)
			}
			if out.String() != tt.wantOut {
				t.Errorf("prompt text = %q, want %q", out.String(), tt.wantOut)
			}
		})
	}
}

func TestConfirmCore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input      string
		defaultYes bool
		want       bool
		wantHint   string
	}{
		{"y\n", false, true, "(y/N)"},
		{"yes\n", false, true, "(y/N)"},
		{"n\n", true, false, "(Y/n)"},
		{"\n", true, true, "(Y/n)"},
		{"\n", false, false, "(y/N)"},
		{"garbage\n", true, false, "(Y/n)"},
		{"YES\n", false, true, "(y/N)"},
	}
	for _, tt := range tests {
		var out strings.Builder
		got, err := confirm(strings.NewReader(tt.input), &out, "OK?", tt.defaultYes)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("confirm(%q, def=%v) = %v, want %v", tt.input, tt.defaultYes, got, tt.want)
		}
		if !strings.Contains(out.String(), tt.wantHint) {
			t.Errorf("prompt %q missing hint %q", out.String(), tt.wantHint)
		}
	}
}

func TestReadAll(t *testing.T) {
	t.Parallel()
	got, err := readAll(strings.NewReader("piped body"))
	if err != nil || got != "piped body" {
		t.Errorf("readAll = %q, %v", got, err)
	}
}

func TestEnsureInteractiveNonTTY(t *testing.T) {
	t.Parallel()
	// 测试环境 stdin 非 TTY，Ask/Confirm 应直接拒绝而不挂起。
	if _, err := Ask("q", ""); !isNotInteractive(err) {
		t.Errorf("Ask in non-tty should return NOT_INTERACTIVE, got %v", err)
	}
	if _, err := Confirm("q", true); !isNotInteractive(err) {
		t.Errorf("Confirm in non-tty should return NOT_INTERACTIVE, got %v", err)
	}
}

func isNotInteractive(err error) bool {
	e, ok := err.(*errs.Error)
	return ok && e.Code == errs.CodeNotInteractive
}
