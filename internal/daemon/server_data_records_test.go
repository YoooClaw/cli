package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/data"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/gorilla/websocket"
)

func recordCall(t *testing.T, s *server, label, method string, body any, envelope bool) *httptest.ResponseRecorder {
	t.Helper()
	b, e := json.Marshal(body)
	if e != nil {
		t.Fatal(e)
	}
	if envelope {
		b, e = json.Marshal(map[string]any{"method": "POST", "body": string(b)})
		if e != nil {
			t.Fatal(e)
		}
	}
	r := httptest.NewRequest("POST", "/gateway/"+method, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer owner")
	r.Header.Set(relay.InternalHTTPHeader, "1")
	r.Header.Set(relay.InternalClientLabelHeader, label)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func recordData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code < 200 || w.Code >= 300 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable response")
	}
	var body struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil || !body.OK {
		t.Fatal(e, w.Body.String())
	}
	return body.Data
}
func TestDataRecordsHistoryPaginationScopeAndEnvelope(t *testing.T) {
	s, _ := newTestServer(t, "owner")
	s.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "a", Key: "key-a"}, {Label: "b", Key: "key-b"}}}
	caps := recordData(t, recordCall(t, s, "a", "data.capabilities", map[string]any{}, true))
	if caps["maxChunkBytes"] != float64(data.ChunkBytes) || caps["scope"] != "client" {
		t.Fatal(caps)
	}
	var ids []string
	for i := 0; i < 3; i++ {
		body := map[string]any{"contentBase64": base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("记录\n%d", i))), "source": "harness-records", "idempotencyKey": fmt.Sprint(i), "meta": map[string]any{"clientMessageId": fmt.Sprint(i)}}
		obj := recordData(t, recordCall(t, s, "a", "data.write", body, true))
		if _, ok := obj["blobPath"]; ok {
			t.Fatal("local path leak")
		}
		ids = append(ids, obj["id"].(string))
	}
	for _, label := range []string{"b", "default", "local"} {
		page := recordData(t, recordCall(t, s, label, "data.list", map[string]any{"source": "harness-records"}, true))
		if len(page["items"].([]any)) != 0 {
			t.Fatal("cross-label list", label, page)
		}
		for _, method := range []string{"data.get", "data.content"} {
			if w := recordCall(t, s, label, method, map[string]any{"id": ids[0]}, false); w.Code != 404 {
				t.Fatal("cross-label content", w.Body.String())
			}
		}
		result := recordData(t, recordCall(t, s, label, "data.resolve", map[string]any{"idempotencyKey": "0"}, false))
		if result["object"] != nil {
			t.Fatal("cross-label receipt")
		}
	}
	cursor := ""
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		p := recordData(t, recordCall(t, s, "a", "data.list", map[string]any{"source": "harness-records", "limit": 1, "cursor": cursor}, true))
		items := p["items"].([]any)
		if len(items) != 1 {
			t.Fatal(p)
		}
		id := items[0].(map[string]any)["id"].(string)
		if seen[id] {
			t.Fatal("duplicate page")
		}
		seen[id] = true
		cursor = p["nextCursor"].(string)
	}
	if cursor != "" {
		t.Fatal("missing terminal cursor")
	}
	got := recordData(t, recordCall(t, s, "a", "data.get", map[string]any{"id": ids[0]}, true))
	if got["id"] != ids[0] {
		t.Fatal(got)
	}
	chunk := recordData(t, recordCall(t, s, "a", "data.content", map[string]any{"id": ids[0]}, true))
	b, e := base64.StdEncoding.DecodeString(chunk["contentBase64"].(string))
	if e != nil || string(b) != "记录\n0" || chunk["eof"] != true {
		t.Fatal(chunk, e)
	}
	found := recordData(t, recordCall(t, s, "a", "data.resolve", map[string]any{"idempotencyKey": "0"}, true))
	if found["object"].(map[string]any)["id"] != ids[0] {
		t.Fatal(found)
	}
	for _, body := range []any{map[string]any{"client": "b"}, map[string]any{"limit": 51}, map[string]any{"limit": -1}, map[string]any{"method": "DELETE", "body": "{}"}, nil} {
		if w := recordCall(t, s, "a", "data.list", body, false); w.Code != 400 {
			t.Fatal("invalid body", body, w.Code, w.Body.String())
		}
	}
	// Direct API-key authentication uses the same isolated methods.
	w := dataHTTP(s, "POST", "/gateway/data.get", "key-a", []byte(`{"id":"`+ids[0]+`"}`))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = dataHTTP(s, "POST", "/gateway/data.get", "key-b", []byte(`{"id":"`+ids[0]+`"}`)); w.Code != 404 {
		t.Fatal(w.Body.String())
	}
}
func TestDataRecordsUploadAndResolve(t *testing.T) {
	s, _ := newTestServer(t, "owner")
	s.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "a", Key: "key-a"}}}
	raw := []byte{0, 255, 1, 10}
	sum := sha256.Sum256(raw)
	begin := map[string]any{"name": "文件.any", "idempotencyKey": "one", "sizeBytes": len(raw), "sha256": hex.EncodeToString(sum[:])}
	st := recordData(t, recordCall(t, s, "a", "data.upload.begin", begin, true))
	id := st["uploadId"].(string)
	for _, method := range []string{"data.upload.status", "data.upload.commit", "data.upload.abort", "data.upload.chunk"} {
		w := recordCall(t, s, "other", method, map[string]any{"uploadId": id, "contentBase64": "AA=="}, true)
		if w.Code != 404 {
			t.Fatal(method, w.Code, w.Body.String())
		}
	}
	resolved := recordData(t, recordCall(t, s, "a", "data.resolve", map[string]any{"idempotencyKey": "one"}, true))
	if resolved["upload"].(map[string]any)["uploadId"] != id {
		t.Fatal(resolved)
	}
	w := recordCall(t, s, "a", "data.upload.chunk", map[string]any{"uploadId": id, "contentBase64": "?"}, true)
	if w.Code != 400 {
		t.Fatal(w.Body.String())
	}
	st = recordData(t, recordCall(t, s, "a", "data.upload.chunk", map[string]any{"uploadId": id, "offset": 0, "contentBase64": base64.StdEncoding.EncodeToString(raw)}, true))
	if st["offset"] != float64(len(raw)) {
		t.Fatal(st)
	}
	st = recordData(t, recordCall(t, s, "a", "data.upload.status", map[string]any{"uploadId": id}, true))
	if st["state"] != "uploading" {
		t.Fatal(st)
	}
	st = recordData(t, recordCall(t, s, "a", "data.upload.commit", map[string]any{"uploadId": id}, true))
	if st["state"] != "succeeded" {
		t.Fatal(st)
	}
	chunk := recordData(t, recordCall(t, s, "a", "data.content", map[string]any{"id": st["objectId"]}, true))
	if chunk["contentBase64"] != base64.StdEncoding.EncodeToString(raw) {
		t.Fatal(chunk)
	}
	// Key rotation must not resume a previously authorized pending upload.
	begin["idempotencyKey"] = "rotated"
	st = recordData(t, recordCall(t, s, "a", "data.upload.begin", begin, true))
	s.credentialSet.Entries[0].Key = "new-key"
	if w = recordCall(t, s, "a", "data.upload.status", map[string]any{"uploadId": st["uploadId"]}, true); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// Exercise actual WS dispatcher -> HTTP auth -> daemon with arbitrary binary
