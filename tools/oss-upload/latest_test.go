package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadLatestProcess(t *testing.T) {
	if os.Getenv("OSS_LATEST_TEST_PROCESS") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("oss-upload", flag.ExitOnError)
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"oss-upload"}, os.Args[i+1:]...)
			break
		}
	}
	main()
	os.Exit(0)
}

func TestUploadsNeverPromoteLatest(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		args                      []string
		production, missing, fail bool
	}{
		{name: "production default", production: true},
		{name: "test upload only", args: []string{"--test-build", "--version", "1.2.3"}},
		{name: "removed test promotion rejected", args: []string{"--test-build", "--version", "1.2.3", "--update-latest"}, fail: true},
		{name: "old branch fix", args: []string{"--test-build", "--version", "0.9.1-alpha.2"}},
		{name: "production explicit rejected", args: []string{"--update-latest"}, fail: true},
		{name: "production target rejected", args: []string{"--test-build", "--version", "1.2.3"}, production: true, fail: true},
		{name: "missing artifact never promotes", args: []string{"--test-build", "--version", "1.2.3"}, missing: true, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"version":"1.2.3"}`), 0600)
			os.Mkdir(filepath.Join(root, "dist-native"), 0700)
			for _, name := range nativeArtifacts {
				if tc.missing && name == nativeArtifacts[0] {
					continue
				}
				os.WriteFile(filepath.Join(root, "dist-native", name), []byte("fixture"), 0600)
			}
			var checksums strings.Builder
			for _, name := range nativeArtifacts {
				if name != "checksums.txt" {
					fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256([]byte("fixture")), name)
				}
			}
			os.WriteFile(filepath.Join(root, "dist-native", "checksums.txt"), []byte(checksums.String()), 0600)
			args := append([]string{"-test.run=^TestUploadLatestProcess$", "--", "--dry-run", "--only", "artifacts"}, tc.args...)
			cmd := exec.Command(os.Args[0], args...)
			cmd.Dir = root
			// Deliberately exclude real OSS credentials and local .env files.
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "OSS_LATEST_TEST_PROCESS=1"}
			if tc.production {
				cmd.Env = append(cmd.Env, "OSS_BUCKET="+defaultBucket, "OSS_PUBLIC_URL="+defaultPublicURL)
			} else {
				cmd.Env = append(cmd.Env, "OSS_BUCKET=isolated-test", "OSS_PUBLIC_URL=https://artifacts.test.invalid")
			}
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v output=%s", err, out)
			}
			wrote := strings.Contains(string(out), "would upload latest -> cli/latest")
			if wrote {
				t.Fatalf("upload unexpectedly wrote latest: %s", out)
			}
		})
	}
}
