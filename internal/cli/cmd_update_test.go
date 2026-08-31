package cli

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestNativeTargetNameForWindows(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		goos, goarch, want string
	}{
		{goos: "windows", goarch: "amd64", want: "yoooclaw-win32-x64.exe"},
		{goos: "darwin", goarch: "arm64", want: "yoooclaw-darwin-arm64"},
		{goos: "linux", goarch: "amd64", want: "yoooclaw-linux-x64"},
		{goos: "windows", goarch: "386", want: ""},
	} {
		if got := nativeTargetNameFor(tc.goos, tc.goarch); got != tc.want {
			t.Errorf("nativeTargetNameFor(%q, %q) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestUpgradeCommandForNativeInstallers(t *testing.T) {
	t.Parallel()

	windows := upgradeCommandFor("native", "windows", "amd64", "latest", "1.2.3")
	for _, want := range []string{"install.ps1", "-Version 1.2.3", "-Force"} {
		if !strings.Contains(windows, want) {
			t.Errorf("Windows upgrade command %q does not contain %q", windows, want)
		}
	}

	unix := upgradeCommandFor("native", "linux", "amd64", "latest", "1.2.3")
	for _, want := range []string{"install.sh", "--version 1.2.3", "--force"} {
		if !strings.Contains(unix, want) {
			t.Errorf("Unix upgrade command %q does not contain %q", unix, want)
		}
	}
}

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
