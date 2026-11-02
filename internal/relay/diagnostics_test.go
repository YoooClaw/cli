package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type diagnosticLogger struct{ lines []string }

func (l *diagnosticLogger) Debug(s string) { l.lines = append(l.lines, s) }
func (l *diagnosticLogger) Info(s string)  { l.lines = append(l.lines, s) }
func (l *diagnosticLogger) Warn(s string)  { l.lines = append(l.lines, s) }
func (l *diagnosticLogger) Error(s string) { l.lines = append(l.lines, s) }

func TestResponseMetadataExcludesContents(t *testing.T) {
	for _, typ := range []string{"res", "proxy_response"} {
		got := responseMetadata(fmt.Sprintf(`{"type":%q,"id":"relay-2003","payload":{"apiKey":"secret"},"body":"private"}`, typ))
		if got != fmt.Sprintf("type=%s id=%q", typ, "relay-2003") {
			t.Fatal(got)
		}
	}
	for _, raw := range []string{`{"type":"event","payload":"secret"}`, `not-json`, `{"type":"res","id":123}`} {
		if got := responseMetadata(raw); got != "frame" {
			t.Fatal(got)
		}
	}
	got := responseMetadata(`{"type":"res","id":"line\nforged"}`)
	if strings.Contains(got, "\n") {
		t.Fatal("unescaped log newline")
	}
}

func TestConnectionEndDiagnostics(t *testing.T) {
	l := &diagnosticLogger{}
	c := NewClient(ClientOptions{Logger: l})
	c.markInbound()
	c.logConnectionEnd("test-connection", "read", time.Now(), fmt.Errorf("read failed: %w", &websocket.CloseError{Code: 4001, Text: "private server reason"}))
	got := strings.Join(l.lines, "\n")
	for _, want := range []string{"connection=test-connection", "source=read", "closeCode=4001", "inboundIdleMs="} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s: %s", want, got)
		}
	}
	if strings.Contains(got, "private") {
		t.Fatal(got)
	}
}

func TestStatusWriteFailureIsLogged(t *testing.T) {
	l := &diagnosticLogger{}
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	c := NewClient(ClientOptions{Logger: l, StatusFilePath: filepath.Join(parent, "state.json")})
	c.writeStatus("connected", "")
	if got := strings.Join(l.lines, "\n"); !strings.Contains(got, "Relay status directory failed state=connected") {
		t.Fatal(got)
	}
}

func TestHTTPReceiveLogConnectsRequestToConnectionWithoutPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(Frame{"type": "request", "id": "relay-import", "method": "POST", "path": "/transfer/import/start?token=SECRET", "body": "PRIVATE_BODY"})
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4000, "replaced_by_new_connection"), time.Now().Add(time.Second))
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	logs := &diagnosticLogger{}
	c := NewClient(ClientOptions{Logger: logs})
	done := make(chan error, 1)
	c.readLoop(conn, done, "connection-original")
	c.logConnectionEnd("connection-original", "read", time.Now(), <-done)
	var received string
	for _, line := range logs.lines {
		if strings.HasPrefix(line, "Relay HTTP received") {
			received = line
		}
	}
	for _, want := range []string{"connection=connection-original", `id="relay-import"`, `method="POST"`, `path="/transfer/import/start"`} {
		if !strings.Contains(received, want) {
			t.Fatalf("missing %s: %s", want, received)
		}
	}
	if strings.Contains(received, "SECRET") || strings.Contains(received, "PRIVATE_BODY") {
		t.Fatal("HTTP payload leaked into metadata log")
	}
	last := logs.lines[len(logs.lines)-1]
	if !strings.Contains(last, "closeCode=4000 reason=replaced_by_new_connection") {
		t.Fatal(last)
	}
}
