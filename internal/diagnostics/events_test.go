package diagnostics

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeErrorRedactsCredentials(t *testing.T) {
	got := SafeError(errors.New("dial wss://host/ws?apiKey=secret123&token=token456 Authorization=Bearer bearer789 ock-cli-abc123"))
	for _, secret := range []string{"secret123", "token456", "bearer789", "ock-cli-abc123"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret leaked: %s", got)
		}
	}
}
func TestWriteEscapesLinesAndPreservesEvents(t *testing.T) {
	root := t.TempDir()
	Write(root, "info", "service.entry", map[string]any{"profile": "line1\nline2"})
	Write(root, "error", "service.failed", map[string]any{"reason": "executable_mismatch"})
	raw, err := os.ReadFile(filepath.Join(root, "logs", "daemon-supervisor.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "\n") != 2 || !strings.Contains(string(raw), "executable_mismatch") {
		t.Fatalf("invalid event output: %s", raw)
	}
}

func TestSafeLogTextEscapesAndRedacts(t *testing.T) {
	got := SafeLogText("failure\nforged\r\x1b[31m token=private-token Bearer private-bearer ock_private https://oss.test/a?Signature=private-signature")
	for _, value := range []string{"\n", "\r", "\x1b", "private-token", "private-bearer", "ock_private", "private-signature"} {
		if strings.Contains(got, value) {
			t.Fatalf("leaked %q in %s", value, got)
		}
	}
	if !strings.Contains(got, `\nforged\r\x1b`) {
		t.Fatal(got)
	}
}
