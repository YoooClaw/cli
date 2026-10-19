package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/gorilla/websocket"
)

const calendarSnapshot = `{"startDate":"2026-10-16","endDate":"2026-10-18","collectedAt":"2026-10-16T12:00:00+08:00","items":[{"id":"instance","calendarName":"工作","title":"夜间活动","startAt":"2026-10-16T23:00:00+08:00","endAt":"2026-10-18T00:00:00+08:00","timeZone":"Asia/Shanghai","isAllDay":false,"extra":{"n":9007199254740993}}]}`

func TestCalendarsAcceptAPIKeyAndReplaceDateFiles(t *testing.T) {
	srv, _ := newTestServer(t, "gateway-secret")
	srv.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "phone", Key: "phone-key"}}}
	post := func(body, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/calendars", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	for _, key := range []string{"phone-key", "gateway-secret"} {
		w := post(calendarSnapshot, key)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ingested":1`) ||
			!strings.Contains(w.Body.String(), `"filesWritten":3`) {
			t.Fatalf("response: %d %s", w.Code, w.Body.String())
		}
	}
	for _, day := range []string{"2026-10-16", "2026-10-17"} {
		raw, err := os.ReadFile(filepath.Join(srv.ctx.Paths.Calendars, day+".jsonl"))
		if err != nil || bytes.Count(raw, []byte{'\n'}) != 1 || !bytes.Contains(raw, []byte(`9007199254740993`)) {
			t.Fatalf("%s: %s %v", day, raw, err)
		}
		if bytes.Contains(raw, []byte("clientLabel")) || bytes.Contains(raw, []byte("receivedAt")) {
			t.Fatalf("unexpected metadata: %s", raw)
		}
	}
	w := post(`{"startDate":"2026-10-17","endDate":"2026-10-18","collectedAt":"2026-10-16T13:00:00+08:00","items":[]}`, "phone-key")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ingested":0`) {
		t.Fatalf("empty snapshot: %d %s", w.Code, w.Body.String())
	}
	for _, day := range []string{"2026-10-17", "2026-10-18"} {
		raw, err := os.ReadFile(filepath.Join(srv.ctx.Paths.Calendars, day+".jsonl"))
		if err != nil || len(raw) != 0 {
			t.Fatalf("empty snapshot did not clear %s: %s %v", day, raw, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(srv.ctx.Paths.Calendars, "2026-10-16.jsonl"))
	if err != nil || len(raw) == 0 {
		t.Fatalf("history lost: %s %v", raw, err)
	}
	health := httptest.NewRecorder()
	srv.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if !strings.Contains(health.Body.String(), `"calendars"`) || !isIngestPath("/calendars") {
		t.Fatal("calendar capability/ingest path not exposed")
	}
}

func TestCalendarsInvalidBatchDoesNotOverwriteExistingFiles(t *testing.T) {
	srv, _ := newTestServer(t, "")
	if err := os.MkdirAll(srv.ctx.Paths.Calendars, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(srv.ctx.Paths.Calendars, "2026-10-16.jsonl")
	const original = "existing calendar\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{}`, `null`, `{"items":`,
		`{"startDate":"2026-10-16","endDate":"2026-10-18"}`,
		`{"startDate":"2026-10-16","endDate":"2026-10-18","items":null}`,
		`{"startDate":"2026-10-16","endDate":"2026-10-18","items":[]}`,
		`{"startDate":"2026-10-16","endDate":"2026-10-18","collectedAt":"2026-10-16","items":[]}`,
		strings.Replace(calendarSnapshot, `"timeZone":"Asia/Shanghai"`, `"timeZone":"not-a-zone"`, 1),
		strings.Replace(calendarSnapshot, `}]}`, `},{}]}`, 1),
	} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/calendars", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != original {
			t.Fatalf("invalid batch changed file: %s %v", raw, err)
		}
	}
}

