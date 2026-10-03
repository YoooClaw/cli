package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/paths"
)

type seenRequest struct {
	Method        string
	URI           string
	Authorization string
	ContentType   string
	Body          string
}

func registerLocalService(t *testing.T, name, baseURL string) {
	t.Helper()
	dir := paths.LocalServicesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"baseUrl": baseURL, "token": "svc-token"})
	if err := os.WriteFile(filepath.Join(dir, name+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func postEnvelope(t *testing.T, url, envelope string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(envelope))
	req.Header.Set("Content-Type", "application/json")
	// Relay 回环带的是 daemon 自己的令牌，不能透传给本机服务。
	req.Header.Set("Authorization", "Bearer gw")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(raw)
}

func TestLocalServiceForwardsEnvelope(t *testing.T) {
	_, ts := newTestServer(t, "gw")
	var seen []seenRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, seenRequest{r.Method, r.RequestURI, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(body)})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":1}`))
	}))
	t.Cleanup(upstream.Close)
	registerLocalService(t, "clawpilot-thread", upstream.URL)

	status, ct, body := postEnvelope(t, ts.URL+"/local-services/clawpilot-thread/api/threads?scope=all", `{"method":"GET"}`)
	if status != 201 || ct != "application/json; charset=utf-8" || body != `{"ok":1}` {
		t.Fatalf("GET 转发结果 = %d %q %q", status, ct, body)
	}
	status, _, _ = postEnvelope(t, ts.URL+"/local-services/clawpilot-thread/api/commands", `{"method":"POST","body":"{\"type\":\"seen\"}"}`)
	if status != 201 {
		t.Fatalf("POST 转发状态 = %d", status)
	}

	want := []seenRequest{
		{Method: "GET", URI: "/api/threads?scope=all", Authorization: "Bearer svc-token"},
		{Method: "POST", URI: "/api/commands", Authorization: "Bearer svc-token", ContentType: "application/json", Body: `{"type":"seen"}`},
	}
	if len(seen) != len(want) {
		t.Fatalf("upstream 收到 %d 个请求，want %d", len(seen), len(want))
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("请求 %d = %+v, want %+v", i, seen[i], want[i])
		}
	}
}

func TestLocalServiceRejectsUnregisteredAndNonLoopback(t *testing.T) {
	_, ts := newTestServer(t, "gw")
	registerLocalService(t, "remote", "http://example.com:80")

	for _, path := range []string{"/local-services/missing/api", "/local-services/remote/api"} {
		if status, _, _ := postEnvelope(t, ts.URL+path, `{"method":"GET"}`); status != 503 {
			t.Errorf("%s status = %d, want 503", path, status)
		}
	}
	if status, _, _ := postEnvelope(t, ts.URL+"/local-services/../x", `{"method":"GET"}`); status != 404 {
		t.Errorf("非法服务名 status = %d, want 404", status)
	}
}
