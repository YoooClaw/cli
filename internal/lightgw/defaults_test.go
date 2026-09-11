package lightgw

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/light"
)

type captureTransport func(*http.Request) (*http.Response, error)

func (f captureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOneShotDefaultsReachSender(t *testing.T) {
	var sent map[string]any
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = captureTransport(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"000000"}`))}, nil
	})
	for _, tc := range []struct {
		name       string
		body       map[string]any
		brightness float64
	}{
		{"text-only", map[string]any{"title": "Tomorrow", "reason": "weather", "repeat": true}, 0},
		{"partial", map[string]any{"reason": "light", "segments": []any{map[string]any{"mode": "steady", "duration_s": float64(8)}}}, 128},
		{"explicit-off", map[string]any{"reason": "light", "segments": []any{map[string]any{"mode": "steady", "duration_s": float64(8), "brightness": float64(0)}}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Send(t.TempDir(), tc.body, "example.invalid", "test", nil)
			if err != nil || !result.(light.SendResult).OK {
				t.Fatalf("%v %v", result, err)
			}
			segment := sent["segments"].([]any)[0].(map[string]any)
			if segment["brightness"] != tc.brightness || sent["repeat_times"] != float64(1) {
				t.Fatalf("unexpected payload: %v", sent)
			}
			if sent["reason"] != tc.body["reason"] {
				t.Fatal("lost display text")
			}
		})
	}
	for _, segments := range []any{nil, []any{}, []any{map[string]any{"mode": "steady", "duration_s": float64(8), "brightness": "128"}}, []any{map[string]any{"mode": "steady", "duration_s": float64(8), "color": nil}}} {
		sent = nil
		_, err := Send(t.TempDir(), map[string]any{"segments": segments, "reason": "text"}, "example.invalid", "test", nil)
		if err == nil || sent != nil {
			t.Fatalf("invalid explicit segments were sent: %v", segments)
		}
	}
	_, err := Send(t.TempDir(), map[string]any{}, "example.invalid", "test", nil)
	if err == nil {
		t.Fatal("accepted empty request")
	}
}
