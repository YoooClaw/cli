package notif

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

func readPersistedStats(t *testing.T, s *Storage, date string) dailyStatsIndex {
	t.Helper()
	raw, err := os.ReadFile(s.dailyStatsPath(date))
	if err != nil {
		t.Fatal(err)
	}
	var idx dailyStatsIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	return idx
}

func expectStats(t *testing.T, s *Storage, date string, count int64, latest string) DailyStats {
	t.Helper()
	stats, err := s.ReadDailyStats(date)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != count || stats.Date != date || stats.TimeZone != time.Local.String() {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if count == 0 {
		if stats.Latest != nil {
			t.Fatalf("empty day has latest: %+v", stats.Latest)
		}
	} else if stats.Latest == nil || stats.Latest.Content != latest {
		t.Fatalf("latest = %+v, want content %q", stats.Latest, latest)
	}
	return stats
}

func TestDailyStatsIngestDedupLateUploadsAndRestart(t *testing.T) {
	s := newStorage(t)
	day := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	date := day.Format(time.DateOnly)
	old, newest := notifAt("old", day, "old"), notifAt("new", day.Add(time.Hour), "new")
	result := s.Ingest([]RawNotification{newest, old, old, notifAt("", day, "old"), {Timestamp: "bad"}}, "phone")
	if result.Ingested != 2 || result.DedupedByID != 1 || result.DedupedByContent != 1 || result.Invalid != 1 {
		t.Fatalf("unexpected ingest: %+v", result)
	}
	// Verify indexing happens on writes, before the first stats query.
	idx := readPersistedStats(t, s, date)
	if idx.Count != 2 || idx.Latest.Content != "new" {
		t.Fatalf("write did not update stats: %+v", idx)
	}
	s.Ingest([]RawNotification{notifAt("late", day.Add(-time.Hour), "late")}, "other-phone")
	expectStats(t, s, date, 3, "new")
	// Same instant in a different offset must compare as an instant, not a string.
	tie := notifAt("tie", day.Add(time.Hour), "tie")
	tie.Timestamp = day.Add(time.Hour).UTC().Format(time.RFC3339Nano)
	s.Ingest([]RawNotification{tie}, "other-phone")
	expectStats(t, s, date, 4, "tie")
	s2 := newTestStorage(t, s.dir)
	expectStats(t, s2, date, 4, "tie")
	if len(s2.dayCache) != 0 {
		t.Fatal("stats reads must not retain complete day data in memory")
	}
	s2.Ingest([]RawNotification{notifAt("after-restart", day.Add(2*time.Hour), "after-restart")}, "phone")
	if readPersistedStats(t, s2, date).Count != 5 {
		t.Fatal("incremental count was lost on restart")
	}
}

func TestDailyStatsHostTimeZoneAndUTCTimestamps(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("test-shanghai", 8*60*60)
	t.Cleanup(func() { time.Local = previous })
	s := newStorage(t)
	s.Ingest([]RawNotification{
		{ID: "before", App: "x", Body: "before", Timestamp: "2026-10-29T15:59:59Z"},
		{ID: "after", App: "x", Body: "after", Timestamp: "2026-10-29T16:00:00Z"},
	}, "phone")
	expectStats(t, s, "2026-10-29", 1, "before")
	stats := expectStats(t, s, "2026-10-30", 1, "after")
	if stats.Latest.Timestamp != "2026-10-29T16:00:00Z" {
		t.Fatal("UTC source timestamp was rewritten")
	}
}

func TestDailyStatsRebuildOnlyRequestedDayAndSkipBadRows(t *testing.T) {
	s := newStorage(t)
	date := "2026-10-30"
	raw := []byte("\n{broken}\nnull\n[]\n{}\n" +
		`{"appName":"x","timestamp":"bad"}` + "\n" +
		`{"appName":"x","timestamp":"2026-10-30T03:00:00Z","content":42}` + "\n" +
		`{"appName":"x","timestamp":"2026-10-30T02:00:00Z","content":"older"}` + "\n" +
		`{"appName":"x","timestamp":"2026-10-30T04:00:00Z","content":"unterminated"}`)
	if err := os.WriteFile(s.dayPath(date), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// An unrelated corrupt day must not affect this read or get an index.
	if err := os.WriteFile(s.dayPath("2026-10-29"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectStats(t, s, date, 3, "unterminated")
	if _, err := os.Stat(s.dailyStatsPath("2026-10-29")); !os.IsNotExist(err) {
		t.Fatal("unrelated date was indexed")
	}
	after, _ := os.ReadFile(s.dayPath(date))
	if !bytes.Equal(raw, after) || len(s.dayCache) != 0 {
		t.Fatal("read changed source or populated the full day cache")
	}
}

func TestDailyStatsLegacyMigrationAndJSONLPreference(t *testing.T) {
	s := newStorage(t)
	day := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	date := day.Format(time.DateOnly)
	writeDateFile(t, s.dir, date, []StoredNotification{
		{AppName: "x", Content: "legacy", Timestamp: day.Format(time.RFC3339)},
	})
	expectStats(t, s, date, 1, "legacy")
	if _, err := os.Stat(s.dayPath(date)); !os.IsNotExist(err) {
		t.Fatal("query unexpectedly migrated source")
	}
	s.Ingest([]RawNotification{notifAt("new", day.Add(time.Hour), "new")}, "phone")
	idx := readPersistedStats(t, s, date)
	if idx.Count != 2 || idx.Source.Name != date+".jsonl" {
		t.Fatalf("migration index mismatch: %+v", idx)
	}
	// A stale legacy file must not be counted alongside JSONL.
	writeDateFile(t, s.dir, date, []StoredNotification{{AppName: "x", Content: "stale", Timestamp: day.Format(time.RFC3339)}})
	expectStats(t, s, date, 2, "new")
}

func TestDailyStatsRebuildStaleAndCorruptIndexes(t *testing.T) {
	for _, change := range []string{"missing", "invalid-json", "wrong-version", "negative", "wrong-date", "wrong-zone", "append", "replace", "truncate", "source-deleted"} {
		t.Run(change, func(t *testing.T) {
			s := newStorage(t)
			day := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
			date := day.Format(time.DateOnly)
			s.Ingest([]RawNotification{notifAt("one", day, "one")}, "phone")
			idx := readPersistedStats(t, s, date)
			count, latest := int64(1), "one"
			var err error
			switch change {
			case "missing":
				err = os.Remove(s.dailyStatsPath(date))
			case "invalid-json":
				err = os.WriteFile(s.dailyStatsPath(date), []byte("{"), 0o600)
			case "wrong-version", "negative", "wrong-date", "wrong-zone":
				switch change {
				case "wrong-version":
					idx.SchemaVersion++
				case "negative":
					idx.Count = -1
				case "wrong-date":
					idx.Date = "2026-10-29"
				case "wrong-zone":
					idx.TimeZone = "wrong-zone"
				}
				err = fsutil.WriteJSON(s.dailyStatsPath(date), idx, 0o600)
			case "append":
				lines, _ := encodeJSONLines([]StoredNotification{{AppName: "x", Content: "two", Timestamp: day.Add(time.Hour).Format(time.RFC3339)}})
				err = appendDayLines(s.dayPath(date), lines)
				count, latest = 2, "two"
			case "replace":
				lines, _ := encodeJSONLines([]StoredNotification{{AppName: "x", Content: "replacement", Timestamp: day.Format(time.RFC3339)}})
				err = fsutil.WriteAtomic(s.dayPath(date), lines, 0o600)
				latest = "replacement"
			case "truncate":
				err = os.WriteFile(s.dayPath(date), nil, 0o600)
				count, latest = 0, ""
			case "source-deleted":
				err = os.Remove(s.dayPath(date))
				count, latest = 0, ""
			}
			if err != nil {
				t.Fatal(err)
			}
			expectStats(t, s, date, count, latest)
			if change == "source-deleted" {
				if _, err := os.Stat(s.dailyStatsPath(date)); !os.IsNotExist(err) {
					t.Fatal("deleted source retained stale stats")
				}
			} else if idx = readPersistedStats(t, s, date); idx.Count != count || idx.Date != date || idx.SchemaVersion != 1 {
				t.Fatalf("index not repaired: %+v", idx)
			}
		})
	}
}

func TestDailyStatsImportsShareIndexAndRollback(t *testing.T) {
	s := newStorage(t)
	day := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	date := day.Format(time.DateOnly)
	entry := StoredNotification{AppName: "x", Content: "imported", Timestamp: day.Format(time.RFC3339),
		Transfer: &TransferMark{Origin: "origin", RecordID: "id", MemoryPolicy: MemoryPolicySkipHistory}}
	importOne := func() error {
		return s.ImportDay(date, func(_ []StoredNotification, admit func(StoredNotification) bool) error {
			admit(entry)
			admit(entry)
			return nil
		})
	}
	if err := importOne(); err != nil {
		t.Fatal(err)
	}
	if readPersistedStats(t, s, date).Count != 1 {
		t.Fatal("import did not maintain index")
	}
	if err := importOne(); err != nil {
		t.Fatal(err)
	}
	stats := expectStats(t, s, date, 1, "imported")
	if stats.Latest.Transfer == nil || stats.Latest.Transfer.RecordID != "id" {
		t.Fatal("import provenance lost")
	}
	err := s.ImportDay(date, func(_ []StoredNotification, admit func(StoredNotification) bool) error {
		entry.Content = "rollback"
		admit(entry)
		return errors.New("abort")
	})
	if err == nil {
		t.Fatal("expected import failure")
	}
	expectStats(t, s, date, 1, "imported")
}

func TestDailyStatsWriteFailuresDoNotFailIngest(t *testing.T) {
	s := newStorage(t)
	day := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	date := day.Format(time.DateOnly)
	blocker := filepath.Join(s.dir, dailyStatsDirName)
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := s.Ingest([]RawNotification{notifAt("one", day, "one")}, "phone")
	if result.Ingested != 1 || result.Failed != 0 {
		t.Fatalf("cache failure affected ingest: %+v", result)
	}
	expectStats(t, s, date, 1, "one")
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	expectStats(t, s, date, 1, "one")
	readPersistedStats(t, s, date)
}

func TestDailyStatsFailedSourceWriteInvalidatesIndex(t *testing.T) {
	s := newStorage(t)
	day := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	date := day.Format(time.DateOnly)
	s.Ingest([]RawNotification{notifAt("one", day, "one")}, "phone")
	if err := os.Remove(s.dayPath(date)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.dayPath(date), 0o700); err != nil {
		t.Fatal(err)
	}
	result := s.Ingest([]RawNotification{notifAt("two", day.Add(time.Hour), "two")}, "phone")
	if result.Ingested != 0 || result.Failed != 1 {
		t.Fatalf("expected failed source write: %+v", result)
	}
	if _, err := os.Stat(s.dailyStatsPath(date)); !os.IsNotExist(err) {
		t.Fatal("failed source write left index")
	}
	if _, err := s.ReadDailyStats(date); err == nil {
		t.Fatal("source error was presented as zero")
	}
}

func TestDailyStatsRejectsInvalidDatesAndBrokenLegacy(t *testing.T) {
	s := newStorage(t)
	for _, date := range []string{"", "../outside", "2026-02-30", "2026-1-01", "2026-10-30.jsonl"} {
		if _, err := s.ReadDailyStats(date); err == nil {
			t.Fatalf("accepted invalid date %q", date)
		}
	}
	for _, raw := range []string{"{broken", "null", "{}", "[{}", "[] {}", "[] garbage"} {
		path := legacyDayFilePath(s.dir, "2026-10-30")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadDailyStats("2026-10-30"); err == nil {
			t.Fatalf("accepted broken legacy %q", raw)
		}
		got, _ := os.ReadFile(path)
		if string(got) != raw {
			t.Fatal("stats read mutated broken legacy source")
		}
	}
}

func TestDailyStatsPrunesAndQuarantines(t *testing.T) {
	s := newStorage(t)
	old := time.Now().AddDate(0, 0, -10)
	date := old.Format(time.DateOnly)
	s.Ingest([]RawNotification{notifAt("old", old, "old")}, "phone")
	readPersistedStats(t, s, date)
	retention := 1
	s.cfg.RetentionDays = &retention
	s.Ingest(nil, "phone")
	if _, err := os.Stat(s.dailyStatsPath(date)); !os.IsNotExist(err) {
		t.Fatal("retention left stats for removed day")
	}
	expectStats(t, s, date, 0, "")
	s.cfg.RetentionDays = nil
	s.Ingest([]RawNotification{notifAt("new", old, "old")}, "phone")
	readPersistedStats(t, s, date)
	if err := os.Remove(s.dayPath(date)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyDayFilePath(s.dir, date), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := newTestStorage(t, s.dir)
	s2.Ingest([]RawNotification{notifAt("new", old, "recovered")}, "phone")
	expectStats(t, s2, date, 1, "recovered")
}

func TestDailyStatsRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require privileges on Windows")
	}
	s := newStorage(t)
	date := "2026-10-30"
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(s.dir, dailyStatsDirName)); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	s.Ingest([]RawNotification{notifAt("one", day, "one")}, "phone")
	expectStats(t, s, date, 1, "one")
	files, _ := os.ReadDir(outside)
	if len(files) != 0 {
		t.Fatal("stats escaped through symlink")
	}
	if err := os.Remove(s.dayPath(date)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "missing"), s.dayPath(date)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadDailyStats(date); err == nil {
		t.Fatal("symlink source was treated as empty")
	}
}

func TestDailyStatsLongRowsAndTornTail(t *testing.T) {
	s := newStorage(t)
	date := "2026-10-30"
	content := strings.Repeat("长", 25000)
	lines, _ := encodeJSONLines([]StoredNotification{{AppName: "x", Content: content, Timestamp: "2026-10-30T03:00:00Z"}})
	lines = append(lines, []byte("\n{\"appName\":\"x\",\"timestamp\":")...)
	if err := os.WriteFile(s.dayPath(date), lines, 0o600); err != nil {
		t.Fatal(err)
	}
	expectStats(t, s, date, 1, content)
}

func TestDailyStatsConcurrentReadersAndWriters(t *testing.T) {
	s := newStorage(t)
	day := time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)
	date := day.Format(time.DateOnly)
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Go(func() {
			result := s.Ingest([]RawNotification{notifAt(fmt.Sprint(i), day.Add(time.Duration(i)*time.Second), fmt.Sprint(i))}, "phone")
			if result.Ingested != 1 {
				t.Errorf("write failed: %+v", result)
			}
		})
		wg.Go(func() {
			stats, err := s.ReadDailyStats(date)
			if err != nil || stats.Count < 0 || stats.Count > 30 || (stats.Count == 0) != (stats.Latest == nil) {
				t.Errorf("inconsistent concurrent read: %+v, %v", stats, err)
			}
		})
	}
	wg.Wait()
	expectStats(t, s, date, 30, "29")
	if info, err := os.Stat(s.dailyStatsPath(date)); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("stats file permissions: %v %v", info, err)
	}
}

func TestDailyStatsContextCancellationAndColdReadLimit(t *testing.T) {
	s := newStorage(t)
	const date = "2026-10-30"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ReadDailyStatsContext(ctx, date); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled stats read: %v", err)
	}
	if err := os.WriteFile(s.dayPath(date), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(s.dayPath(date), DailyStatsReadLimit+1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadDailyStatsContext(context.Background(), date); !errors.Is(err, ErrDailyStatsTooLarge) {
		t.Fatalf("oversized cold read: %v", err)
	}
	if _, err := os.Stat(s.dailyStatsPath(date)); !os.IsNotExist(err) {
		t.Fatal("failed rebuild wrote a partial count")
	}
}
