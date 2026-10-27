package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/assets"
	"github.com/YoooClaw/cli/internal/healthdata"
)

func TestSleepUploadListAndDetailHTTPBridge(t *testing.T) {
	source := "watch"
	session := healthdata.SleepSession{SleepSummary: healthdata.SleepSummary{
		ID: strings.Repeat("a", 64), StartAt: "2026-09-30T23:00:00+08:00", EndAt: "2026-10-01T01:00:00+08:00",
		SleepMinutes: 110, StageMinutes: map[string]int{"core": 60, "rem": 50, "deep": 0, "unspecified": 0},
		AwakeMinutes: 10, AwakeningCount: 1, LongestAwakeMinutes: 10, Sources: []healthdata.SleepSource{{ID: source, Name: "Test Watch"}},
	}, Segments: []healthdata.SleepSegment{
		{Stage: "core", StartAt: "2026-09-30T23:00:00+08:00", EndAt: "2026-10-01T00:00:00+08:00", Source: &source},
		{Stage: "awake", StartAt: "2026-10-01T00:00:00+08:00", EndAt: "2026-10-01T00:10:00+08:00", Source: &source},
		{Stage: "rem", StartAt: "2026-10-01T00:10:00+08:00", EndAt: "2026-10-01T01:00:00+08:00", Source: &source},
	}}
	batch := healthdata.DailyBatch{SchemaVersion: 3, Provider: "healthkit", TimeZone: "Asia/Shanghai", CollectedAt: "2026-10-01T12:00:00+08:00",
		Days: []healthdata.DailySnapshot{{Date: "2026-10-01", Items: []json.RawMessage{}, SleepSessions: []healthdata.SleepSession{session}}}}
	raw, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	checkSleepHTTPFlow(t, raw)
}

// Uses real localhost HTTP, with the same POST read envelope as messageBridge.
func checkSleepHTTPFlow(t *testing.T, raw []byte) {
	t.Helper()
	_, local := newTestServer(t, "sleep-token")
	request := func(method, path, body, token string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, local.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := local.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	var batch healthdata.DailyBatch
	if err := json.Unmarshal(raw, &batch); err != nil {
		t.Fatal(err)
	}
	prepared, err := healthdata.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	status, receipt := request("POST", "/health-data", string(raw), "sleep-token")
	assertHealthReceipt(t, status, receipt, prepared.ItemCount, 0)
	status, list := request("POST", "/assets/health-data?limit=14", `{"method":"GET","body":null}`, "sleep-token")
	if status != 200 || strings.Contains(string(list), `"segments"`) {
		t.Fatal("list must contain summaries only", status)
	}
	var page struct {
		Items []struct {
			Data struct {
				Date          string                        `json:"date"`
				Revision      string                        `json:"revision"`
				SleepSessions []healthdata.SleepListSummary `json:"sleepSessions"`
			} `json:"data"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != len(batch.Days) {
		t.Fatal("days missing")
	}
	for _, day := range page.Items {
		for _, session := range day.Data.SleepSessions {
			path := "/assets/health-data/" + day.Data.Date + "/sleep-sessions/" + session.ID
			q := url.Values{"revision": {day.Data.Revision}, "limit": {"2"}}
			seen := []healthdata.SleepSegment{}
			for attempts := 0; ; attempts++ {
				if attempts > 4096 {
					t.Fatal("cursor did not terminate")
				}
				status, data := request("POST", path+"?"+q.Encode(), `{"method":"GET","body":null}`, "sleep-token")
				if status != 200 {
					t.Fatalf("detail failed: %d %s", status, data)
				}
				var detail assets.SleepResponse
				if err := json.Unmarshal(data, &detail); err != nil {
					t.Fatal(err)
				}
				if len(detail.Items) < 1 || len(detail.Items) > 2 || len(data) > assets.ResponseLimit {
					t.Fatal("page bounds")
				}
				seen = append(seen, detail.Items...)
				if detail.NextCursor == nil {
					break
				}
				q.Set("cursor", *detail.NextCursor)
			}
			if len(seen) != session.SegmentCount {
				t.Fatal("incomplete timeline")
			}
			for _, uploaded := range batch.Days {
				for _, s := range uploaded.SleepSessions {
					if s.ID == session.ID {
						a, _ := json.Marshal(s.Segments)
						b, _ := json.Marshal(seen)
						if string(a) != string(b) {
							t.Fatal("timeline changed on round trip")
						}
					}
				}
			}
			if status, _ := request("GET", path+"?revision="+day.Data.Revision, "", "wrong"); status != 401 {
				t.Fatal("detail auth bypass")
			}
			if status, _ := request("GET", path+"?revision="+day.Data.Revision+"&limit=501", "", "sleep-token"); status != 400 {
				t.Fatal("detail limit bypass")
			}
			if status, _ := request("POST", path+"?revision="+day.Data.Revision, `{"method":"DELETE"}`, "sleep-token"); status != 400 {
				t.Fatal("write envelope allowed")
			}
		}
	}
}
