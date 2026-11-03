package daemon

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/data"
	"github.com/YoooClaw/cli/internal/relay"
	"net/http/httptest"
	"strings"
	"testing"
)

func dataHTTP(s *server, method, path, token string, b []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestDataHTTPIsolationRoundTrip(t *testing.T) {
	s, _ := newTestServer(t, "owner")
	s.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "a", Key: "key-a"}, {Label: "b", Key: "key-b"}, {Label: "default", Key: "key-default"}}}
	raw := []byte{0, 255, 1, 13, 10}
	w := dataHTTP(s, "POST", "/data?name=../../file.bin", "owner", raw)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result data.PutResult
	json.Unmarshal(w.Body.Bytes(), &result)
	id := result.Data.ID
	if result.Data.ClientLabel != "local" {
		t.Fatal(result)
	}
	for _, key := range []string{"key-a", "key-b", "key-default"} {
		for _, method := range []string{"GET", "DELETE"} {
			w = dataHTTP(s, method, "/data/"+id, key, nil)
			if w.Code != 404 {
				t.Fatal(key, method, w.Code)
			}
		}
		w = dataHTTP(s, "GET", "/data", key, nil)
		var p data.Page
		json.Unmarshal(w.Body.Bytes(), &p)
		if len(p.Items) != 0 {
			t.Fatal("leak", p)
		}
	}
	for _, method := range []string{"GET", "HEAD"} {
		w = dataHTTP(s, method, "/data/"+id+"/content", "owner", nil)
		if w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") != "sandbox" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w)
		}
		if method == "GET" && !bytes.Equal(w.Body.Bytes(), raw) {
			t.Fatal("roundtrip")
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("head body")
		}
	}
	w = dataHTTP(s, "POST", "/data", "key-a", []byte("a"))
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Data.ClientLabel != "a" {
		t.Fatal(result)
	}
	w = dataHTTP(s, "DELETE", "/data/"+result.Data.ID, "key-b", nil)
	if w.Code != 404 {
		t.Fatal("cross delete")
	}
	w = dataHTTP(s, "GET", "/data/"+result.Data.ID, "key-a", nil)
	if w.Code != 200 {
		t.Fatal("own read")
	}
}
func TestDataRelayLimitsAndErrors(t *testing.T) {
	s, _ := newTestServer(t, "owner")
	s.cfg.Data.MaxInlineBytes = 4
	s.cfg.Data.MaxObjectBytes = 5
	s.cfg.Data.QuotaBytes = 6
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/gateway/data.write", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer owner")
		r.Header.Set(relay.InternalHTTPHeader, "1")
		r.Header.Set(relay.InternalClientLabelHeader, "phone")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w := call(`{"contentBase64":"` + base64.StdEncoding.EncodeToString([]byte{255, 0, 1, 2}) + `","idempotencyKey":"retry"}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var a data.PutResult
	json.Unmarshal(w.Body.Bytes(), &a)
	if a.Data.ClientLabel != "phone" {
		t.Fatal(a)
	}
	w = call(`{"contentBase64":"/wABAg==","idempotencyKey":"retry"}`)
	var b data.PutResult
	json.Unmarshal(w.Body.Bytes(), &b)
	if w.Code != 201 || a.Data.ID != b.Data.ID {
		t.Fatal(w.Body.String())
	}
	for _, tt := range []struct {
		body string
		code int
	}{{`{}`, 400}, {`{"contentBase64":"?"}`, 400}, {`{"contentBase64":"YWJjZGU="}`, 413}, {`{"contentBase64":"YQ==","meta":[]}`, 400}, {`{"contentBase64":"YQ==","idempotencyKey":"retry"}`, 409}, {`{"contentBase64":"YWJj"}`, 413}} {
		w = call(tt.body)
		if w.Code != tt.code || !strings.Contains(w.Body.String(), "YOOOCLAW_DATA_") {
			t.Fatal(tt, w.Code, w.Body.String())
		}
	}
	w = dataHTTP(s, "POST", "/data", "owner", []byte("123456"))
	if w.Code != 413 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("GET", "/data", nil)
	r.Header.Set("Authorization", "Bearer owner")
	r.Header.Set(relay.InternalHTTPHeader, "1")
	r.Header.Set(relay.InternalClientLabelHeader, "phone")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal("relay read allowed")
	}
}
