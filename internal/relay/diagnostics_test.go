package relay

import (
	"fmt"
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
