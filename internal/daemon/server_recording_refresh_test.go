package daemon

import (
	"context"

	"github.com/YoooClaw/cli/internal/creds"
	"testing"
)

func TestRecordingRefresherRejectsChangedCredentials(t *testing.T) {
	srv, _ := newTestServer(t, "")
	srv.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "phone-a", Key: "first"}}}
	refresh := srv.recordingURLRefresher("phone-a", "task")
	srv.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "phone-a", Key: "second"}}}
	if _, err := refresh(context.Background()); err == nil {
		t.Fatal("changed account accepted")
	}
	if _, err := srv.recordingURLRefresher("unknown", "task")(context.Background()); err == nil {
		t.Fatal("unknown owner accepted")
	}
	refresh = srv.recordingURLRefresher("phone-a", "task")
	t.Setenv("OPENCLAW_HOST_PRODUCTION", "changed.example.invalid")
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "production")
	if _, err := refresh(context.Background()); err == nil {
		t.Fatal("changed environment accepted")
	}
}
