package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/assets"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/gorilla/websocket"
)

func decodeSummaryHTTP(t *testing.T, status int, header http.Header, raw []byte) assets.SummaryResponse {
	t.Helper()
	if status != 200 || header.Get("Cache-Control") != "no-store" || header.Get("Content-Type") != "application/json; charset=utf-8" || len(raw) > assets.ResponseLimit {
		t.Fatalf("summary HTTP envelope: %d %s", status, raw)
	}
	var out assets.SummaryResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Assets == nil {
		t.Fatalf("summary envelope: %s", raw)
	}
	return out
}

func seedSummaryRecordings(t *testing.T, s *server) {
	t.Helper()
	storage := withRecordings(t, s)
	day := time.Date(2026, 10, 22, 9, 0, 0, 0, time.Local)
	for i, label := range []string{"legacy", "phone-a", "phone-b"} {
		_, err := storage.Ingest(fmt.Sprint(i), recording.Metadata{Name: fmt.Sprint(i), CreatedAt: day.Add(time.Duration(i) * time.Hour).UTC().Format(time.RFC3339)}, label)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAssetsSummaryHTTPGETAndClientBridge(t *testing.T) {
	s, ts := newTestServer(t, "gateway-secret")
	seedAssetEndpoints(t, s)
	seedSummaryRecordings(t, s)
	path := "/assets/summary?date=2026-10-22"
	var first assets.SummaryResponse
	for _, method := range []string{"GET", "POST"} {
		body := ""
		if method == "POST" {
			body = `{"method":"GET","body":null}`
		}
		req, err := http.NewRequestWithContext(context.Background(), method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer gateway-secret")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		out := decodeSummaryHTTP(t, resp.StatusCode, resp.Header, raw)
		if out.Partial || len(out.Assets) != 7 || *out.Assets["notifications"].Count != 125 || *out.Assets["recordings"].Count != 3 ||
			out.Assets["health-data"].Count != nil || *out.Assets["health-data"].Latest.StorageDate != "2026-10-16" {
			t.Fatalf("summary categories: %+v", out)
		}
		if method == "GET" {
			first = out
		} else if !reflect.DeepEqual(first.Assets, out.Assets) {
			t.Fatal("GET and POST envelope returned different summaries")
		}
	}
	logs, err := os.ReadFile(s.logger.logFile)
	if err != nil || !strings.Contains(string(logs), "assets.summary status=200 date=2026-10-22 categories=7 partial=false") {
		t.Fatal("missing completed summary response log")
	}
	if strings.Contains(string(logs), `"content":"body"`) {
		t.Fatal("summary content leaked to diagnostics")
	}
}

func TestAssetsSummaryCredentialsAndRecordingScope(t *testing.T) {
	s, _ := newTestServer(t, "gateway-secret")
	seedAssetEndpoints(t, s)
	seedSummaryRecordings(t, s)
	s.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "phone-a", Key: "a-key"}, {Label: "phone-b", Key: "b-key"}}}
	for token, want := range map[string]int64{"gateway-secret": 3, "a-key": 2, "b-key": 2} {
		w := assetHTTP(s, "GET", "/assets/summary?date=2026-10-22", token)
		out := decodeSummaryHTTP(t, w.Code, w.Header(), w.Body.Bytes())
		if out.Partial || *out.Assets["recordings"].Count != want || *out.Assets["notifications"].Count != 125 {
			t.Fatalf("credential scope for %s: %+v", token, out)
		}
		if token == "a-key" && out.Assets["recordings"].Latest.Data["recordingId"] != "1" ||
			token == "b-key" && out.Assets["recordings"].Latest.Data["recordingId"] != "2" {
			t.Fatal("recording summary bypassed source visibility")
		}
	}
	for _, token := range []string{"", "wrong"} {
		w := assetHTTP(s, "GET", "/assets/summary", token)
		if w.Code != 401 || !strings.Contains(w.Body.String(), `"action":"reauth"`) {
			t.Fatal("summary authentication bypass")
		}
	}
	req := httptest.NewRequest("GET", "/assets/summary?types=recordings", nil)
	req.Header.Set(relay.InternalHTTPHeader, "1")
	req.Header.Set(relay.InternalClientLabelHeader, "phone-a")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("forged Relay headers bypassed summary auth")
	}
	for _, path := range []string{"/health", "/gateway/health"} {
		method := "GET"
		if path == "/gateway/health" {
			method = "POST"
		}
		if w := assetHTTP(s, method, path, "gateway-secret"); !strings.Contains(w.Body.String(), "assets.summary.read.v1") {
			t.Fatal("missing summary capability", path)
		}
	}
}

