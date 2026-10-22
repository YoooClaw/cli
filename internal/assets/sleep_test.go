package assets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/healthdata"
)

func seedSleep(t *testing.T, s *Service, count, sourceLength int) (string, string, string) {
	t.Helper()
	date := "2026-10-21"
	id := strings.Repeat("a", 64)
	source := strings.Repeat("s", sourceLength)
	start, _ := time.Parse(time.RFC3339, "2026-10-21T00:00:00+08:00")
	end := start.Add(time.Duration(count) * time.Second)
	segments := make([]healthdata.SleepSegment, count)
	for i := range segments {
		segments[i] = healthdata.SleepSegment{Stage: "core", StartAt: start.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano), EndAt: start.Add(time.Duration(i+1) * time.Second).Format(time.RFC3339Nano), Source: &source}
	}
	minutes := int(end.Round(time.Minute).Sub(start.Round(time.Minute)) / time.Minute)
	session := healthdata.SleepSession{SleepSummary: healthdata.SleepSummary{ID: id, StartAt: start.Format(time.RFC3339), EndAt: end.Format(time.RFC3339), SleepMinutes: minutes, StageMinutes: map[string]int{"core": minutes, "deep": 0, "rem": 0, "unspecified": 0}, Sources: []healthdata.SleepSource{{ID: source, Name: "Test Watch"}}}, Segments: segments}
	raw := marshal(t, healthdata.DailyBatch{SchemaVersion: 3, Provider: "healthkit", TimeZone: "Asia/Shanghai", CollectedAt: "2026-10-22T00:00:00+08:00", Days: []healthdata.DailySnapshot{{Date: date, Items: []json.RawMessage{}, SleepSessions: []healthdata.SleepSession{session}}}})
	batch, err := healthdata.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	store := healthdata.NewStorage(s.sources["health-data"].Dir)
	if err = store.Append(batch); err != nil {
		t.Fatal(err)
	}
	page := get(t, s, "health-data", url.Values{})
	if len(page.Items) != 1 {
		t.Fatal("sleep-only day missing")
	}
	data := marshal(t, page)
	if strings.Contains(string(data), `"segments"`) {
		t.Fatal("list leaked full timeline")
	}
	return date, id, page.Items[0].Data["revision"].(string)
}
func TestSleepDefaultMaximumByteCapAndCompleteContinuation(t *testing.T) {
	s := fixture(t)
	date, id, rev := seedSleep(t, s, 1200, 512)
	p, e := s.ReadSleep(context.Background(), date, id, url.Values{"revision": {rev}})
	if e != nil || len(p.Items) != 100 || p.NextCursor == nil {
		t.Fatal("default", e, len(p.Items))
	}
	seen := map[string]bool{}
	count := 0
	values := url.Values{"revision": {rev}, "limit": {"500"}}
	for pages := 0; pages < 20; pages++ {
		p, e = s.ReadSleep(context.Background(), date, id, values)
		if e != nil {
			t.Fatal(e)
		}
		if len(marshal(t, p)) > ResponseLimit || len(p.Items) > 500 || len(p.Items) == 0 {
			t.Fatal("unbounded or empty page")
		}
		if pages == 0 && len(p.Items) >= 500 {
			t.Fatal("byte budget did not shorten page")
		}
		for _, segment := range p.Items {
			if seen[segment.StartAt] {
				t.Fatal("duplicate segment")
			}
			seen[segment.StartAt] = true
			count++
		}
		if p.NextCursor == nil {
			break
		}
		values = url.Values{"cursor": {*p.NextCursor}, "limit": {"500"}}
	}
	if count != 1200 {
		t.Fatalf("lost segments: %d", count)
	}
}
func TestSleepCursorIsBoundToProfileDaySessionAndRevision(t *testing.T) {
	s := fixture(t)
	date, id, rev := seedSleep(t, s, 150, 5)
	p, e := s.ReadSleep(context.Background(), date, id, url.Values{"revision": {rev}})
	if e != nil {
		t.Fatal(e)
	}
	cursor := *p.NextCursor
	for _, tc := range []struct {
		name, date, id string
		values         url.Values
		code           string
	}{
		{"changed day", "2026-10-20", id, url.Values{"cursor": {cursor}}, "CURSOR_QUERY_MISMATCH"},
		{"changed session", date, strings.Repeat("b", 64), url.Values{"cursor": {cursor}}, "CURSOR_QUERY_MISMATCH"},
		{"changed revision", date, id, url.Values{"cursor": {cursor}, "revision": {strings.Repeat("b", 64)}}, "CURSOR_QUERY_MISMATCH"},
		{"tampered", date, id, url.Values{"cursor": {cursor + "x"}}, "CURSOR_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, e := s.ReadSleep(context.Background(), tc.date, tc.id, tc.values)
			if e == nil || e.Code != tc.code {
				t.Fatal(e)
			}
		})
	}
	oldProfile := s.profile
	s.profile = "another-profile"
	_, e = s.ReadSleep(context.Background(), date, id, url.Values{"cursor": {cursor}})
	if e == nil || e.Status != 403 {
		t.Fatal("profile binding", e)
	}
	s.profile = oldProfile
	path := filepath.Join(s.sources["health-data"].Dir, date+".jsonl")
	raw, _ := os.ReadFile(path)
	replace(t, path, append(raw, '\n'))
	_, e = s.ReadSleep(context.Background(), date, id, url.Values{"cursor": {cursor}})
	if e == nil || e.Status != 410 {
		t.Fatal("snapshot changed", e)
	}
	_, e = s.ReadSleep(context.Background(), date, id, url.Values{"revision": {rev}})
	if e == nil || e.Status != 410 {
		t.Fatal("stale list", e)
	}
}
func TestSleepRejectsInvalidLimitsAndMissingRevision(t *testing.T) {
	s := fixture(t)
	date, id, rev := seedSleep(t, s, 3, 5)
	for _, limit := range []string{"0", "501", "-1", "1.2", ""} {
		_, e := s.ReadSleep(context.Background(), date, id, url.Values{"revision": {rev}, "limit": {limit}})
		if e == nil || e.Status != 400 {
			t.Fatal(limit, e)
		}
	}
	for _, values := range []url.Values{{}, {"revision": {rev}, "limit": {"1", "2"}}, {"revision": {rev}, "extra": {"x"}}} {
		if _, e := s.ReadSleep(context.Background(), date, id, values); e == nil || e.Status != 400 {
			t.Fatal(e)
		}
	}
	if _, e := s.ReadSleep(context.Background(), date, strings.Repeat("b", 64), url.Values{"revision": {rev}}); e == nil || e.Status != 404 {
		t.Fatal(e)
	}
}
func TestSleepPathOnlyAcceptsDateAndHexID(t *testing.T) {
	for _, path := range []string{"/assets/health-data/../sleep-sessions/" + strings.Repeat("a", 64), "/assets/health-data/2026-02-30/sleep-sessions/" + strings.Repeat("a", 64), "/assets/health-data/2026-10-21/sleep-sessions/../../secret"} {
		if _, _, ok := SleepRoute(path); ok {
			t.Fatal(path)
		}
	}
	path := fmt.Sprintf("/assets/health-data/%s/sleep-sessions/%s", "2026-10-21", strings.Repeat("a", 64))
	if _, _, ok := SleepRoute(path); !ok {
		t.Fatal(path)
	}
}
func TestSleepCursorExpiryRestartAndSymlinkProtection(t *testing.T) {
	s := fixture(t)
	date, id, rev := seedSleep(t, s, 150, 5)
	p, e := s.ReadSleep(context.Background(), date, id, url.Values{"revision": {rev}})
	if e != nil {
		t.Fatal(e)
	}
	values := url.Values{"cursor": {*p.NextCursor}}
	now := s.now()
	s.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, e = s.ReadSleep(context.Background(), date, id, values); e == nil || e.Status != 410 {
		t.Fatal("expiry", e)
	}
	restarted := New(s.profile, s.sources)
	if _, e = restarted.ReadSleep(context.Background(), date, id, values); e == nil || e.Status != 410 {
		t.Fatal("restart", e)
	}
	path := filepath.Join(s.sources["health-data"].Dir, date+".jsonl")
	outside := filepath.Join(t.TempDir(), "private.jsonl")
	raw, _ := os.ReadFile(path)
	if err := os.WriteFile(outside, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skip(err)
	}
	if _, e = restarted.ReadSleep(context.Background(), date, id, url.Values{"revision": {rev}}); e == nil || e.Status != 500 {
		t.Fatal("symlink followed", e)
	}
}
