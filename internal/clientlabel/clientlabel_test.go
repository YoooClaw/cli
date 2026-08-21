package clientlabel

import "testing"

func TestShared(t *testing.T) {
	for _, label := range []string{"", "  ", "default", "Default", "legacy"} {
		if !Shared(label) {
			t.Errorf("Shared(%q) = false, want true", label)
		}
	}
	for _, label := range []string{"phone-a", "8bf56c19debc4fd587378c88a4f419d8"} {
		if Shared(label) {
			t.Errorf("Shared(%q) = true, want false", label)
		}
	}
}

func TestVisible(t *testing.T) {
	cases := []struct {
		entry, scope string
		want         bool
	}{
		{"phone-a", "", true},         // 本机视角不隔离
		{"phone-a", "phone-a", true},  // 自己的数据
		{"phone-a", "phone-b", false}, // 别人的数据
		{"default", "phone-b", true},  // 老数据对所有人可见
		{"", "phone-b", true},
		{"legacy", "phone-b", true},
		{"phone-a", "default", true}, // scope 本身没来源信息时不隔离
	}
	for _, c := range cases {
		if got := Visible(c.entry, c.scope); got != c.want {
			t.Errorf("Visible(%q, %q) = %v, want %v", c.entry, c.scope, got, c.want)
		}
	}
}
