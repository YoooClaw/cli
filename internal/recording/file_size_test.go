package recording

import "testing"

func TestFormatShortFileSizeMatchesAppRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		size int64
		want string
	}{
		{"missing", -1, "--"},
		{"zero", 0, "0 B"},
		{"bytes", 900, "900 B"},
		{"sub unit two decimals", 901, "0.90 kB"},
		{"one digit rounds down", 5_940_000, "5.9 MB"},
		{"one digit half up", 5_950_000, "6 MB"},
		{"integer rounds down", 12_400_000, "12 MB"},
		{"integer half up", 12_500_000, "13 MB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatShortFileSize(tt.size); got != tt.want {
				t.Fatalf("FormatShortFileSize(%d) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}
