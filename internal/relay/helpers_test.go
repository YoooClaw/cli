package relay

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestIntField(t *testing.T) {
	t.Parallel()
	if intField(Frame{"n": float64(42)}, "n", -1) != 42 {
		t.Error("float64 field")
	}
	if intField(Frame{"n": 7}, "n", -1) != 7 {
		t.Error("int field")
	}
	if intField(Frame{"n": "x"}, "n", 99) != 99 {
		t.Error("fallback on wrong type")
	}
	if intField(Frame{}, "missing", 5) != 5 {
		t.Error("fallback on missing")
	}
}

func TestStringField(t *testing.T) {
	t.Parallel()
	if stringField(Frame{"s": "hi"}, "s") != "hi" {
		t.Error("string field")
	}
	if stringField(Frame{"s": 1}, "s") != "" {
		t.Error("non-string -> empty")
	}
}

func TestHeadersFromFrame(t *testing.T) {
	t.Parallel()
	got := headersFromFrame(map[string]any{"A": "1", "B": 2, "C": "3"})
	if got["A"] != "1" || got["C"] != "3" {
		t.Errorf("string headers should survive: %+v", got)
	}
	if _, ok := got["B"]; ok {
		t.Error("non-string header should be dropped")
	}
	if len(headersFromFrame("not-a-map")) != 0 {
		t.Error("non-map -> empty")
	}
}

func TestFlattenHeaders(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Add("X-Test", "a")
	h.Add("X-Test", "b")
	flat := flattenHeaders(h)
	if flat["x-test"] != "a, b" {
		t.Errorf("flatten should lowercase + join: %+v", flat)
	}
}

func TestMapPath(t *testing.T) {
	t.Parallel()
	if mapPath("/api/message/messageBridge/send") != "/notifications" {
		t.Error("messageBridge should map to /notifications")
	}
	if mapPath("/foo") != "/foo" {
		t.Error("absolute path unchanged")
	}
	if mapPath("foo") != "/foo" {
		t.Error("relative path should get leading slash")
	}
}

func TestRequestBody(t *testing.T) {
	t.Parallel()
	if requestBody(http.MethodGet, "body") != nil {
		t.Error("GET should have nil body")
	}
	if requestBody(http.MethodHead, "body") != nil {
		t.Error("HEAD should have nil body")
	}
	if requestBody(http.MethodPost, "body") == nil {
		t.Error("POST should carry body")
	}
}

func TestRedactURL(t *testing.T) {
	t.Parallel()
	got := redactURL("wss://host/ws?apiKey=supersecretkey123")
	if strings.Contains(got, "supersecretkey123") {
		t.Errorf("apiKey should be redacted: %q", got)
	}
	if redactURL("://bad") == "" {
		t.Error("unparseable url returned as-is")
	}
}

func TestPreview(t *testing.T) {
	t.Parallel()
	if preview("short", 10) != "short" {
		t.Error("short unchanged")
	}
	if preview("0123456789", 4) != "0123..." {
		t.Error("long should be truncated")
	}
}

func TestMin(t *testing.T) {
	t.Parallel()
	if min(3, 5) != 3 || min(5, 3) != 3 || min(4, 4) != 4 {
		t.Error("min")
	}
}

func TestRelayCredential(t *testing.T) {
	t.Parallel()
	c := relayCredential("  Bearer my-key ")
	if c.Query["apiKey"] != "my-key" {
		t.Errorf("should strip Bearer + trim: %+v", c.Query)
	}
}

func TestDispatcherInternalHeaders(t *testing.T) {
	t.Parallel()
	client := NewClient(ClientOptions{TunnelURL: "ws://x", CredentialProvider: func() (Credential, error) { return Credential{}, nil }})
	d := NewDispatcher(DispatcherOptions{Client: client, HTTPBaseURL: "http://local", HTTPToken: "tok", ClientLabel: "phone-a", Logger: testLogger{t}})

	m := map[string]string{}
	d.addInternalHeaders(m)
	if m[InternalHTTPHeader] != "1" || m[InternalClientLabelHeader] != "phone-a" || m["Authorization"] != "Bearer tok" {
		t.Errorf("addInternalHeaders wrong: %+v", m)
	}

	h := http.Header{}
	d.addInternalHeadersMap(h)
	if h.Get(InternalHTTPHeader) != "1" || h.Get("Authorization") != "Bearer tok" {
		t.Errorf("addInternalHeadersMap wrong: %+v", h)
	}
}

func TestClientStatusAndReconnect(t *testing.T) {
	t.Parallel()
	c := NewClient(ClientOptions{
		TunnelURL:          "ws://example/ws",
		CredentialProvider: func() (Credential, error) { return Credential{}, nil },
		ReconnectBackoffMs: 100,
	})
	if c.IsConnected() {
		t.Error("new client should not be connected")
	}
	st := c.Status()
	if st.Connected || st.URL != "ws://example/ws" {
		t.Errorf("status: %+v", st)
	}
	c.setConnected()
	if !c.IsConnected() {
		t.Error("should be connected after setConnected")
	}
	c.setDisconnected("boom")
	if c.IsConnected() || c.Status().LastDisconnectReason != "boom" {
		t.Errorf("disconnect state wrong: %+v", c.Status())
	}
	// nextReconnectDelay 单调递增 attempt 且有上限
	d1 := c.nextReconnectDelay()
	d2 := c.nextReconnectDelay()
	if d1 <= 0 || d2 < d1 {
		t.Errorf("reconnect delays should grow: %v then %v", d1, d2)
	}
	if d2 > time.Minute {
		t.Errorf("delay should cap at 1m: %v", d2)
	}
}

func TestClientOnConnectedHandlers(t *testing.T) {
	t.Parallel()
	c := NewClient(ClientOptions{TunnelURL: "ws://x", CredentialProvider: func() (Credential, error) { return Credential{}, nil }})
	connected := make(chan struct{}, 1)
	disconnected := make(chan string, 1)
	c.OnConnected(func() { connected <- struct{}{} })
	c.OnDisconnected(func(reason string) { disconnected <- reason })
	c.emitConnected()
	c.emitDisconnected("bye")
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Error("OnConnected handler not invoked")
	}
	select {
	case r := <-disconnected:
		if r != "bye" {
			t.Errorf("disconnect reason = %q", r)
		}
	case <-time.After(time.Second):
		t.Error("OnDisconnected handler not invoked")
	}
}
