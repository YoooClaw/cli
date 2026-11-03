package assets

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/reminders"
)

const SummaryLatestLimit = 32 << 10

var summaryKinds = []string{"notifications", "locations", "calendars", "reminders", "web-pages", "recordings", "health-data"}

type SummaryResult struct {
	Status   string    `json:"status"`
	Count    *int64    `json:"count,omitempty"`
	Latest   *Item     `json:"latest"`
	LatestBy string    `json:"latestBy"`
	Warnings []Warning `json:"warnings"`
	Error    *Error    `json:"error,omitempty"`
}

type SummaryResponse struct {
	OK          bool                     `json:"ok"`
	Partial     bool                     `json:"partial"`
	Date        string                   `json:"date"`
	TimeZone    string                   `json:"timeZone"`
	DayStart    string                   `json:"dayStart"`
	DayEnd      string                   `json:"dayEnd"`
	GeneratedAt string                   `json:"generatedAt"`
	Assets      map[string]SummaryResult `json:"assets"`
}

// SummaryStores injects existing stores rather than introducing new writers or
// HTTP clients. Recordings retain their legacy credential/source visibility.
type SummaryStores struct {
	Notifications  *notif.Storage
	Reminders      *reminders.Storage
	Recordings     *recording.Storage
	RecordingScope string
}

type summaryQuery struct {
	date  string
	zone  *time.Location
	kinds []string
}

func (s *Service) summaryQuery(values url.Values) (summaryQuery, *Error) {
	q := summaryQuery{date: s.now().In(time.Local).Format(time.DateOnly), zone: time.Local, kinds: summaryKinds}
	for name, values := range values {
		if (name != "date" && name != "types") || len(values) != 1 || values[0] == "" {
			return q, invalid()
		}
	}
	if date := values.Get("date"); date != "" {
		if !validDate(date) {
			return q, invalid()
		}
		q.date = date
	}
	if selected := values.Get("types"); selected != "" {
		seen := map[string]bool{}
		q.kinds = strings.Split(selected, ",")
		for _, kind := range q.kinds {
			found := false
			for _, allowed := range summaryKinds {
				found = found || kind == allowed
			}
			if !found || seen[kind] {
				return q, invalid()
			}
			seen[kind] = true
		}
	}
	return q, nil
}

func emptySummary(kind string) SummaryResult {
	count := int64(0)
	by := map[string]string{"notifications": "timestamp", "locations": "timestamp", "calendars": "startAt", "reminders": "due", "web-pages": "capturedAt", "recordings": "created_at", "health-data": "date"}
	r := SummaryResult{Status: "ok", Count: &count, LatestBy: by[kind], Warnings: []Warning{}}
	if kind == "health-data" {
		r.Count = nil
	}
	return r
}

// ReadSummary queries each category once, concurrently and under its own store
// boundary. Each result is a committed category snapshot, not a global snapshot.
// Errors never expose a partial count as a successful total.
func (s *Service) ReadSummary(ctx context.Context, values url.Values, stores SummaryStores) (SummaryResponse, *Error) {
	q, err := s.summaryQuery(values)
	if err != nil {
		return SummaryResponse{}, err
	}
	if ctx.Err() != nil {
		return SummaryResponse{}, readFailed()
	}
	start, _ := time.ParseInLocation(time.DateOnly, q.date, q.zone)
	out := SummaryResponse{OK: true, Date: q.date, TimeZone: q.zone.String(), DayStart: start.Format(time.RFC3339), DayEnd: start.AddDate(0, 0, 1).Format(time.RFC3339), Assets: map[string]SummaryResult{}}
	results := make([]SummaryResult, len(q.kinds))
	var wg sync.WaitGroup
	for i, kind := range q.kinds {
		wg.Go(func() {
			r := emptySummary(kind)
			var e *Error
			switch kind {
			case "notifications":
				e = s.summaryNotifications(ctx, q, stores.Notifications, &r)
			case "locations", "calendars":
				e = s.summaryDay(ctx, q, kind, &r)
			case "reminders":
				e = s.summaryReminders(ctx, q, stores.Reminders, &r)
			case "web-pages":
				e = s.summaryWeb(ctx, q, &r)
			case "recordings":
				e = s.summaryRecordings(ctx, q, stores.Recordings, stores.RecordingScope, &r)
			case "health-data":
				e = s.summaryHealth(ctx, &r)
			}
			if e != nil {
				r.Status, r.Error, r.Count, r.Latest = "error", e, nil, nil
			} else if r.Latest != nil {
				boundItem(r.Latest, map[string]int{"title": 512, "name": 512, "content": 8192, "description": 8192}, SummaryLatestLimit)
			}
			results[i] = r
		})
	}
	wg.Wait()
	for i, kind := range q.kinds {
		out.Assets[kind] = results[i]
		out.Partial = out.Partial || results[i].Error != nil
	}
	out.GeneratedAt = s.now().In(q.zone).Format(time.RFC3339Nano)
	raw, marshalErr := json.Marshal(out)
	if marshalErr != nil || len(raw) > ResponseLimit {
		return SummaryResponse{}, readFailed()
	}
	return out, nil
}

func summaryLatest(r *SummaryResult, it Item, at, current time.Time) time.Time {
	if r.Latest == nil || !at.Before(current) {
		r.Latest = &it
		return at
	}
	return current
}
