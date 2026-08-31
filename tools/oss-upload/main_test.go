package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderWuyingInstallerUsesOSSBase(t *testing.T) {
	root := t.TempDir()
	scriptsDir := filepath.Join(root, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := strings.Join([]string{
		"OSS_BASE_URL=\"" + sentinelBaseURL + "\"",
		"OSS_RENDERED=\"" + sentinelRendered + "\"",
		"RELEASE_VERSION=\"" + sentinelReleaseVersion + "\"",
		"# " + rawWuyingInstallerURL,
	}, "\n")
	if err := os.WriteFile(filepath.Join(scriptsDir, "install-wuying.sh"), []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}

	baseURL := "https://artifact.example/cli"
	rendered := string(renderWuyingInstaller(root, baseURL, "0.10.0-beta.1"))
	for _, want := range []string{
		"OSS_BASE_URL=\"" + baseURL + "\"",
		"OSS_RENDERED=\"1\"",
		"RELEASE_VERSION=\"0.10.0-beta.1\"",
		baseURL + "/install-wuying.sh",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered installer missing %q:\n%s", want, rendered)
		}
	}
	for _, unwanted := range []string{sentinelBaseURL, sentinelRendered, sentinelReleaseVersion, rawWuyingInstallerURL} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("rendered installer still contains %q:\n%s", unwanted, rendered)
		}
	}
}

func TestPowerShellInstallerContentType(t *testing.T) {
	t.Parallel()
	if got := contentTypeFor("install.ps1"); got != "text/plain; charset=utf-8" {
		t.Fatalf("contentTypeFor(install.ps1) = %q", got)
	}
}

func TestRenderPowerShellInstaller(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	scriptsDir := filepath.Join(root, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := strings.Join([]string{
		`$Base = "` + sentinelBaseURL + `"`,
		`$Rendered = "` + sentinelRendered + `"`,
		`$Source = "` + rawInstallerPS1URL + `"`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(scriptsDir, "install.ps1"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	baseURL := "https://example.test/cli"
	rendered := string(renderInstaller(root, "install.ps1", baseURL))
	for _, want := range []string{`$Base = "` + baseURL + `"`, `$Rendered = "1"`, baseURL + "/install.ps1"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered installer missing %q:\n%s", want, rendered)
		}
	}
	for _, unwanted := range []string{sentinelBaseURL, sentinelRendered, rawInstallerPS1URL} {
		if strings.Contains(rendered, unwanted) {
			t.Errorf("rendered installer still contains %q:\n%s", unwanted, rendered)
		}
	}
}
