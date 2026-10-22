package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/assets"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/gorilla/websocket"
)

func TestAssetsRelayHTTPRoundTripAndCrossFileCursor(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) { testAssetsRelayRoundTrip(t, method) })
	}
}

func testAssetsRelayRoundTrip(t *testing.T, method string) {
	t.Helper()
	s, local := newTestServer(t, "gateway-secret")
	seedAssetEndpoints(t, s)
	done := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	fakeRelay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, e := upgrader.Upgrade(w, r, nil)
		if e != nil {
			done <- e
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		request := func(id, path string) (assets.Response, error) {
			frame := relay.Frame{"type": "request", "id": id, "method": method, "path": path}
			if method == http.MethodPost {
				frame["body"] = `{"method":"GET","body":null}`
			}
			if e := conn.WriteJSON(frame); e != nil {
				return assets.Response{}, e
			}
			for {
				_, raw, e := conn.ReadMessage()
				if e != nil {
					return assets.Response{}, e
				}
				if string(raw) == "ping" {
					_ = conn.WriteMessage(websocket.TextMessage, []byte("pong"))
					continue
				}
				var response relay.ProxyResponseFrame
				if e := json.Unmarshal(raw, &response); e != nil {
					return assets.Response{}, e
				}
				if response.ID != id || response.Type != "proxy_response" || response.Status != 200 || len(response.Body) > assets.ResponseLimit ||
					response.Headers["cache-control"] != "no-store" {
					return assets.Response{}, fmt.Errorf("invalid Relay response: %+v", response)
				}
				var page assets.Response
				if e := json.Unmarshal([]byte(response.Body), &page); e != nil {
					return page, e
				}
				return page, nil
			}
		}
		path := "assets/notifications?limit=50"
		for i, want := range []int{50, 50, 25, 8} {
			page, e := request(fmt.Sprint(i), path)
			if e != nil {
				done <- e
				return
			}
			if len(page.Items) != want {
				done <- fmt.Errorf("Relay page %d: got %d want %d", i, len(page.Items), want)
				return
			}
			if i == 3 {
				if page.NextCursor != nil {
					done <- fmt.Errorf("last page did not end")
					return
				}
			} else {
				if page.NextCursor == nil {
					done <- fmt.Errorf("premature end")
					return
				}
				path = "assets/notifications?limit=50&cursor=" + url.QueryEscape(*page.NextCursor)
			}
		}
		for _, kind := range []string{"locations", "calendars", "health-data", "web-pages"} {
			path := "assets/" + kind + "?limit=1"
			if kind == "calendars" {
				path += "&anchorDate=2026-10-22"
			}
			page, e := request(kind, path)
			if e != nil {
				done <- e
				return
			}
			if len(page.Items) != 1 || page.NextCursor == nil {
				done <- fmt.Errorf("missing Relay category %s", kind)
				return
			}
		}
		done <- nil
	}))
	defer fakeRelay.Close()
	client := relay.NewClient(relay.ClientOptions{TunnelURL: "ws" + strings.TrimPrefix(fakeRelay.URL, "http"),
		CredentialProvider: func() (relay.Credential, error) { return relay.Credential{}, nil }, Logger: s.logger})
	// Uploads were labeled phone-a; Relay's different label must not filter assets.
	dispatcher := relay.NewDispatcher(relay.DispatcherOptions{Client: client, HTTPBaseURL: local.URL, HTTPToken: "gateway-secret", ClientLabel: "phone-b", Logger: s.logger})
	dispatcher.Start()
	client.Start()
	defer client.Stop("test complete")
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Relay request timed out")
	}
	logs, e := os.ReadFile(s.logger.logFile)
	if e != nil || !strings.Contains(string(logs), method+" assets/notifications?limit=50") ||
		!strings.Contains(string(logs), "assets.read kind=notifications status=200 items=8") {
		t.Fatal("missing transport or completed response logs")
	}
}
