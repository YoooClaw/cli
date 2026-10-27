package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/YoooClaw/cli/internal/assets"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/relay"
)

func assetFile(t *testing.T, dir, name string, raw []byte) {
	t.Helper()
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, name), raw, 0600); e != nil {
		t.Fatal(e)
	}
}
func assetHTTP(s *server, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}
func assetResponse(t *testing.T, w *httptest.ResponseRecorder) assets.Response {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	if w.Body.Len() > assets.ResponseLimit || w.Header().Get("Cache-Control") != "no-store" ||
		w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatal("response limit or headers")
	}
	var p assets.Response
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	if !p.OK || p.Items == nil || p.Warnings == nil {
		t.Fatal("invalid HTTP envelope")
	}
	return p
}
func seedAssetEndpoints(t *testing.T, s *server) {
	t.Helper()
	notification := []byte(`{"appName":"app","title":"hello","content":"body","timestamp":"2026-10-22T00:00:00Z","clientLabel":"phone-a"}` + "\n")
	assetFile(t, s.ctx.Paths.Notifications, "2026-10-22.jsonl", bytes.Repeat(notification, 125))
	assetFile(t, s.ctx.Paths.Notifications, "2026-10-21.jsonl", bytes.Repeat(notification, 8))
	assetFile(t, s.ctx.Paths.Locations, "2026-10-22.jsonl", bytes.Repeat([]byte(`{"longitude":0,"latitude":0}`+"\n"), 125))
	assetFile(t, s.ctx.Paths.Calendars, "2026-10-22.jsonl", bytes.Repeat([]byte(`{"id":"event","title":"Meeting","startAt":"2026-10-22T09:00:00+08:00","endAt":"2026-10-22T10:00:00+08:00","timeZone":"Asia/Shanghai"}`+"\n"), 125))
	for day := 1; day <= 16; day++ {
		date := fmt.Sprintf("2026-10-%02d", day)
		raw := fmt.Sprintf(`{"schemaVersion":3,"date":%q,"timeZone":"Asia/Shanghai","provider":"healthkit","updatedAt":"2026-10-22T00:00:00Z"}
{"metric":"steps","name":"Steps","unit":"count","value":10}
`, date)
		assetFile(t, s.ctx.Paths.HealthData, date+".jsonl", []byte(raw))
	}
	pages := make([]map[string]any, 60)
	for i := range pages {
		pages[i] = map[string]any{"urlHash": fmt.Sprintf("%064d", i), "canonicalUrl": "https://example.com", "title": "page", "captureCount": 1, "clientLabel": "phone-a", "capturedAt": "2026-10-22T00:00:00Z"}
	}
	raw, e := json.Marshal(map[string]any{"pages": pages})
	if e != nil {
		t.Fatal(e)
	}
	assetFile(t, s.ctx.Paths.WebPages, "index.json", raw)
}

func TestAssetsHTTPAllCategoriesLimitsAndContract(t *testing.T) {
	s, _ := newTestServer(t, "gateway-secret")
	seedAssetEndpoints(t, s)
	s.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "phone-a", Key: "phone-key"}}}
	for _, tt := range []struct {
		kind            string
		def, max, total int
	}{
		{"notifications", 50, 100, 133}, {"locations", 50, 100, 125}, {"calendars", 50, 100, 125},
		{"health-data", 7, 14, 16}, {"web-pages", 20, 50, 60},
	} {
		t.Run(tt.kind, func(t *testing.T) {
			path := "/assets/" + tt.kind
			query := url.Values{}
			if tt.kind == "calendars" {
				query.Set("anchorDate", "2026-10-22")
			}
			p := assetResponse(t, assetHTTP(s, "GET", path+"?"+query.Encode(), "phone-key"))
			if len(p.Items) != tt.def {
				t.Fatalf("default got %d want %d", len(p.Items), tt.def)
			}
			query.Set("limit", fmt.Sprint(tt.max))
			seen := map[string]bool{}
			total := 0
			for {
				p = assetResponse(t, assetHTTP(s, "GET", path+"?"+query.Encode(), "phone-key"))
				if len(p.Items) > tt.max {
					t.Fatal("item cap exceeded")
				}
				for _, it := range p.Items {
					if seen[it.RecordID] {
						t.Fatal("HTTP pagination repeated record")
					}
					seen[it.RecordID] = true
					total++
				}
				if p.NextCursor == nil {
					break
				}
				query.Set("cursor", *p.NextCursor)
				if total > tt.total {
					t.Fatal("pagination did not terminate")
				}
			}
			if total != tt.total {
				t.Fatalf("got %d want %d", total, tt.total)
			}
			w := assetHTTP(s, "GET", path+"?limit="+fmt.Sprint(tt.max+1), "phone-key")
			if w.Code != 400 || !strings.Contains(w.Body.String(), "INVALID_PARAMS") {
				t.Fatal("over-limit was accepted")
			}
			if w := assetHTTP(s, "POST", path, "phone-key"); w.Code != 400 {
				t.Fatal("POST must require an explicit read-only GET envelope")
			}
		})
	}
	logs, e := os.ReadFile(s.logger.logFile)
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"notifications", "locations", "calendars", "health-data", "web-pages"} {
		if !bytes.Contains(logs, []byte("assets.read kind="+kind+" status=200")) {
			t.Fatal("missing completed response log", kind)
		}
	}
}

