package version

import (
	"strings"
	"testing"
)

func TestVersionDefault(t *testing.T) {
	t.Parallel()
	if Version == "" {
		t.Error("Version should never be empty")
	}
}

func TestDistFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		exe  string
		want string
	}{
		{"/usr/local/bin/yoooclaw", "native"},
		{"/home/u/project/node_modules/.bin/yoooclaw", "npm"},
		{"/opt/node_modules/@yoooclaw/cli-darwin-arm64/yoooclaw", "npm"},
		{"", "native"},
	}
	for _, tt := range tests {
		if got := distFor(tt.exe); got != tt.want {
			t.Errorf("distFor(%q) = %q, want %q", tt.exe, got, tt.want)
		}
	}
}

func TestDist(t *testing.T) {
	t.Parallel()
	// 测试二进制不在 node_modules 下，应判定 native。
	if got := Dist(); got != "native" && got != "npm" {
		t.Errorf("Dist() = %q, want native|npm", got)
	}
	if strings.TrimSpace(Dist()) == "" {
		t.Error("Dist() should never be empty")
	}
}
