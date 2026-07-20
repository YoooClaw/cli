package fsutil

import (
	"strings"
	"testing"
)

func TestIsSafeName(t *testing.T) {
	t.Parallel()
	valid := []string{"rec-123", "a", "UUID_0f3.ogg", "中文规则名", ".hidden", "a b"}
	for _, s := range valid {
		if !IsSafeName(s) {
			t.Errorf("IsSafeName(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"", ".", "..",
		"../evil", "..\\evil", "a/b", `a\b`,
		"/abs", "C:evil", "a:b",
		"nul\x00byte",
		strings.Repeat("x", 201),
	}
	for _, s := range invalid {
		if IsSafeName(s) {
			t.Errorf("IsSafeName(%q) = true, want false", s)
		}
	}
}