func TestAssetsHTTPAuthProfileAndCapabilities(t *testing.T) {
	s, _ := newTestServer(t, "gateway-secret")
	seedAssetEndpoints(t, s)
	s.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "phone-a", Key: "a-key"}, {Label: "phone-b", Key: "b-key"}}}
	for _, kind := range []string{"notifications", "locations", "calendars", "health-data", "web-pages"} {
		path := "/assets/" + kind
		for _, token := range []string{"", "unknown"} {
			w := assetHTTP(s, "GET", path, token)
			if w.Code != 401 || !strings.Contains(w.Body.String(), `"action":"reauth"`) || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("unauthorized read")
			}
		}
		if kind == "calendars" {
			path += "?anchorDate=2026-10-22"
		}
		p := assetResponse(t, assetHTTP(s, "GET", path, "a-key"))
		if len(p.Items) == 0 {
			t.Fatal("authenticated profile read returned no assets", kind)
		}
		for _, token := range []string{"b-key", "gateway-secret"} {
			other := assetResponse(t, assetHTTP(s, "GET", path, token))
			if !reflect.DeepEqual(other.Items, p.Items) || !reflect.DeepEqual(other.Warnings, p.Warnings) {
				t.Fatal("same profile credentials did not share assets", kind)
			}
		}
		if p.NextCursor == nil {
			t.Fatal("missing cursor fixture", kind)
		}
		nextPath := "/assets/" + kind + "?cursor=" + url.QueryEscape(*p.NextCursor)
		expected := assetResponse(t, assetHTTP(s, "GET", nextPath, "a-key"))
		for _, token := range []string{"b-key", "gateway-secret"} {
			next := assetResponse(t, assetHTTP(s, "GET", nextPath, token))
			if !reflect.DeepEqual(next.Items, expected.Items) || len(next.Items) == 0 || next.Items[0].RecordID == p.Items[0].RecordID {
				t.Fatal("same profile cursor failed across credentials", kind)
			}
		}
		if w := assetHTTP(s, "GET", nextPath, "unknown"); w.Code != 401 {
			t.Fatal("cursor bypassed authentication", kind)
		}
	}
	for _, path := range []string{"/health", "/gateway/health"} {
		method := "GET"
		if path == "/gateway/health" {
			method = "POST"
		}
		w := assetHTTP(s, method, path, "gateway-secret")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		for _, kind := range []string{"notifications", "web-pages", "locations", "calendars", "health-data"} {
			if !strings.Contains(w.Body.String(), "assets."+kind+".read.v1") {
				t.Fatal("missing capability", kind)
			}
		}
	}
}

func TestAssetsHTTPRejectMalformedParamsAndKeepRecordingRoute(t *testing.T) {
	s, _ := newTestServer(t, "gateway-secret")
	for _, query := range []string{"limit=1&limit=2", "cursor=&cursor=", "unknown=1", "limit=-1", "limit=", "limit=1;evil=2", "cursor=%ff", "cursor=%zz"} {
		w := assetHTTP(s, "GET", "/assets/notifications?"+query, "gateway-secret")
		if w.Code != 400 {
			t.Fatalf("accepted query %s: %d", query, w.Code)
		}
	}
	for _, path := range []string{"/assets/recordings", "/assets/dictation", "/assets/notifications/../locations"} {
		if w := assetHTTP(s, "GET", path, "gateway-secret"); w.Code != 404 {
			t.Fatal("unrequested asset route", path)
		}
	}
	storage := withRecordings(t, s)
	if _, e := storage.Ingest("rec1", recording.Metadata{Name: "Original route"}, "phone-a"); e != nil {
		t.Fatal(e)
	}
	w := assetHTTP(s, "POST", "/gateway/recordings.list", "gateway-secret")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"recordings"`) || !strings.Contains(w.Body.String(), `"total":1`) || strings.Contains(w.Body.String(), "nextCursor") {
		t.Fatalf("recording contract changed: %s", w.Body.String())
	}
}

func TestAssetsConcurrentUploadReadAndSnapshotAcquisition(t *testing.T) {
	s, _ := newTestServer(t, "gateway-secret")
	const workers = 6
	var wg sync.WaitGroup
	failures := make(chan string, workers)
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				body := fmt.Sprintf(`{"notifications":[{"id":"%d-%d","app":"app","title":"title-%d-%d","body":"body","timestamp":"2026-10-22T00:00:00Z"}]}`, worker, i, worker, i)
				r := httptest.NewRequest("POST", "/notifications", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer gateway-secret")
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != 200 {
					failures <- w.Body.String()
					return
				}
				w = assetHTTP(s, "GET", "/assets/notifications?limit=100", "gateway-secret")
				if w.Code != 200 || w.Body.Len() > assets.ResponseLimit || strings.Contains(w.Body.String(), "INVALID_RECORD_SKIPPED") || strings.Contains(w.Body.String(), "INCOMPLETE_TAIL_SKIPPED") {
					failures <- w.Body.String()
					return
				}
			}
		}(n)
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		t.Fatal(e)
	}
	p := assetResponse(t, assetHTTP(s, "GET", "/assets/notifications?limit=100", "gateway-secret"))
	if len(p.Items) != 100 || p.NextCursor == nil {
		t.Fatal("concurrent append count")
	}
	q := "/assets/notifications?limit=100&cursor=" + url.QueryEscape(*p.NextCursor)
	if len(assetResponse(t, assetHTTP(s, "GET", q, "gateway-secret")).Items) != 50 {
		t.Fatal("concurrent append lost records")
	}
	// Forging a Relay header without the gateway credential still fails.
	req := httptest.NewRequest("GET", "/assets/notifications", nil)
	req.Header.Set(relay.InternalHTTPHeader, "1")
	req.Header.Set(relay.InternalClientLabelHeader, "phone-a")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("forged internal header authenticated")
	}
}
