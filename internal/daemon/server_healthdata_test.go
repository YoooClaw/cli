package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
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

const healthBatch = `{
  "schemaVersion":1,"provider":"healthkit","collectedAt":"2026-10-19T09:10:00+08:00",
  "items":[{"id":"source-id","type":"HKQuantityTypeIdentifierStepCount",
    "startAt":"2026-10-19T08:00:00+08:00","endAt":"2026-10-19T08:10:00+08:00",
    "data":{"value":820,"unit":"count","extra":{"large":9007199254740993,"future":1e999}}}],
  "extension":{"keep":true}
}`

func TestHealthDataPreservesWholeBatchAndAcceptsExistingIngestCredentials(t *testing.T) {
	srv, _ := newTestServer(t, "gateway-secret")
	srv.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "phone", Key: "phone-key"}}}
	for _, token := range []string{"phone-key", "gateway-secret"} {
		request := httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(healthBatch))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, request)
		assertHealthReceipt(t, response.Code, response.Body.Bytes(), 1, 0)
	}
	raw, err := os.ReadFile(healthDataFile(srv))
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(healthBatch)); err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat(append(compact.Bytes(), '\n'), 2)
	if !bytes.Equal(raw, want) {
		t.Fatalf("batch changed, server metadata added or duplicate removed: %s", raw)
	}
	health := httptest.NewRecorder()
	srv.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if !strings.Contains(health.Body.String(), `"health-data"`) || !strings.Contains(health.Body.String(), `"health-data-daily-v2"`) || !isIngestPath("/health-data") {
		t.Fatalf("health-data capability/auth path missing: %s", health.Body.String())
	}
}

func TestHealthDataDeletionIsStoredAsAnEventWithoutRemovingEarlierData(t *testing.T) {
	srv, _ := newTestServer(t, "")
	deletion := `{"schemaVersion":1,"provider":"healthkit","collectedAt":"2026-10-19T10:00:00+08:00","items":[],"deletedIds":["source-id"]}`
	for i, body := range []string{healthBatch, deletion, `{"items":[]}`} {
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(body)))
		items, deleted := 0, 0
		if i == 0 {
			items = 1
		} else if i == 1 {
			deleted = 1
		}
		assertHealthReceipt(t, response.Code, response.Body.Bytes(), items, deleted)
	}
	raw, err := os.ReadFile(healthDataFile(srv))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	if len(lines) != 3 || !bytes.Contains(lines[0], []byte(`"id":"source-id"`)) || string(lines[1]) != deletion {
		t.Fatalf("source delete must remain an event, not erase stored history: %s", raw)
	}
}

func TestHealthDataInvalidBatchDoesNotAppendOrModifyPreviousData(t *testing.T) {
	srv, _ := newTestServer(t, "")
	if err := os.MkdirAll(filepath.Dir(healthDataFile(srv)), 0o700); err != nil {
		t.Fatal(err)
	}
	prior := []byte("{\"items\":[]}\n")
	if err := os.WriteFile(healthDataFile(srv), prior, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		``, `null`, `[]`, `{}`, `{"items":null}`, `{"items":[{},null]}`,
		`{"items":[{}],"deletedIds":["valid",null]}`, `{"items":[],"deletedIds":[1]}`,
		`{"items":[]}{"items":[]}`, `{"items":`,
	} {
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s: response=%d %s", body, response.Code, response.Body.String())
		}
	}
	raw, err := os.ReadFile(healthDataFile(srv))
	if err != nil || !bytes.Equal(raw, prior) {
		t.Fatalf("invalid input modified old data: %s err=%v", raw, err)
	}
}

