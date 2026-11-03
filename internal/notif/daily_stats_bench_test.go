package notif

import (
	"fmt"
	"os"
	"testing"
)

type statsBenchmarkLogger struct{ b *testing.B }

func (l statsBenchmarkLogger) Info(string)     {}
func (l statsBenchmarkLogger) Warn(msg string) { l.b.Fatal(msg) }

// Compare actual cached reads with rebuilding the same day's source. Fixture
// setup is excluded and all writes stay inside the benchmark's temporary dir.
func BenchmarkReadDailyStats(b *testing.B) {
	for _, size := range []int{200, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			s := NewStorage(b.TempDir(), PluginConfig{}, statsBenchmarkLogger{b})
			if err := s.Init(); err != nil {
				b.Fatal(err)
			}
			const date = "2026-10-30"
			entries := make([]StoredNotification, size)
			for i := range entries {
				entries[i] = StoredNotification{AppName: "x", Title: "通知", Content: fmt.Sprintf("通知内容 %d", i), Timestamp: "2026-10-30T03:00:00Z"}
			}
			lines, err := encodeJSONLines(entries)
			if err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(s.dayPath(date), lines, 0o600); err != nil {
				b.Fatal(err)
			}
			if _, err := s.ReadDailyStats(date); err != nil {
				b.Fatal(err)
			}
			for _, rebuild := range []bool{false, true} {
				name := "cached"
				if rebuild {
					name = "rebuild"
				}
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if rebuild {
							if err := os.Remove(s.dailyStatsPath(date)); err != nil {
								b.Fatal(err)
							}
						}
						stats, err := s.ReadDailyStats(date)
						if err != nil || stats.Count != int64(size) {
							b.Fatalf("read: count=%d err=%v", stats.Count, err)
						}
					}
				})
			}
		})
	}
}
