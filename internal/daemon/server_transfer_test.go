package daemon

import (
	"net/http"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/relay"
	"github.com/YoooClaw/cli/internal/transfer"
)

func postTransfer(t *testing.T, url, token string, relayed bool) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/transfer", strings.NewReader(`{"action":"capabilities"}`))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if relayed {
		req.Header.Set(relay.InternalHTTPHeader, "1")
		req.Header.Set(relay.InternalClientLabelHeader, "phone-a")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// /transfer 能让 daemon 读取任意本地目录并写入存储：只接受本机 CLI，
// 经 Relay 隧道转进来的请求（即便带着 gateway token）一律拒绝。
func TestTransferEndpointIsLocalOnly(t *testing.T) {
	srv, ts := newTestServer(t, "tok")
	srv.transfer = &transfer.Service{Dir: t.TempDir()}
	if got := postTransfer(t, ts.URL, "tok", false); got != 200 {
		t.Fatalf("local CLI call = %d, want 200", got)
	}
	if got := postTransfer(t, ts.URL, "tok", true); got != 403 {
		t.Fatalf("relay-forwarded call = %d, want 403", got)
	}
	if got := postTransfer(t, ts.URL, "", false); got != 401 {
		t.Fatalf("unauthenticated call = %d, want 401", got)
	}
}
