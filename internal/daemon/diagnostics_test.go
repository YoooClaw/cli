package daemon

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/creds"
)

func TestCredentialSummaryRotationAndRedaction(t *testing.T) {
	set := creds.CredentialSet{Mode: "file-multi", Location: "/tmp/credentials.json", Entries: []creds.ApiKeyEntry{{Label: "phone", Key: "private-old-key", Source: "file", Default: true}}}
	before, _ := json.Marshal(credentialSummary(set))
	set.Entries[0].Key = "private-new-key"
	after, _ := json.Marshal(credentialSummary(set))
	if string(before) == string(after) {
		t.Fatal("key rotation not visible")
	}
	for _, raw := range [][]byte{before, after} {
		if strings.Contains(string(raw), "private-") {
			t.Fatalf("key leaked: %s", raw)
		}
		if !strings.Contains(string(raw), "sha256:") || !strings.Contains(string(raw), "phone") {
			t.Fatalf("missing metadata: %s", raw)
		}
	}
}

func TestStateDiagnosticReasons(t *testing.T) {
	p := sandboxPaths(t)
	if state := State(p); state.Reason != "lock_missing" {
		t.Fatalf("missing: %+v", state)
	}
	if err := os.WriteFile(p.DaemonLock, []byte("invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	if state := State(p); state.Reason != "lock_unreadable" {
		t.Fatalf("unreadable: %+v", state)
	}
	if err := WriteLock(p, Lock{PID: 2147483600}); err != nil {
		t.Fatal(err)
	}
	if state := State(p); state.Reason != "process_not_alive" || !state.Stale {
		t.Fatalf("dead: %+v", state)
	}
}

func TestReloadLogsAppliedCredentialsWithoutSecrets(t *testing.T) {
	srv, _ := newTestServer(t, "")
	srv.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "old", Key: "old-private-key"}}}
	srv.reloadCredentials()
	raw, err := os.ReadFile(srv.logger.logFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"credentials.reload_begin", "credentials.reload_applied", "connectionConfirmed", "old"} {
		if !strings.Contains(string(raw), event) {
			t.Fatalf("missing %s: %s", event, raw)
		}
	}
	if strings.Contains(string(raw), "old-private-key") {
		t.Fatalf("credential leaked: %s", raw)
	}
}
