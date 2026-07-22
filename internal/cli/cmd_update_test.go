package cli

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

type updateRoundTripFunc func(*http.Request) (*http.Response, error)

func (f updateRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUpdateSelfOmitsHint(t *testing.T) {
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: updateRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"dist-tags":{"latest":"9999.0.0"}}`)),
		}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = originalClient })

	cmd := &cobra.Command{}
	cmd.Flags().Bool("beta", false, "")

	result, err := updateSelf(nil, cmd, nil)
	if err != nil {
		t.Fatalf("updateSelf() error = %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("updateSelf() result type = %T, want map[string]any", result)
	}
	if _, exists := payload["hint"]; exists {
		t.Fatalf("updateSelf() returned unexpected hint: %v", payload["hint"])
	}
}