func TestHealthDataAuthAndWriteFailureNeverAcknowledgeSuccess(t *testing.T) {
	srv, _ := newTestServer(t, "gateway-secret")
	for _, token := range []string{"", "unknown-key"} {
		request := httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(healthBatch))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized response=%d %s", response.Code, response.Body.String())
		}
	}
	if _, err := os.Stat(srv.ctx.Paths.HealthData); !os.IsNotExist(err) {
		t.Fatalf("unauthorized input wrote health data: %v", err)
	}
	if err := os.MkdirAll(healthDataFile(srv), 0o700); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/health-data", strings.NewReader(healthBatch))
	request.Header.Set("Authorization", "Bearer gateway-secret")
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"HEALTH_DATA_WRITE_FAILED"`) {
		t.Fatalf("write failure acknowledged: %d %s", response.Code, response.Body.String())
	}
}

func TestHealthDataRelayRoundTripIncludesDeletionReceiptAndOriginalJSONL(t *testing.T) {
	srv, local := newTestServer(t, "gateway-secret")
	daily := `{"schemaVersion":2,"provider":"healthkit","timeZone":"Asia/Shanghai","collectedAt":"2026-10-19T16:00:00+08:00","days":[{"date":"2026-09-30","items":[{"metric":"steps","name":"Steps","value":31502,"unit":"steps"}]}]}`
	payloads := []string{healthBatch, `{"provider":"healthkit","items":[],"deletedIds":["source-id"]}`, daily, daily}
	type result struct {
		responses []relay.ProxyResponseFrame
		err       error
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
		var responses []relay.ProxyResponseFrame
		for i, body := range payloads {
			if err := conn.WriteJSON(relay.Frame{
				"type": "request", "id": fmt.Sprintf("health-test-%d", i), "method": "POST", "path": "health-data", "body": body,
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
				if err := json.Unmarshal(raw, &response); err != nil {
					done <- result{err: err}
					return
				}
				responses = append(responses, response)
				break
			}
		}
		done <- result{responses: responses}
	}))
	defer fakeRelay.Close()
	client := relay.NewClient(relay.ClientOptions{
		TunnelURL:          "ws" + strings.TrimPrefix(fakeRelay.URL, "http"),
		CredentialProvider: func() (relay.Credential, error) { return relay.Credential{}, nil }, Logger: srv.logger,
	})
	dispatcher := relay.NewDispatcher(relay.DispatcherOptions{
		Client: client, HTTPBaseURL: local.URL, HTTPToken: "gateway-secret", ClientLabel: "phone", Logger: srv.logger,
	})
	dispatcher.Start()
	client.Start()
	defer client.Stop("test complete")
	select {
	case got := <-done:
		if got.err != nil || len(got.responses) != len(payloads) {
			t.Fatalf("Relay responses=%+v err=%v", got.responses, got.err)
		}
		for i, response := range got.responses {
			if response.Type != "proxy_response" || response.ID != fmt.Sprintf("health-test-%d", i) {
				t.Fatalf("unexpected Relay response: %+v", response)
			}
			if i < 2 {
				assertHealthReceipt(t, response.Status, []byte(response.Body), 1-i, i)
			} else {
				assertHealthReceipt(t, response.Status, []byte(response.Body), 1, 0)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("health-data Relay response timed out")
	}
	raw, err := os.ReadFile(healthDataFile(srv))
	if err != nil || len(bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})) != 2 ||
		!bytes.Contains(raw, []byte(`"large":9007199254740993`)) || !bytes.Contains(raw, []byte(`"deletedIds":["source-id"]`)) {
		t.Fatalf("Relay batch did not reach JSONL unchanged: %s err=%v", raw, err)
	}
	dailyRaw, err := os.ReadFile(filepath.Join(srv.ctx.Paths.HealthData, "2026-09-30.jsonl"))
	if err != nil || len(bytes.Split(bytes.TrimSpace(dailyRaw), []byte{'\n'})) != 2 || bytes.Count(dailyRaw, []byte(`"value":31502`)) != 1 {
		t.Fatalf("Relay daily replays were not replaced once by business date: %s err=%v", dailyRaw, err)
	}
	logs, err := os.ReadFile(srv.logger.logFile)
	if err != nil || !strings.Contains(string(logs), "POST health-data -> /health-data") ||
		!strings.Contains(string(logs), "requestId=health-test-0 ingested=1 deleted=0") ||
		!strings.Contains(string(logs), "requestId=health-test-1 ingested=0 deleted=1") ||
		!strings.Contains(string(logs), "requestId=health-test-3 ingested=1 deleted=0") {
		t.Fatalf("missing actual request/storage result logs: %s err=%v", logs, err)
	}
}

func healthDataFile(srv *server) string {
	return filepath.Join(srv.ctx.Paths.HealthData, "legacy", "receipts", time.Now().Format("2006-01-02")+".jsonl")
}

func assertHealthReceipt(t *testing.T, status int, raw []byte, items, deleted int) {
	t.Helper()
	var receipt struct {
		OK       bool `json:"ok"`
		Ingested int  `json:"ingested"`
		Deleted  int  `json:"deleted"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil || status != http.StatusOK ||
		!receipt.OK || receipt.Ingested != items || receipt.Deleted != deleted {
		t.Fatalf("receipt=%d %s err=%v, want ok=true ingested=%d deleted=%d", status, raw, err, items, deleted)
	}
}