// content. Both RPC and messageBridge frames must preserve the full JSON result.
func TestDataRecordsWebSocketRoundTrip(t *testing.T) {
	s, local := newTestServer(t, "owner")
	s.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "a", Key: "key-a"}}}
	done := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := upgrader.Upgrade(w, r, nil)
		if e != nil {
			done <- e
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
		run := func() error {
			call := func(method string, body any, rpc bool) (map[string]any, error) {
				var frame map[string]any
				if rpc {
					frame = map[string]any{"type": "req", "id": "test", "method": method, "params": body}
				} else {
					raw, _ := json.Marshal(body)
					envelope, _ := json.Marshal(map[string]any{"method": "POST", "body": string(raw)})
					frame = map[string]any{"type": "request", "id": "test", "method": "POST", "path": "/gateway/" + method, "body": string(envelope)}
				}
				if e := c.WriteJSON(frame); e != nil {
					return nil, e
				}
				for {
					_, raw, e := c.ReadMessage()
					if e != nil {
						return nil, e
					}
					if string(raw) == "ping" {
						_ = c.WriteMessage(websocket.TextMessage, []byte("pong"))
						continue
					}
					var res map[string]any
					if e = json.Unmarshal(raw, &res); e != nil {
						return nil, e
					}
					var result map[string]any
					if rpc {
						if res["ok"] != true {
							return nil, fmt.Errorf("RPC failed: %s", raw)
						}
						result = res["payload"].(map[string]any)
					} else {
						if res["status"].(float64) >= 300 {
							return nil, fmt.Errorf("HTTP failed: %s", raw)
						}
						if e = json.Unmarshal([]byte(res["body"].(string)), &result); e != nil {
							return nil, e
						}
					}
					if result["ok"] != true {
						return nil, fmt.Errorf("bad result: %v", result)
					}
					return result["data"].(map[string]any), nil
				}
			}
			raw := bytes.Repeat([]byte{255, 0, 128, 1, 13, 10}, data.ChunkBytes/3)
			sum := sha256.Sum256(raw)
			st, e := call("data.upload.begin", map[string]any{"idempotencyKey": "ws", "sizeBytes": len(raw), "sha256": hex.EncodeToString(sum[:]), "name": "sample.zip"}, false)
			if e != nil {
				return e
			}
			id := st["uploadId"]
			for off := 0; off < len(raw); off += data.ChunkBytes {
				end := min(off+data.ChunkBytes, len(raw))
				if _, e = call("data.upload.chunk", map[string]any{"uploadId": id, "offset": off, "contentBase64": base64.StdEncoding.EncodeToString(raw[off:end])}, false); e != nil {
					return e
				}
			}
			st, e = call("data.upload.commit", map[string]any{"uploadId": id}, true)
			if e != nil {
				return e
			}
			var got []byte
			for off := int64(0); ; {
				chunk, e := call("data.content", map[string]any{"id": st["objectId"], "offset": off}, true)
				if e != nil {
					return e
				}
				part, e := base64.StdEncoding.DecodeString(chunk["contentBase64"].(string))
				if e != nil {
					return e
				}
				got = append(got, part...)
				if chunk["eof"] == true {
					break
				}
				off = int64(chunk["nextOffset"].(float64))
			}
			if !bytes.Equal(raw, got) {
				return fmt.Errorf("binary roundtrip failed")
			}
			return nil
		}
		done <- run()
	}))
	defer fake.Close()
	c := relay.NewClient(relay.ClientOptions{TunnelURL: "ws" + strings.TrimPrefix(fake.URL, "http"), CredentialProvider: func() (relay.Credential, error) { return relay.Credential{}, nil }, Logger: s.logger})
	d := relay.NewDispatcher(relay.DispatcherOptions{Client: c, HTTPBaseURL: local.URL, HTTPToken: "owner", ClientLabel: "a", Logger: s.logger})
	d.Start()
	c.Start()
	defer c.Stop("test complete")
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Relay test timed out")
	}
}
