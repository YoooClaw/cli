package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/assets"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/relay"
)

const reminderSnapshot = `{"schemaVersion":1,"sourceId":"phone","collectedAt":"2026-10-01T09:00:00+08:00","items":[{"id":"1","title":"Call","listId":"l","listName":"Personal","due":null,"isCompleted":false,"completedAt":null,"priority":0,"isRecurring":true,"notes":"private note","url":"https://example.com","createdAt":null,"modifiedAt":null},{"id":"2","title":"Call","listId":"l","listName":"Personal","due":{"date":"2026-10-02","time":null,"timeZone":null},"isCompleted":false,"completedAt":null,"priority":1,"isRecurring":false,"notes":null,"url":null,"createdAt":null,"modifiedAt":null}]}`

func TestRemindersHTTPUploadAndBridgeQueries(t *testing.T) {
	s, local := newTestServer(t, "gateway")
	s.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "phone", Key: "phone-key"}}}
	request := func(method, path, body, token string, internal bool) (int, []byte) {
		t.Helper()
		r, err := http.NewRequest(method, local.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		if internal {
			r.Header.Set(relay.InternalHTTPHeader, "1")
			r.Header.Set(relay.InternalClientLabelHeader, "phone")
		}
		response, err := local.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(path, "/assets/") && response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("private response cacheable")
		}
		return response.StatusCode, raw
	}
	if status, _ := request("POST", "/reminders", reminderSnapshot, "bad", false); status != 401 {
		t.Fatal(status)
	}
	if status, raw := request("POST", "/reminders", reminderSnapshot, "phone-key", false); status != 200 || !strings.Contains(string(raw), `"ingested":2`) {
		t.Fatal(status, string(raw))
	}
	// The existing Relay loopback context can use the same upload and read routes.
	if status, raw := request("POST", "/reminders", reminderSnapshot, "gateway", true); status != 200 || !strings.Contains(string(raw), `"applied":false`) {
		t.Fatal(status, string(raw))
	}
	status, raw := request("POST", "/assets/reminders?limit=1", `{"method":"GET","body":null}`, "phone-key", false)
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	var first assets.ReminderResponse
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.NextCursor == nil || strings.Contains(string(raw), "private note") {
		t.Fatal("summary paging failed")
	}
	status, raw = request("GET", "/assets/reminders?cursor="+url.QueryEscape(*first.NextCursor), "", "gateway", true)
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	var last assets.ReminderResponse
	if err := json.Unmarshal(raw, &last); err != nil {
		t.Fatal(err)
	}
	if len(last.Items) != 1 || last.NextCursor != nil || last.Items[0].RecordID == first.Items[0].RecordID {
		t.Fatal("paging lost duplicate-title reminders")
	}
	detailPath := "/assets/reminders/" + last.Items[0].RecordID + "?revision=" + first.Revision
	if status, raw := request("POST", detailPath, `{"method":"GET","body":null}`, "phone-key", false); status != 200 || !strings.Contains(string(raw), "private note") {
		t.Fatal(status, string(raw))
	}
	for _, path := range []string{"/assets/reminders", detailPath} {
		if status, _ := request("GET", path, "", "bad", false); status != 401 {
			t.Fatal("auth bypass", status)
		}
		if status, _ := request("POST", path, `{"method":"DELETE"}`, "phone-key", false); status != 400 {
			t.Fatal("write envelope accepted", status)
		}
	}
	if status, _ := request("POST", "/reminders", strings.Replace(reminderSnapshot, `"items":[`, `"items":null,"ignored":[`, 1), "phone-key", false); status != 400 {
		t.Fatal("invalid full snapshot accepted")
	}
	status, raw = request("GET", "/health", "", "", false)
	if status != 200 || !strings.Contains(string(raw), `"reminders"`) || !strings.Contains(string(raw), `"assets.reminders.read.v1"`) {
		t.Fatal("capabilities missing")
	}
}
