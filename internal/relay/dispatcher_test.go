package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type testLogger struct{ t *testing.T }

func (l testLogger) Debug(msg string) { l.t.Log("[DEBUG] " + msg) }
func (l testLogger) Info(msg string)  { l.t.Log("[INFO] " + msg) }
func (l testLogger) Warn(msg string)  { l.t.Log("[WARN] " + msg) }
func (l testLogger) Error(msg string) { l.t.Log("[ERROR] " + msg) }

func TestClientDispatcherReqAndRequest(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(InternalHTTPHeader) != "1" {
			t.Fatalf("missing internal relay header on %s", r.URL.Path)
		}
		if r.Header.Get(InternalClientLabelHeader) != "phone-a" {
			t.Fatalf("missing client label on %s: %q", r.URL.Path, r.Header.Get(InternalClientLabelHeader))
		}
		if r.Header.Get("Authorization") != "Bearer gateway-token" {
			t.Fatalf("missing loopback gateway token on %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/gateway/notifications.push":
			if r.Header.Get(InternalRequestIDHeader) != "req_1" {
				t.Fatalf("missing relay request id on %s: %q", r.URL.Path, r.Header.Get(InternalRequestIDHeader))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"data":{"ok":true,"echo":"notifications.push"}}`))
		case "/notifications":
			if r.Header.Get(InternalRequestIDHeader) != "http_1" {
				t.Fatalf("missing relay request id on %s: %q", r.URL.Path, r.Header.Get(InternalRequestIDHeader))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"ingested":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer local.Close()

	done := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	relayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apiKey") != "relay-key" {
			t.Fatalf("unexpected apiKey query: %q", r.URL.Query().Get("apiKey"))
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		if err := conn.WriteJSON(map[string]any{"type": "req", "id": "req_1", "method": "notifications.push", "params": map[string]any{"items": []any{}}}); err != nil {
			done <- err
			return
		}
		msg, err := readNonHeartbeat(conn)
		if err != nil {
			done <- err
			return
		}
		wantPrefix := `{"type":"res","id":"req_1","ok":true,"payload":`
		if !strings.HasPrefix(msg, wantPrefix) {
			t.Fatalf("unexpected req response order/body: %s", msg)
		}
		var reqResp struct {
			Type    string         `json:"type"`
			ID      string         `json:"id"`
			OK      bool           `json:"ok"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal([]byte(msg), &reqResp); err != nil {
			done <- err
			return
		}
		if reqResp.Payload["echo"] != "notifications.push" {
			t.Fatalf("unexpected req payload: %+v", reqResp.Payload)
		}

		if err := conn.WriteJSON(map[string]any{
			"type": "request", "id": "http_1", "method": "POST", "path": "/api/message/messageBridge/send",
			"headers": map[string]string{"authorization": "Bearer external"}, "body": `{"notifications":[]}`,
		}); err != nil {
			done <- err
			return
		}
		msg, err = readNonHeartbeat(conn)
		if err != nil {
			done <- err
			return
		}
		wantPrefix = `{"type":"proxy_response","id":"http_1","status":200,`
		if !strings.HasPrefix(msg, wantPrefix) {
			t.Fatalf("unexpected proxy response order/body: %s", msg)
		}
		var httpResp struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			Status int    `json:"status"`
			Body   string `json:"body"`
		}
		if err := json.Unmarshal([]byte(msg), &httpResp); err != nil {
			done <- err
			return
		}
		if !strings.Contains(httpResp.Body, `"ingested":1`) {
			t.Fatalf("unexpected proxy body: %s", httpResp.Body)
		}
		done <- nil
	}))
	defer relayServer.Close()

	client := NewClient(ClientOptions{
		TunnelURL:          "ws" + strings.TrimPrefix(relayServer.URL, "http"),
		CredentialProvider: func() (Credential, error) { return relayCredential("relay-key"), nil },
		HeartbeatSec:       60,
		ReconnectBackoffMs: 50,
		Logger:             testLogger{t},
	})
	dispatcher := NewDispatcher(DispatcherOptions{
		Client: client, HTTPBaseURL: local.URL, HTTPToken: "gateway-token", ClientLabel: "phone-a", Logger: testLogger{t},
	})
	dispatcher.Start()
	client.Start()
	defer client.Stop("test done")

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay dispatcher test timed out")
	}
}

func readNonHeartbeat(conn *websocket.Conn) (string, error) {
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return "", err
		}
		if msgType != websocket.TextMessage {
			continue
		}
		text := string(data)
		if text == "ping" {
			if err := conn.WriteMessage(websocket.TextMessage, []byte("pong")); err != nil {
				return "", err
			}
			continue
		}
		return text, nil
	}
}
