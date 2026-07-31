package recording

import "testing"

func TestFormatDurationDisplayUsesOnlyReachedUnits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		seconds float64
		want    string
	}{
		{"zero", 0, "0s"},
		{"seconds only", 16, "16s"},
		{"fraction truncated", 16.78, "16s"},
		{"minute boundary", 60, "1m 0s"},
		{"minutes and seconds", 76, "1m 16s"},
		{"below hour", 3599, "59m 59s"},
		{"hour boundary", 3600, "1h 0m 0s"},
		{"hours minutes seconds", 3661, "1h 1m 1s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatDurationDisplay(tt.seconds); got != tt.want {
				t.Fatalf("FormatDurationDisplay(%v) = %q, want %q", tt.seconds, got, tt.want)
			}
		})
	}
}
