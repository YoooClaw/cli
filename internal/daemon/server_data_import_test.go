package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/data"
	"github.com/YoooClaw/cli/internal/relay"
)

func TestDataImportHTTPAndRelayLifecycle(t *testing.T) {
	s, _ := newTestServer(t, "owner")
	s.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "a", Key: "key-a"}, {Label: "b", Key: "key-b"}}}
	raw := []byte{0, 255, 7, 8}
	sum := sha256.Sum256(raw)
	release := make(chan struct{})
	service, e := data.NewImports(s.dataStore(), data.ImportDependencies{Download: func(ctx context.Context, u, key, dest, cp string, n int64) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return os.WriteFile(dest, raw, 0600)
	}})
	if e != nil {
		t.Fatal(e)
	}
	s.dataImports = service
	defer service.Stop()
	body := map[string]any{"url": "https://bucket.oss-cn-hangzhou.aliyuncs.com/audio?Signature=secret", "sizeBytes": 4, "sha256": hex.EncodeToString(sum[:]), "idempotencyKey": "once", "name": "audio.bin"}
	b, _ := json.Marshal(body)
	w := dataHTTP(s, "POST", "/data/import", "key-a", b)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct {
		OK   bool              `json:"ok"`
		Data data.ImportStatus `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &response)
	id := response.Data.TaskID
	found, resolveErr := service.Resolve("once", "a")
	if resolveErr != nil || found == nil || found.TaskID != id {
		t.Fatal(found, resolveErr)
	}
	other, resolveErr := service.Resolve("once", "b")
	if resolveErr != nil || other != nil {
		t.Fatal(other, resolveErr)
	}

	if id == "" || response.Data.State != "queued" {
		t.Fatal(response)
	}
	if !s.activityPayload()["busy"].(bool) || s.updateIdle(time.Now().Add(time.Hour)) {
		t.Fatal("pending import not busy")
	}
	for _, path := range []string{"/data/imports/" + id, "/data/imports/" + id + "/retry"} {
		method := "GET"
		if strings.HasSuffix(path, "retry") {
			method = "POST"
		}
		w = dataHTTP(s, method, path, "key-b", []byte(`{}`))
		if w.Code != 404 {
			t.Fatal("scope leak", w.Code, w.Body.String())
		}
	}
	callRelay := func(method string, payload []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/gateway/"+method, strings.NewReader(string(payload)))
		r.Header.Set("Authorization", "Bearer owner")
		r.Header.Set(relay.InternalHTTPHeader, "1")
		r.Header.Set(relay.InternalClientLabelHeader, "a")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w = callRelay("data.import", b)
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &response)
	if response.Data.TaskID != id {
		t.Fatal("duplicate task")
	}
	w = callRelay("data.import.status", []byte(`{"taskId":"`+id+`"}`))
	if w.Code != 200 || strings.Contains(w.Body.String(), "Signature") {
		t.Fatal(w.Body.String())
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w = dataHTTP(s, "GET", "/data/imports/"+id, "key-a", nil)
		json.Unmarshal(w.Body.Bytes(), &response)
		if response.Data.State == "succeeded" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if response.Data.State != "succeeded" {
		t.Fatal(response)
	}
	o, e := s.dataStore().Show(response.Data.ObjectID, "a")
	if e != nil || o.ClientLabel != "a" {
		t.Fatal(o, e)
	}
	if _, e = s.dataStore().Show(o.ID, "b"); e == nil {
		t.Fatal("object isolation")
	}
	w = callRelay("data.import.retry", []byte(`{"taskId":"`+id+`"}`))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestDataImportRejectsBadRequestsAndUnavailable(t *testing.T) {
	s, _ := newTestServer(t, "owner")
	w := dataHTTP(s, "POST", "/data/import", "owner", []byte(`{}`))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	if e := s.startDataImports(); e != nil {
		t.Fatal(e)
	}
	defer s.dataImports.Stop()
	for _, b := range []string{`{}`, `{"url":"http://localhost/x","sizeBytes":1,"sha256":"` + strings.Repeat("a", 64) + `"}`, strings.Repeat("x", 65537)} {
		w = dataHTTP(s, "POST", "/gateway/data.import", "owner", []byte(b))
		if w.Code != 400 && w.Code != 413 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w = dataHTTP(s, "POST", "/data/imports/missing/retry", "owner", []byte(`{}`))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	before := s.dataImportIdentity("local")
	s.credentialSet = creds.CredentialSet{DefaultEntry: &creds.ApiKeyEntry{Label: "a", Key: "new"}}
	if before == s.dataImportIdentity("local") {
		t.Fatal("local account not bound")
	}
	if s.dataImportIdentity("unknown") != "" {
		t.Fatal("unknown client")
	}
}
