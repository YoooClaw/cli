package cli

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/version"
	"github.com/spf13/cobra"
)

type updateRoundTripFunc func(*http.Request) (*http.Response, error)

func (f updateRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUpdateSelfUsesRolloutWithPlatform(t *testing.T) {
	ctx := envTestContext(t)
	if err := os.WriteFile(paths.SharedCredentialsPath(), []byte(`{"apiKey":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	oldVersion := version.Version
	version.Version = "0.12.0"
	t.Cleanup(func() { version.Version = oldVersion })
	originalTransport := http.DefaultTransport
	http.DefaultTransport = updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/ota/client/plugin/check-update" {
			t.Fatalf("unexpected endpoint: %s", req.URL.Path)
		}
		raw, _ := io.ReadAll(req.Body)
		for _, field := range []string{`"component":"CLI"`, `"platform":`} {
			if !strings.Contains(string(raw), field) {
				t.Fatal(string(raw))
			}
		}
		if strings.Contains(string(raw), `"arch":`) {
			t.Fatal("check request must use one combined platform field")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"code":"000000","data":{"updateAvailable":false}}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	result, err := updateSelf(ctx, &cobra.Command{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["updateAvailable"] != false {
		t.Fatal(result)
	}
}
