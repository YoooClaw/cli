package daemon

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/nanoshell"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/gorilla/websocket"
)

func publishNanoshell(t *testing.T, s *server) ([]byte, string) {
	t.Helper()
	build := func(comment string) []byte {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		z.SetComment(comment)
		for _, e := range []struct {
			n string
			b []byte
		}{
			{"game.nsp/manifest.json", []byte(`{"id":"com.game","name":"Game","version":1,"entry":"app.wasm"}`)},
			{"game.nsp/app.wasm", []byte{0, 97, 115, 109, 1, 0, 0, 0}},
			{"game.nsp/README-INSTALL.txt", bytes.Repeat([]byte("x"), 120*1024)},
		} {
			w, err := z.CreateHeader(&zip.FileHeader{Name: e.n, Method: zip.Store})
			if err != nil {
				t.Fatal(err)
			}
			w.Write(e.b)
		}
		if e := z.Close(); e != nil {
			t.Fatal(e)
		}
		return b.Bytes()
	}
	raw := build("")
	raw = build(strings.Repeat("x", nanoshell.MaxPackageBytes-len(raw)))
	p := filepath.Join(t.TempDir(), "game.zip")
	os.WriteFile(p, raw, 0600)
	result, err := (nanoshell.Store{Root: s.ctx.Paths.Nanoshell}).Publish(p, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	return raw, result.PackageID
}
func TestNanoshellGatewayScopeAndErrors(t *testing.T) {
	srv, ts := newTestServer(t, "")
	_, id := publishNanoshell(t, srv)
	for _, label := range []string{"phone-a", "phone-b", "default"} {
		res := asClient(t, "POST", ts.URL+"/gateway/nanoshell.apps.list", label, `{}`)
		items := gatewayData(t, res)["items"].([]any)
		if (len(items) == 1) != (label == "phone-a") {
			t.Fatal(label, res)
		}
		res = asClient(t, "POST", ts.URL+"/gateway/nanoshell.apps.download", label, fmt.Sprintf(`{"packageId":%q}`, id))
		if label == "phone-a" {
			gatewayData(t, res)
		} else if res["ok"] != false || res["error"].(map[string]any)["code"] != "PACKAGE_NOT_FOUND" {
			t.Fatal(res)
		}
	}
	for _, body := range []string{`{"limit":0}`, `{"limit":101}`, `{"limit":"bad"}`, `{"limit":1.5}`, `{"cursor":"bad"}`, `[]`, `null`, `{"limit":`} {
		res := asClient(t, "POST", ts.URL+"/gateway/nanoshell.apps.list", "phone-a", body)
		if res["ok"] != false {
			t.Fatal(body, res)
		}
	}
}
func TestNanoshellWebSocketWholePackage(t *testing.T) {
	srv, local := newTestServer(t, "gateway-token")
	raw, id := publishNanoshell(t, srv)
	done := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, e := upgrader.Upgrade(w, r, nil)
		if e != nil {
			done <- e
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		rpc := func(method string, params any) (map[string]any, error) {
			if e := conn.WriteJSON(map[string]any{"type": "req", "id": method, "method": method, "params": params}); e != nil {
				return nil, e
			}
			for {
				_, b, e := conn.ReadMessage()
				if e != nil {
					return nil, e
				}
				if string(b) == "ping" {
					conn.WriteMessage(websocket.TextMessage, []byte("pong"))
					continue
				}
				var res map[string]any
				if e = json.Unmarshal(b, &res); e != nil {
					return nil, e
				}
				if res["type"] != "res" || res["id"] != method || res["ok"] != true {
					return nil, fmt.Errorf("unexpected response: %v", res)
				}
				p, ok := res["payload"].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("missing payload")
				}
				return p, nil
			}
		}
		list, e := rpc("nanoshell.apps.list", map[string]any{})
		if e != nil {
			done <- e
			return
		}
		items, ok := list["items"].([]any)
		if !ok || len(items) != 1 {
			done <- fmt.Errorf("bad list: %v", list)
			return
		}
		pkg, e := rpc("nanoshell.apps.download", map[string]any{"packageId": id})
		if e != nil {
			done <- e
			return
		}
		data, ok := pkg["data"].(string)
		if !ok {
			done <- fmt.Errorf("no base64 data")
			return
		}
		decoded, e := base64.StdEncoding.DecodeString(data)
		if e != nil || !bytes.Equal(decoded, raw) || pkg["packageId"] != id || pkg["size"] != float64(nanoshell.MaxPackageBytes) {
			done <- fmt.Errorf("whole package mismatch")
			return
		}
		done <- nil
	}))
	defer remote.Close()
	c := relay.NewClient(relay.ClientOptions{TunnelURL: "ws" + strings.TrimPrefix(remote.URL, "http"), CredentialProvider: func() (relay.Credential, error) {
		return relay.Credential{Query: map[string]string{"apiKey": "test"}}, nil
	}, HeartbeatSec: 60, Logger: srv.logger})
	d := relay.NewDispatcher(relay.DispatcherOptions{Client: c, HTTPBaseURL: local.URL, HTTPToken: "gateway-token", ClientLabel: "phone-a", Logger: srv.logger})
	d.Start()
	c.Start()
	defer c.Stop("test finished")
	defer d.Cleanup()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("websocket timeout")
	}
}
