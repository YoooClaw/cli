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

func TestInstallerUploadKeysSkipLiveOnPrerelease(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		version  string
		filename string
		live     bool
		want     []string
	}{
		{
			name:     "stable refreshes live and archive",
			version:  "0.10.0",
			filename: "install-wuying.sh",
			live:     true,
			want: []string{
				"cli/v0.10.0/installer/install-wuying.sh",
				"cli/install-wuying.sh",
			},
		},
		{
			name:     "prerelease archives only",
			version:  "0.11.0-beta.1",
			filename: "install-wuying.sh",
			want:     []string{"cli/v0.11.0-beta.1/installer/install-wuying.sh"},
		},
		{
			name:     "prerelease leaves the unix installer alone",
			version:  "0.11.0-beta.1",
			filename: "install.sh",
			want:     []string{"cli/v0.11.0-beta.1/installer/install.sh"},
		},
		{
			name:     "test build refreshes live even when prerelease",
			version:  "0.12.3-test.42",
			filename: "install.sh",
			live:     true,
			want: []string{
				"cli/v0.12.3-test.42/installer/install.sh",
				"cli/install.sh",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := installerUploadKeys("cli", tc.version, tc.filename, tc.live)
			if len(got) != len(tc.want) {
				t.Fatalf("installerUploadKeys() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("installerUploadKeys()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestIsPrerelease(t *testing.T) {
	t.Parallel()

	for version, want := range map[string]bool{
		"0.10.0":        false,
		"1.0.0":         false,
		"0.10.0-beta.3": true,
		"0.10.0-rc.1":   true,
	} {
		if got := isPrerelease(version); got != want {
			t.Errorf("isPrerelease(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestValidateTestTarget(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		bucket    string
		publicURL string
		wantErr   bool
	}{
		{name: "explicit test target", bucket: "example-test", publicURL: "https://test.example.com", wantErr: false},
		{name: "missing bucket", bucket: "", publicURL: "https://test.example.com", wantErr: true},
		{name: "missing public URL", bucket: "example-test", publicURL: " ", wantErr: true},
		{name: "production bucket", bucket: defaultBucket, publicURL: "https://test.example.com", wantErr: true},
		{name: "production URL with trailing slash", bucket: "example-test", publicURL: defaultPublicURL + "/", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateTestTarget(tc.bucket, tc.publicURL)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateTestTarget(%q, %q) error = %v, wantErr %v", tc.bucket, tc.publicURL, err, tc.wantErr)
			}
		})
	}
}
