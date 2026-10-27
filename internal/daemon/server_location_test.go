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

func TestLocationsPreserveFieldsAndAppendRepeatedRecords(t *testing.T) {
	srv, _ := newTestServer(t, "gateway-secret")
	srv.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "phone", Key: "phone-key"}}}
	items := []string{
		`{"longitude":0,"latitude":90,"deviceType":"iOS","id":"same-id","collectedAt":"not-validated","horizontalAccuracy":-1,"coordinateSystem":"custom","poiText":"示例地点","address":"示例地址","extra":{"n":9007199254740993}}`,
		`{"longitude":0,"latitude":0,"deviceType":null}`,
		`{"longitude":0,"latitude":0,"deviceType":""}`,
		`{"longitude":0,"latitude":0}`,
	}
	for _, token := range []string{"phone-key", "gateway-secret"} {
		req := httptest.NewRequest(http.MethodPost, "/locations", strings.NewReader(`{"items":[`+strings.Join(items, ",")+`]}`))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ingested":4`) {
			t.Fatalf("response = %d %s", w.Code, w.Body.String())
		}
	}
	raw, err := os.ReadFile(filepath.Join(srv.ctx.Paths.Locations, time.Now().Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	if len(lines) != 2*len(items) {
		t.Fatalf("got %d lines, want %d (no deduplication)", len(lines), 2*len(items))
	}
	for i, line := range lines {
		var expected map[string]json.RawMessage
		if err := json.Unmarshal([]byte(items[i%len(items)]), &expected); err != nil {
			t.Fatal(err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(expected) {
			t.Fatalf("fields added or removed: %s", line)
		}
		for key, value := range expected {
			if !bytes.Equal(got[key], value) {
				t.Fatalf("field %s changed: got %s want %s", key, got[key], value)
			}
		}
	}
}

func TestLocationsRejectInvalidBatchBeforeWriting(t *testing.T) {
	srv, _ := newTestServer(t, "")
	for _, body := range []string{
		`{"items":[{"longitude":0,"latitude":0},{"longitude":181,"latitude":0}]}`,
		`{"items":[{"longitude":null,"latitude":0}]}`,
		`{"items":[null]}`,
		`{"items":[]}`, `{}`, `null`, `{"items":`,
	} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/locations", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: got %d %s", body, w.Code, w.Body.String())
		}
	}
	if _, err := os.Stat(srv.ctx.Paths.Locations); !os.IsNotExist(err) {
		t.Fatalf("invalid batch wrote to storage: %v", err)
	}
}

func TestLocationsAuthAndWriteFailure(t *testing.T) {
	srv, _ := newTestServer(t, "gateway-secret")
	const body = `{"items":[{"longitude":0,"latitude":0}]}`
	for _, token := range []string{"", "unknown-key"} {
		req := httptest.NewRequest(http.MethodPost, "/locations", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized response = %d", w.Code)
		}
	}
	if err := os.MkdirAll(filepath.Join(srv.ctx.Paths.Locations, time.Now().Format("2006-01-02")+".jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/locations", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer gateway-secret")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"LOCATION_WRITE_FAILED"`) {
		t.Fatalf("write failure acknowledged as success: %d %s", w.Code, w.Body.String())
	}
}

func TestLocationsRelayRoundTrip(t *testing.T) {
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
		err = conn.WriteJSON(relay.Frame{
			"type": "request", "id": "location-test", "method": "POST", "path": "locations",
			"body": `{"items":[{"longitude":120.123456,"latitude":30.123456,"poiText":"示例地点"}]}`,
		})
		if err != nil {
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
		if got.response.Type != "proxy_response" || got.response.ID != "location-test" || got.response.Status != 200 ||
			!strings.Contains(got.response.Body, `"ingested":1`) {
			t.Fatalf("unexpected relay response: %+v", got.response)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay response timed out")
	}
	raw, err := os.ReadFile(filepath.Join(srv.ctx.Paths.Locations, time.Now().Format("2006-01-02")+".jsonl"))
	if err != nil || !bytes.Contains(raw, []byte(`"longitude":120.123456`)) {
		t.Fatalf("location not saved: %s %v", raw, err)
	}
	if bytes.Contains(raw, []byte("clientLabel")) || bytes.Contains(raw, []byte("receivedAt")) {
		t.Fatalf("unexpected server metadata: %s", raw)
	}
	logs, err := os.ReadFile(srv.logger.logFile)
	if err != nil || !strings.Contains(string(logs), "POST locations -> /locations") ||
		!strings.Contains(string(logs), "requestId=location-test ingested=1") {
		t.Fatalf("missing request/write-result logs: %s %v", logs, err)
	}
}
