package assets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/reminders"
	"github.com/YoooClaw/cli/internal/testutil"
)

func summaryFixture(t *testing.T) (*Service, SummaryStores) {
	t.Helper()
	s := fixture(t)
	n := notif.NewStorage(s.sources["notifications"].Dir, notif.PluginConfig{}, testutil.Logger{T: t})
	if err := n.Init(); err != nil {
		t.Fatal(err)
	}
	s.sources["notifications"] = Source{Dir: s.sources["notifications"].Dir, WithReadView: n.WithReadView}
	root := filepath.Dir(s.sources["notifications"].Dir)
	s.sources["recordings"] = Source{Dir: filepath.Join(root, "recordings")}
	r := recording.NewStorage(s.sources["recordings"].Dir, summaryTestLogger{t})
	if err := r.Init(); err != nil {
		t.Fatal(err)
	}
	return s, SummaryStores{Notifications: n, Reminders: reminders.NewStorage(filepath.Join(root, "reminders")), Recordings: r}
}

type summaryTestLogger struct{ t *testing.T }

func (l summaryTestLogger) Info(string)    {}
func (l summaryTestLogger) Warn(m string)  { l.t.Log(m) }
func (l summaryTestLogger) Error(m string) { l.t.Log(m) }

func getSummary(t *testing.T, s *Service, stores SummaryStores, q url.Values) SummaryResponse {
	t.Helper()
	out, err := s.ReadSummary(context.Background(), q, stores)
	if err != nil {
		t.Fatal(err)
	}
	raw := marshal(t, out)
	if !out.OK || out.Assets == nil || len(raw) > ResponseLimit {
		t.Fatalf("invalid summary envelope: %s", raw)
	}
	for kind, result := range out.Assets {
		if result.Warnings == nil || kind == "health-data" && result.Count != nil {
			t.Fatalf("invalid category %s: %+v", kind, result)
		}
		if result.Error != nil && (result.Count != nil || result.Latest != nil || result.Status != "error") {
			t.Fatalf("error exposed partial totals: %+v", result)
		}
		if result.Latest != nil && len(marshal(t, result.Latest)) > SummaryLatestLimit {
			t.Fatal("latest item exceeds summary cap")
		}
	}
	return out
}