func TestAssetsSummaryRejectsInvalidParamsAndWriteEnvelopes(t *testing.T) {
	s, _ := newTestServer(t, "gateway-secret")
	for _, query := range []string{"date=", "date=2026-02-30", "date=2026-10-22&date=2026-10-23", "types=", "types=notifications,notifications", "types=dictation", "limit=1", "timeZone=UTC", "date=%zz", "types=%ff", "date=2026-10-22;evil=1"} {
		w := assetHTTP(s, "GET", "/assets/summary?"+query, "gateway-secret")
		if w.Code != 400 || !strings.Contains(w.Body.String(), "INVALID_PARAMS") {
			t.Fatalf("accepted query %q: %d %s", query, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{"", `{}`, `{"method":"POST"}`, `{"method":"GET","body":{}}`, `{"method":"GET","extra":1}`, `{"method":"GET"} {}`, `{"method":"GET"}` + strings.Repeat(" ", 4096)} {
		if w := assetBridge(s, "/assets/summary", body, "gateway-secret"); w.Code != 400 {
			t.Fatalf("accepted summary envelope %q", body)
		}
	}
	for _, method := range []string{"PUT", "DELETE", "PATCH"} {
		if w := assetHTTP(s, method, "/assets/summary", "gateway-secret"); w.Code != 405 || w.Header().Get("Allow") != "GET, POST" {
			t.Fatal("write method accepted")
		}
	}
	if w := assetHTTP(s, "GET", "/assets/summary/?types=notifications", "gateway-secret"); w.Code != 404 {
		t.Fatal("unexpected summary subroute")
	}
}

func TestAssetsSummaryPartialErrorHTTP(t *testing.T) {
	s, _ := newTestServer(t, "gateway-secret")
	seedAssetEndpoints(t, s)
	assetFile(t, s.ctx.Paths.Locations, "2026-10-22.jsonl", nil)
	// A malformed newest health file must not silently become an empty result.
	assetFile(t, s.ctx.Paths.HealthData, "2026-10-23.jsonl", []byte("broken"))
	w := assetHTTP(s, "GET", "/assets/summary?date=2026-10-22&types=notifications,health-data", "gateway-secret")
	out := decodeSummaryHTTP(t, w.Code, w.Header(), w.Body.Bytes())
	if !out.Partial || out.Assets["notifications"].Count == nil || *out.Assets["notifications"].Count != 125 ||
		out.Assets["health-data"].Error == nil || out.Assets["health-data"].Latest != nil {
		t.Fatalf("partial result contract: %+v", out)
	}
	if strings.Contains(w.Body.String(), s.ctx.Paths.HealthData) {
		t.Fatal("source path leaked to client")
	}
}

func TestAssetsSummaryConcurrentIngest(t *testing.T) {
	s, _ := newTestServer(t, "gateway-secret")
	date := time.Now().In(time.Local).Format(time.DateOnly)
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Go(func() {
			body := fmt.Sprintf(`{"notifications":[{"id":"%d","app":"app","body":"%d","timestamp":%q}]}`, i, i, time.Now().UTC().Format(time.RFC3339Nano))
			if w := assetBridge(s, "/notifications", body, "gateway-secret"); w.Code != 200 {
				t.Errorf("ingest failed: %s", w.Body.String())
			}
			w := assetHTTP(s, "GET", "/assets/summary?date="+date+"&types=notifications", "gateway-secret")
			var out assets.SummaryResponse
			if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Partial || !out.OK || out.Assets["notifications"].Count == nil {
				t.Errorf("concurrent summary failed: %s", w.Body.String())
			}
		})
	}
	wg.Wait()
	w := assetHTTP(s, "GET", "/assets/summary?date="+date+"&types=notifications", "gateway-secret")
	out := decodeSummaryHTTP(t, w.Code, w.Header(), w.Body.Bytes())
	if *out.Assets["notifications"].Count != 30 {
		t.Fatal("summary missed successful concurrent uploads")
	}
}

func TestAssetsSummaryRelayRoundTrip(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			s, local := newTestServer(t, "gateway-secret")
			seedAssetEndpoints(t, s)
			done := make(chan error, 1)
			upgrader := websocket.Upgrader{}
			fakeRelay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
				frame := relay.Frame{"type": "request", "id": "summary", "method": method, "path": "assets/summary?date=2026-10-22&types=notifications,health-data"}
				if method == "POST" {
					frame["body"] = `{"method":"GET","body":null}`
				}
				if err := conn.WriteJSON(frame); err != nil {
					done <- err
					return
				}
				for {
					_, raw, err := conn.ReadMessage()
					if err != nil {
						done <- err
						return
					}
					if string(raw) == "ping" {
						_ = conn.WriteMessage(websocket.TextMessage, []byte("pong"))
						continue
					}
					var response relay.ProxyResponseFrame
					var out assets.SummaryResponse
					if json.Unmarshal(raw, &response) != nil || response.Status != 200 || response.ID != "summary" ||
						response.Headers["cache-control"] != "no-store" || json.Unmarshal([]byte(response.Body), &out) != nil ||
						!out.OK || out.Partial || len(out.Assets) != 2 || out.Assets["notifications"].Count == nil ||
						*out.Assets["notifications"].Count != 125 || out.Assets["health-data"].Count != nil {
						done <- fmt.Errorf("invalid Relay summary response: %s", raw)
						return
					}
					done <- nil
					return
				}
			}))
			defer fakeRelay.Close()
			client := relay.NewClient(relay.ClientOptions{TunnelURL: "ws" + strings.TrimPrefix(fakeRelay.URL, "http"), CredentialProvider: func() (relay.Credential, error) { return relay.Credential{}, nil }, Logger: s.logger})
			dispatcher := relay.NewDispatcher(relay.DispatcherOptions{Client: client, HTTPBaseURL: local.URL, HTTPToken: "gateway-secret", ClientLabel: "phone-b", Logger: s.logger})
			dispatcher.Start()
			client.Start()
			defer client.Stop("test complete")
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Relay summary timed out")
			}
			logs, err := os.ReadFile(s.logger.logFile)
			if err != nil || !strings.Contains(string(logs), method+" assets/summary?") || !strings.Contains(string(logs), "assets.summary status=200") {
				t.Fatal("missing actual transport/completed summary logs")
			}
		})
	}
}
