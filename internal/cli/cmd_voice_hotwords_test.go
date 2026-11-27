package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/creds"
)

// Exercise real Cobra, credential, host and HTTP handling against an isolated Janus endpoint.
func serveHotwords(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	sandbox(t)
	if _, err := creds.SetAPIKey("hotword-test-key", false); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/unauth/janus/asr/v1/words" || r.Header.Get("X-Api-Key-Id") != "hotword-test-key" {
			t.Errorf("unexpected hotword route or credentials")
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "test")
	t.Setenv("OPENCLAW_HOST_TEST", strings.TrimPrefix(srv.URL, "https://"))
	old := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = old })
}

func TestVoiceHotwordWrites(t *testing.T) {
	var writes []map[string]any
	serveHotwords(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected write method or content type")
		}
		var mutation map[string]any
		if err := json.NewDecoder(r.Body).Decode(&mutation); err != nil {
			t.Error(err)
		}
		writes = append(writes, mutation)
		w.WriteHeader(http.StatusNoContent)
	})
	for _, args := range [][]string{
		{"add", "星桥", "YoooClaw Capture"}, {"add", "轻", "--weight", "1"},
		{"add", "强", "--weight", "5"}, {"update", "星桥", "YoooClaw Capture", "--weight", "5"},
		{"update", "轻", "--weight", "1"}, {"delete", "星桥", "YoooClaw Capture"},
	} {
		out, code := execCLI(t, append([]string{"voice", "hotwords"}, args...)...)
		if code != 0 || decode(t, out)["ok"] != true {
			t.Fatalf("%v: %d %s", args, code, out)
		}
	}
	want := []map[string]any{
		{"action": "add", "text": "星桥", "weight": 4.0}, {"action": "add", "text": "YoooClaw Capture", "weight": 4.0},
		{"action": "add", "text": "轻", "weight": 1.0}, {"action": "add", "text": "强", "weight": 5.0},
		{"action": "update", "text": "星桥", "weight": 5.0}, {"action": "update", "text": "YoooClaw Capture", "weight": 5.0},
		{"action": "update", "text": "轻", "weight": 1.0},
		{"action": "delete", "text": "星桥"}, {"action": "delete", "text": "YoooClaw Capture"},
	}
	if !reflect.DeepEqual(writes, want) {
		t.Fatalf("unexpected Janus writes: %#v", writes)
	}
}

func TestVoiceHotwordRejectsInvalidInputsBeforeWriting(t *testing.T) {
	serveHotwords(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected HTTP request") })
	for _, args := range [][]string{
		{"add", "word", "--weight", "0"}, {"add", "word", "--weight", "6"}, {"add", "word", " "},
		{"update", "word"}, {"update", "word", "--weight", "0"}, {"update", "word", "--weight", "6"},
		{"update", "word", " ", "--weight", "3"},
		{"delete", "word", ""}, {"list", "--limit", "101"}, {"list", "--cursor", "-1"},
	} {
		out, code := execCLI(t, append([]string{"voice", "hotwords"}, args...)...)
		if code == 0 || decode(t, out)["ok"] != false {
			t.Fatalf("%v did not fail: %s", args, out)
		}
	}
	for _, action := range []string{"add", "update"} {
		root := newRootCmd()
		root.SetArgs([]string{"voice", "hotwords", action, "word", "--weight", "1.5"})
		if err := root.Execute(); err == nil {
			t.Fatalf("%s accepted fractional weight", action)
		}
	}
}

func TestVoiceHotwordUpdateContinuesAfterMissingWord(t *testing.T) {
	var requests []string
	serveHotwords(t, func(w http.ResponseWriter, r *http.Request) {
		var op struct{ Action, Text string }
		if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
			t.Error(err)
		}
		requests = append(requests, op.Action+"/"+op.Text)
		if op.Text == "missing" {
			http.Error(w, `{"detail":"word not found"}`, http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	out, code := execCLI(t, "voice", "hotwords", "update", "missing", "existing", "--weight", "3")
	var reply struct {
		Error struct {
			Code    string
			Results []struct{ Text, Status string }
		}
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatal(err)
	}
	if code == 0 || reply.Error.Code != "YOOOCLAW_NOT_FOUND" ||
		!reflect.DeepEqual(requests, []string{"update/missing", "update/existing"}) ||
		!reflect.DeepEqual(reply.Error.Results, []struct{ Text, Status string }{{"missing", "error"}, {"existing", "ok"}}) {
		t.Fatalf("code=%d requests=%v: %s", code, requests, out)
	}
}

func TestVoiceHotwordBatchReportsEachOutcome(t *testing.T) {
	var seen []string
	serveHotwords(t, func(w http.ResponseWriter, r *http.Request) {
		var payload struct{ Text string }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		seen = append(seen, payload.Text)
		switch payload.Text {
		case "conflict":
			w.WriteHeader(409)
			io.WriteString(w, `{"detail":"word conflict"}`)
		case "reject":
			w.WriteHeader(401)
			io.WriteString(w, `{"detail":"invalid API key"}`)
		default:
			w.WriteHeader(204)
		}
	})
	out, code := execCLI(t, "voice", "hotwords", "delete", "first", "conflict", "next", "reject", "skipped")
	if code == 0 || !reflect.DeepEqual(seen, []string{"first", "conflict", "next", "reject"}) {
		t.Fatalf("code=%d requests=%v: %s", code, seen, out)
	}
	var reply struct {
		Error struct {
			HTTPStatus int `json:"http_status"`
			Results    []struct {
				Text, Status string
				Error        struct{ Message string }
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatal(err)
	}
	var statuses []string
	for _, row := range reply.Error.Results {
		statuses = append(statuses, row.Text+"/"+row.Status)
	}
	if reply.Error.HTTPStatus != 401 || !reflect.DeepEqual(statuses, []string{"first/ok", "conflict/error", "next/ok", "reject/error", "skipped/not_attempted"}) ||
		!strings.Contains(reply.Error.Results[1].Error.Message, "word conflict") {
		t.Fatal(out)
	}
}

func TestVoiceHotwordPagination(t *testing.T) {
	const cursor = "9007199254740993"
	serveHotwords(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error(r.Method)
		}
		q := r.URL.Query()
		switch q.Get("cursor") {
		case "":
			if q.Get("limit") != "50" {
				t.Error(q)
			}
			io.WriteString(w, `{"items":[{"text":"old","weight":0},{"text":"new","weight":4}],"next_cursor":`+cursor+`}`)
		case cursor:
			if q.Get("limit") != "1" {
				t.Error(q)
			}
			io.WriteString(w, `{"items":[{"text":"last","weight":5}]}`)
		case "9007199254740994":
			io.WriteString(w, `{"items":[]}`)
		default:
			t.Error(q)
		}
	})
	out, code := execCLI(t, "voice", "hotwords", "list", "--format", "json")
	if code != 0 || decode(t, out)["count"] != 2.0 || !strings.Contains(out, cursor) || !strings.Contains(out, `"weight":0`) {
		t.Fatal(out)
	}
	out, code = execCLI(t, "voice", "hotwords", "list", "--cursor", cursor, "--limit", "1", "--format", "json")
	if code != 0 || decode(t, out)["count"] != 1.0 || strings.Contains(out, "next_cursor") {
		t.Fatal(out)
	}
	out, code = execCLI(t, "voice", "hotwords", "list", "--cursor", "9007199254740994", "--format", "json")
	if code != 0 || decode(t, out)["count"] != 0.0 || !strings.Contains(out, `"items":[]`) || strings.Contains(out, "next_cursor") {
		t.Fatal(out)
	}
}
