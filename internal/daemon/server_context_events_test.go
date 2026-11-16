package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/contextevent"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/relay"
	"github.com/gorilla/websocket"
)

func contextRequest(t *testing.T, s *server, method, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func eventPage(t *testing.T, s *server, key string) contextevent.Page {
	t.Helper()
	w := contextRequest(t, s, "GET", "/context/events", "", key)
	var p contextevent.Page
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return p
}
func TestContextEventSourcesAndCommittedData(t *testing.T) {
	s, _ := newTestServer(t, "gateway")
	s.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "phone", Key: "phone-key"}}}
	s.recordingStorage = recording.NewStorage(s.ctx.Paths.Recordings, s.logger)
	if e := s.recordingStorage.Init(); e != nil {
		t.Fatal(e)
	}
	cases := []struct{ source, path, body string }{
		{"notifications", "/notifications", `{"notifications":[{"id":"ctx-event","app":"test","title":"private","body":"secret","timestamp":"2026-10-20T00:00:00Z"}]}`},
		{"locations", "/locations", `{"items":[{"longitude":0,"latitude":0}]}`},
		{"calendars", "/calendars", calendarSnapshot},
		{"reminders", "/reminders", reminderSnapshot},
		{"health-data", "/health-data", healthBatch},
		{"web-pages", "/web-pages", `{"canonicalUrl":"https://example.com/context","title":"Title","markdown":"private body"}`},
		{"recordings", "/gateway/recordings.result.write", `{"recordingId":"context-rec","summary":{"markdown":"private summary"}}`},
		{"data", "/gateway/data.write", `{"name":"note.txt","contentBase64":"aGVsbG8="}`},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			before := len(eventPage(t, s, "gateway").Events)
			w := contextRequest(t, s, "POST", tc.path, tc.body, "phone-key")
			if w.Code >= 300 {
				t.Fatal(w.Code, w.Body.String())
			}
			p := eventPage(t, s, "gateway")
			if len(p.Events) != before+1 || p.Events[before].Source != tc.source {
				t.Fatal(p)
			}
			raw, _ := json.Marshal(p.Events[before])
			if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "secret") {
				t.Fatal("body leaked")
			}
		})
	}
	// A duplicate notification through the gateway alias produces no extra event.
	before := len(eventPage(t, s, "gateway").Events)
	w := contextRequest(t, s, "POST", "/gateway/notifications.push", cases[0].body, "phone-key")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if got := len(eventPage(t, s, "gateway").Events); got != before {
		t.Fatal("duplicate notification event")
	}
	for _, tc := range []struct{ path, body string }{{"/calendars", calendarSnapshot}, {"/health-data", healthBatch}, {"/reminders", reminderSnapshot}, {"/gateway/recordings.result.write", cases[6].body}} {
		w := contextRequest(t, s, "POST", tc.path, tc.body, "phone-key")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if got := len(eventPage(t, s, "gateway").Events); got != before {
		t.Fatal("identical snapshot event", got, before)
	}
	// Empty applied calendar snapshots must still invalidate cached data.
	body := strings.Replace(calendarSnapshot, `"collectedAt":"2026-10-16T12:00:00+08:00"`, `"collectedAt":"2026-10-16T13:00:00+08:00"`, 1)
	var obj map[string]any
	json.Unmarshal([]byte(body), &obj)
	obj["items"] = []any{}
	raw, _ := json.Marshal(obj)
	w = contextRequest(t, s, "POST", "/calendars", string(raw), "phone-key")
	if w.Code != 200 || len(eventPage(t, s, "gateway").Events) != before+1 {
		t.Fatal(w.Body.String())
	}
	// Existing data can actually be read after the event.
	for _, ev := range eventPage(t, s, "gateway").Events {
		if ev.Source == "data" {
			if _, e := s.dataStore().Show(ev.ResourceIDs[0], "phone"); e != nil {
				t.Fatal(e)
			}
		}
	}
}
func TestContextEventsSSEReplayLiveAndRevocation(t *testing.T) {
	s, ts := newTestServer(t, "gateway")
	s.credentialSet = creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "a", Key: "a-key"}, {Label: "b", Key: "b-key"}}}
	s.publishContext("data", "append", "a", 1, false, "objects", "one")
	p := eventPage(t, s, "a-key")
	cursor := p.HighWatermark
	s.publishContext("data", "append", "b", 1, false, "objects", "hidden")
	s.publishContext("data", "append", "a", 1, false, "objects", "two")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/context/events/stream?after="+cursor, nil)
	req.Header.Set("Authorization", "Bearer a-key")
	resp, e := ts.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	next := func() contextevent.Event {
		t.Helper()
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var ev contextevent.Event
				if e := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); e != nil {
					t.Fatal(e)
				}
				return ev
			}
		}
		t.Fatal("stream ended", scanner.Err())
		return contextevent.Event{}
	}
	if ev := next(); len(ev.ResourceIDs) != 1 || ev.ResourceIDs[0] != "two" {
		t.Fatal(ev)
	}
	s.publishContext("data", "append", "a", 1, false, "objects", "three")
	if ev := next(); ev.ResourceIDs[0] != "three" {
		t.Fatal(ev)
	}
	if s.activeRequests.Load() != 0 {
		t.Fatal("SSE blocks update idle")
	}
	s.shareMu.Lock()
	s.credentialSet.Entries = s.credentialSet.Entries[1:]
	s.shareMu.Unlock()
	for scanner.Scan() {
	}
	if ctx.Err() != nil {
		t.Fatal("revoked stream did not close")
	}
	w := contextRequest(t, s, "GET", "/context/events", "", "a-key")
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	if p := eventPage(t, s, "b-key"); len(p.Events) != 1 || p.Events[0].ResourceIDs[0] != "hidden" {
		t.Fatal(p)
	}
}
func TestContextEventsPartialHealthAndJournalFailure(t *testing.T) {
	s, _ := newTestServer(t, "")
	if e := os.MkdirAll(filepath.Join(s.ctx.Paths.HealthData, "2026-10-20.jsonl"), 0700); e != nil {
		t.Fatal(e)
	}
	var batch map[string]any
	json.Unmarshal([]byte(healthBatch), &batch)
	days := batch["days"].([]any)
	second := map[string]any{"date": "2026-10-20", "sleepSessions": []any{}, "items": []any{map[string]any{"metric": "steps", "name": "Steps", "value": 1, "unit": "steps"}}}
	batch["days"] = append(days, second)
	raw, _ := json.Marshal(batch)
	w := contextRequest(t, s, "POST", "/health-data", string(raw), "")
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	p := eventPage(t, s, "")
	if len(p.Events) != 1 || !p.Events[0].Result.Partial || p.Events[0].Result.CommittedCount != 1 {
		t.Fatal(p)
	}
	if e := os.Remove(filepath.Join(s.ctx.Paths.State, "context-events", "state.json")); e != nil {
		t.Fatal(e)
	}
	os.Mkdir(filepath.Join(s.ctx.Paths.State, "context-events", "state.json"), 0700)
	w = contextRequest(t, s, "POST", "/data?name=ok.txt", "data survives", "")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestContextImageCompletion(t *testing.T) {
	s, _ := newTestServer(t, "")
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release; io.WriteString(w, "image bytes") }))
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		imageServer.Close()
	}()
	payload := fmt.Sprintf(`{"imageId":"ctx-image","image":{"oss_image_url":%q,"created_at":"2026-10-20T00:00:00Z","mime_type":"image/jpeg"}}`, imageServer.URL)
	w := contextRequest(t, s, "POST", "/images", payload, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if p := eventPage(t, s, ""); len(p.Events) != 1 {
		t.Fatal(p)
	} // committed metadata, not downloaded file
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p := eventPage(t, s, "")
		if len(p.Events) == 2 {
			if _, e := os.Stat(filepath.Join(s.ctx.Paths.Images, "files", "ctx-image.jpg")); e != nil {
				t.Fatal(e)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("missing download commit event")
}

func TestContextEventProxyDeliveryAndSharedNoBroadcast(t *testing.T) {
	s, _ := newTestServer(t, "")
	frames := make(chan map[string]json.RawMessage, 4)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer callback-key" {
			t.Error("missing callback auth")
		}
		var frame map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&frame)
		frames <- frame
		w.WriteHeader(204)
	}))
	defer callback.Close()
	s.egress = NewProxyEgress(callback.URL, "callback-key", s.logger)
	s.publishContext("data", "append", "phone", 1, false, "objects", "o")
	select {
	case frame := <-frames:
		if string(frame["event"]) != `"context.updated"` || string(frame["clientLabel"]) != `"phone"` {
			t.Fatal(frame)
		}
		var ev contextevent.Event
		json.Unmarshal(frame["payload"], &ev)
		p := eventPage(t, s, "")
		if len(p.Events) != 1 || p.Events[0].EventID != ev.EventID {
			t.Fatal("delivery preceded journal")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("missing proxy event")
	}
	s.publishContext("calendars", "replace", "", 1, false, "days")
	if len(eventPage(t, s, "").Events) != 2 {
		t.Fatal("missing shared event")
	}
	select {
	case f := <-frames:
		t.Fatal("shared broadcast", f)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestContextEventsStandaloneRelayRoundTrip(t *testing.T) {
	s, local := newTestServer(t, "gateway")
	done := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, e := upgrader.Upgrade(w, r, nil)
		if e != nil {
			done <- e
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if e = conn.WriteJSON(relay.Frame{"type": "request", "id": "context-upload", "method": "POST", "path": "gateway/data.write", "body": `{"name":"relay.txt","contentBase64":"aGVsbG8="}`}); e != nil {
			done <- e
			return
		}
		response, event := false, false
		for !response || !event {
			_, raw, e := conn.ReadMessage()
			if e != nil {
				done <- e
				return
			}
			if string(raw) == "ping" {
				conn.WriteMessage(websocket.TextMessage, []byte("pong"))
				continue
			}
			var frame struct {
				Type    string             `json:"type"`
				Event   string             `json:"event"`
				ID      string             `json:"id"`
				Status  int                `json:"status"`
				Payload contextevent.Event `json:"payload"`
			}
			if e = json.Unmarshal(raw, &frame); e != nil {
				done <- e
				return
			}
			if frame.Event == contextevent.Name {
				if frame.Payload.Source != "data" || frame.Payload.Scope.ClientLabel != "phone" {
					done <- fmt.Errorf("wrong event %+v", frame)
					return
				}
				event = true
			}
			if frame.ID == "context-upload" {
				if frame.Status != 201 {
					done <- fmt.Errorf("write status %d", frame.Status)
					return
				}
				response = true
			}
		}
		done <- nil
	}))
	defer endpoint.Close()
	sup := relay.NewSupervisor(relay.SupervisorOptions{TunnelURL: "ws" + strings.TrimPrefix(endpoint.URL, "http"), HTTPBaseURL: local.URL, HTTPToken: "gateway", StateDir: t.TempDir(), Logger: s.logger})
	s.egress = NewRelayEgress(sup)
	sup.Apply(creds.CredentialSet{Entries: []creds.ApiKeyEntry{{Label: "phone", Key: "test-key"}}})
	defer sup.StopAll("done")
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("relay timed out")
	}
}