func TestCalendarsAuthAndWriteFailure(t *testing.T) {
	srv, _ := newTestServer(t, "gateway-secret")
	for _, token := range []string{"", "unknown-key"} {
		r := httptest.NewRequest(http.MethodPost, "/calendars", strings.NewReader(calendarSnapshot))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized: %d %s", w.Code, w.Body.String())
		}
	}
	if err := os.MkdirAll(filepath.Join(srv.ctx.Paths.Calendars, "2026-10-16.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/calendars", strings.NewReader(calendarSnapshot))
	r.Header.Set("Authorization", "Bearer gateway-secret")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"CALENDAR_WRITE_FAILED"`) {
		t.Fatalf("write failure acknowledged as success: %d %s", w.Code, w.Body.String())
	}
}

func TestCalendarsRelayRoundTrip(t *testing.T) {
	srv, local := newTestServer(t, "gateway-secret")
	type result struct {
		response relay.ProxyResponseFrame
		err      error
	}
	done := make(chan result, 1)
	upgrader := websocket.Upgrader{}
	fakeRelay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteJSON(relay.Frame{
			"type": "request", "id": "calendar-test", "method": "POST", "path": "calendars", "body": calendarSnapshot,
		}); err != nil {
			done <- result{err: err}
			return
		}
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				done <- result{err: err}
				return
			}
			if string(raw) == "ping" {
				_ = conn.WriteMessage(websocket.TextMessage, []byte("pong"))
				continue
			}
			var response relay.ProxyResponseFrame
			err = json.Unmarshal(raw, &response)
			done <- result{response: response, err: err}
			return
		}
	}))
	defer fakeRelay.Close()
	client := relay.NewClient(relay.ClientOptions{
		TunnelURL:          "ws" + strings.TrimPrefix(fakeRelay.URL, "http"),
		CredentialProvider: func() (relay.Credential, error) { return relay.Credential{}, nil },
		Logger:             srv.logger,
	})
	dispatcher := relay.NewDispatcher(relay.DispatcherOptions{
		Client: client, HTTPBaseURL: local.URL, HTTPToken: "gateway-secret", ClientLabel: "phone", Logger: srv.logger,
	})
	dispatcher.Start()
	client.Start()
	defer client.Stop("test complete")
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.response.ID != "calendar-test" || got.response.Status != http.StatusOK ||
			!strings.Contains(got.response.Body, `"filesWritten":3`) {
			t.Fatalf("unexpected Relay response: %+v", got.response)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Relay response timed out")
	}
	raw, err := os.ReadFile(filepath.Join(srv.ctx.Paths.Calendars, "2026-10-17.jsonl"))
	if err != nil || !bytes.Contains(raw, []byte(`"id":"instance"`)) {
		t.Fatalf("cross-day calendar not saved: %s %v", raw, err)
	}
	logs, err := os.ReadFile(srv.logger.logFile)
	if err != nil || !strings.Contains(string(logs), "POST calendars -> /calendars") ||
		!strings.Contains(string(logs), "requestId=calendar-test range=2026-10-16..2026-10-18 ingested=1 files=3") {
		t.Fatalf("missing request/write-result logs: %s %v", logs, err)
	}
}

func TestCalendarsRejectFutureAndSkipStaleSnapshots(t *testing.T) {
	srv, _ := newTestServer(t, "")
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/calendars", strings.NewReader(body)))
		return w
	}
	future := strings.Replace(calendarSnapshot, "2026-10-16T12:00:00+08:00", time.Now().Add(48*time.Hour).Format(time.RFC3339), 1)
	if w := post(future); w.Code != http.StatusBadRequest {
		t.Fatalf("future collectedAt accepted: %d %s", w.Code, w.Body.String())
	}
	if w := post(calendarSnapshot); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"applied":true`) {
		t.Fatalf("fresh snapshot rejected: %d %s", w.Code, w.Body.String())
	}
	stale := strings.Replace(calendarSnapshot, `"id":"instance"`, `"id":"stale"`, 1)
	stale = strings.Replace(stale, "2026-10-16T12:00:00+08:00", "2026-10-16T11:00:00+08:00", 1)
	if w := post(stale); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"applied":false`) ||
		!strings.Contains(w.Body.String(), `"ingested":0`) || !strings.Contains(w.Body.String(), `"filesWritten":0`) {
		t.Fatalf("stale snapshot not skipped: %d %s", w.Code, w.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(srv.ctx.Paths.Calendars, "2026-10-16.jsonl"))
	if err != nil || !bytes.Contains(raw, []byte(`"id":"instance"`)) || bytes.Contains(raw, []byte(`"id":"stale"`)) {
		t.Fatalf("stale snapshot changed stored data: %s %v", raw, err)
	}
	logs, err := os.ReadFile(srv.logger.logFile)
	if err != nil || !strings.Contains(string(logs), "skipped stale snapshot") {
		t.Fatalf("missing stale log: %s %v", logs, err)
	}
}