func TestSummaryAllCategoriesDailyCountsAndLatest(t *testing.T) {
	s, stores := summaryFixture(t)
	day := time.Date(2026, 10, 22, 9, 0, 0, 0, time.Local)
	date := day.Format(time.DateOnly)
	notifications := make([]notif.RawNotification, 200)
	for i := range notifications {
		notifications[i] = notif.RawNotification{ID: fmt.Sprint(i), App: "app", Body: fmt.Sprint(i), Timestamp: day.Add(time.Duration(i) * time.Second).Format(time.RFC3339)}
	}
	stores.Notifications.Ingest(notifications, "phone-a")
	stores.Notifications.Ingest(notifications, "phone-a")
	stores.Notifications.Ingest([]notif.RawNotification{{ID: "previous", App: "app", Body: "previous", Timestamp: day.AddDate(0, 0, -1).Format(time.RFC3339)}}, "other-phone")
	put(t, s, "locations", date+".jsonl", []byte(`{"longitude":0,"latitude":0,"timestamp":"2026-10-22T11:00:00Z","address":"latest"}`+"\n"+
		`{"longitude":0,"latitude":0,"timestamp":"2026-10-22T09:00:00Z","address":"late-upload"}`+"\n"+
		`{"longitude":0,"latitude":0}`+"\n"+`{"longitude":999,"latitude":0}`+"\n"))
	put(t, s, "calendars", date+".jsonl", []byte(`{"id":"last","title":"last","startAt":"2026-10-22T19:00:00Z","endAt":"2026-10-22T20:00:00Z","timeZone":"UTC"}`+"\n"+
		`{"id":"first","title":"first","startAt":"2026-10-22T09:00:00Z","endAt":"2026-10-22T10:00:00Z","timeZone":"UTC"}`+"\n"))
	put(t, s, "calendars", "2026-10-23.jsonl", []byte(`{"id":"future","startAt":"2026-10-23T09:00:00Z","endAt":"2026-10-23T10:00:00Z","timeZone":"UTC"}`+"\n"))
	put(t, s, "health-data", "2026-10-23.jsonl", daily(t, "2026-10-23", "Steps", json.Number("9007199254740993")))
	put(t, s, "health-data", date+".jsonl", daily(t, date, "Steps", json.Number("1")))
	pages := []map[string]any{}
	for i, at := range []time.Time{day, day.Add(time.Hour), day.AddDate(0, 0, -1)} {
		pages = append(pages, map[string]any{"urlHash": fmt.Sprint(i), "url": "https://example.com/" + fmt.Sprint(i), "title": fmt.Sprint(i), "capturedAt": at.UTC().Format(time.RFC3339)})
	}
	put(t, s, "web-pages", "index.json", marshal(t, map[string]any{"pages": pages}))
	for i, at := range []time.Time{day, day.Add(time.Hour), day.AddDate(0, 0, -1)} {
		if _, err := stores.Recordings.Ingest(fmt.Sprint(i), recording.Metadata{Name: fmt.Sprint(i), CreatedAt: at.UTC().Format(time.RFC3339)}, "phone-a"); err != nil {
			t.Fatal(err)
		}
	}
	early, late := "09:00:00", "18:00:00"
	_, err := stores.Reminders.Replace(reminders.Payload{SchemaVersion: 1, SourceID: "source", CollectedAt: day.Format(time.RFC3339), Items: []reminders.Item{
		{Summary: reminders.Summary{ID: "first", Title: "first", ListID: "list", Due: &reminders.Due{Date: date, Time: &early}}},
		{Summary: reminders.Summary{ID: "last", Title: "last", ListID: "list", IsCompleted: true, Due: &reminders.Due{Date: date, Time: &late}}},
		{Summary: reminders.Summary{ID: "undated", Title: "undated", ListID: "list"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	out := getSummary(t, s, stores, url.Values{"date": {date}})
	if out.Partial || len(out.Assets) != 7 {
		t.Fatalf("all categories: %+v", out)
	}
	for kind, count := range map[string]int64{"notifications": 200, "locations": 3, "calendars": 2, "reminders": 2, "web-pages": 2, "recordings": 2} {
		r := out.Assets[kind]
		if r.Count == nil || *r.Count != count || r.Latest == nil || r.Status != "ok" {
			t.Fatalf("%s: %+v", kind, r)
		}
	}
	if str(out.Assets["notifications"].Latest.Data, "content") != "199" || str(out.Assets["locations"].Latest.Data, "address") != "latest" ||
		str(out.Assets["calendars"].Latest.Data, "title") != "last" || str(out.Assets["reminders"].Latest.Data, "title") != "last" ||
		str(out.Assets["web-pages"].Latest.Data, "title") != "1" || str(out.Assets["recordings"].Latest.Data, "name") != "1" {
		t.Fatal("latest followed storage order instead of category timestamps")
	}
	health := out.Assets["health-data"]
	if health.Latest == nil || *health.Latest.StorageDate != "2026-10-23" || bytes.Contains(marshal(t, health), []byte(`"count":`)) ||
		!bytes.Contains(marshal(t, health.Latest.Data), []byte("9007199254740993")) {
		t.Fatalf("health must return latest business date, preserving numbers, without count: %+v", health)
	}
}

func TestSummaryEmptyDefaultsAndInvalidQueries(t *testing.T) {
	s, stores := summaryFixture(t)
	out := getSummary(t, s, stores, url.Values{})
	if out.Partial || out.Date != s.now().In(time.Local).Format(time.DateOnly) {
		t.Fatalf("default summary: %+v", out)
	}
	for kind, r := range out.Assets {
		if r.Latest != nil || kind != "health-data" && (r.Count == nil || *r.Count != 0) {
			t.Fatalf("empty %s: %+v", kind, r)
		}
	}
	for _, query := range []url.Values{
		{"date": {""}}, {"date": {"2026-02-30"}}, {"date": {"../../file"}}, {"date": {"2026-10-22", "2026-10-23"}},
		{"types": {""}}, {"types": {"notifications,notifications"}}, {"types": {"images"}}, {"types": {"health-data,"}},
		{"types": {"notifications", "health-data"}}, {"timeZone": {"UTC"}}, {"limit": {"1"}}, {"profile": {"other"}},
	} {
		if _, err := s.ReadSummary(context.Background(), query, stores); err == nil || err.Code != "INVALID_PARAMS" {
			t.Fatalf("accepted query %+v: %v", query, err)
		}
	}
}

func TestSummaryPartialFailuresAndSelectedTypes(t *testing.T) {
	s, stores := summaryFixture(t)
	put(t, s, "locations", "2026-10-22.jsonl", nil)
	if err := os.Remove(filepath.Join(s.sources["locations"].Dir, "2026-10-22.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.sources["locations"].Dir, "2026-10-22.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	out := getSummary(t, s, stores, url.Values{"date": {"2026-10-22"}})
	if !out.Partial || out.Assets["locations"].Error == nil || out.Assets["notifications"].Error != nil {
		t.Fatalf("partial failure: %+v", out)
	}
	out = getSummary(t, s, stores, url.Values{"date": {"2026-10-22"}, "types": {"health-data,notifications"}})
	if out.Partial || len(out.Assets) != 2 {
		t.Fatal("unselected source was read")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ReadSummary(ctx, url.Values{}, stores); err == nil {
		t.Fatal("cancelled request returned success")
	}
}

func TestSummaryReadBudgetsAreErrorsNotPartialCounts(t *testing.T) {
	for _, kind := range []string{"notifications", "locations", "calendars", "web-pages", "recordings", "health-data"} {
		t.Run(kind, func(t *testing.T) {
			s, stores := summaryFixture(t)
			name, size := "2026-10-22.jsonl", int64(ScanLimit+1)
			if kind == "notifications" {
				size = notif.DailyStatsReadLimit + 1
			}
			if kind == "web-pages" || kind == "recordings" {
				name, size = "index.json", IndexLimit+1
			}
			path := put(t, s, kind, name, nil)
			if err := os.Truncate(path, size); err != nil {
				t.Fatal(err)
			}
			out := getSummary(t, s, stores, url.Values{"date": {"2026-10-22"}, "types": {kind}})
			if !out.Partial || out.Assets[kind].Error == nil || out.Assets[kind].Error.Code != "SOURCE_TOO_LARGE" {
				t.Fatalf("%s budget: %+v", kind, out)
			}
		})
	}
}

func TestSummaryHealthLegacySkipAndCorruption(t *testing.T) {
	s, stores := summaryFixture(t)
	put(t, s, "health-data", "2026-10-23.jsonl", []byte(`{"schemaVersion":2}`))
	put(t, s, "health-data", "2026-10-22.jsonl", daily(t, "2026-10-22", "Steps", json.Number("1")))
	q := url.Values{"types": {"health-data"}}
	out := getSummary(t, s, stores, q)
	r := out.Assets["health-data"]
	if out.Partial || *r.Latest.StorageDate != "2026-10-22" || len(r.Warnings) != 1 || r.Warnings[0].Code != "UNSUPPORTED_SCHEMA" {
		t.Fatalf("legacy health handling: %+v", out)
	}
	put(t, s, "health-data", "2026-10-23.jsonl", []byte(`{"schemaVersion":3,"date":"wrong"}`))
	if out = getSummary(t, s, stores, q); !out.Partial || out.Assets["health-data"].Latest != nil {
		t.Fatal("corrupt newest health snapshot was silently hidden")
	}
}

func TestSummaryLatestPayloadCapsPreserveCounts(t *testing.T) {
	s, stores := summaryFixture(t)
	day := time.Date(2026, 10, 22, 9, 0, 0, 0, time.Local)
	stores.Notifications.Ingest([]notif.RawNotification{{ID: "big", App: "app", Title: strings.Repeat("标题", 2000), Body: strings.Repeat("😀", 20000), Timestamp: day.Format(time.RFC3339)}}, "phone")
	put(t, s, "health-data", "2026-10-22.jsonl", daily(t, "2026-10-22", strings.Repeat("name", 20000), json.Number("1")))
	put(t, s, "web-pages", "index.json", marshal(t, map[string]any{"pages": []any{map[string]any{"urlHash": "hash", "url": "https://example.com/" + strings.Repeat("x", SummaryLatestLimit), "capturedAt": day.Format(time.RFC3339)}}}))
	out := getSummary(t, s, stores, url.Values{"date": {"2026-10-22"}})
	if out.Partial || *out.Assets["notifications"].Count != 1 || len(out.Assets["notifications"].Latest.TruncatedFields) == 0 ||
		out.Assets["health-data"].Latest.Status != "oversized" || out.Assets["web-pages"].Latest.Status != "oversized" || *out.Assets["web-pages"].Count != 1 {
		t.Fatalf("bounded latest results: %+v", out)
	}
}

func TestSummaryDayBoundariesUseHostZoneAndDST(t *testing.T) {
	previous := time.Local
	t.Cleanup(func() { time.Local = previous })
	for _, zone := range []string{"Asia/Shanghai", "America/New_York"} {
		t.Run(zone, func(t *testing.T) {
			var err error
			time.Local, err = time.LoadLocation(zone)
			if err != nil {
				t.Fatal(err)
			}
			s, stores := summaryFixture(t)
			date := "2026-03-08"
			out := getSummary(t, s, stores, url.Values{"date": {date}, "types": {"notifications"}})
			start, _ := time.Parse(time.RFC3339, out.DayStart)
			end, _ := time.Parse(time.RFC3339, out.DayEnd)
			want := 24 * time.Hour
			if zone == "America/New_York" {
				want = 23 * time.Hour
			}
			if end.Sub(start) != want || out.TimeZone != zone {
				t.Fatalf("incorrect local day boundaries: %+v", out)
			}
		})
	}
}
