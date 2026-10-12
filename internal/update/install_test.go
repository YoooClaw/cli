package update

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

func buildFixture(t *testing.T, version string) []byte {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Println(\""+version+"\")}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "fixture")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, source).CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestRealBinaryReplacementAndOfflineActivation(t *testing.T) {
	binary := buildFixture(t, "0.13.0")
	var serverURL string
	id, c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/binary" {
			_, _ = w.Write(binary)
			return
		}
		if r.URL.Path == CheckPath {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "000000", "data": map[string]any{"updateAvailable": true, "targetVersion": "0.13.0", "downloadUrl": serverURL + "/binary", "fileSize": len(binary), "fileHash": Hash(binary)}})
			return
		}
		if r.URL.Path == ReportPath {
			var body struct {
				Events []Event `json:"events"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(map[string]int{"accepted": len(body.Events), "rejected": 0})
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"code":"000000","data":true}`))
	})
	serverURL = id.BaseURL
	target := filepath.Join(t.TempDir(), "yoooclaw")
	if runtime.GOOS == "windows" {
		target += ".exe"
	}
	old := buildFixture(t, "0.12.0")
	if err := os.WriteFile(target, old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Register(id.Root, target); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		_ = os.WriteFile(filepath.Join(filepath.Dir(target), "yc.exe"), old, 0700)
	}
	a := Attempt{Identity: id, UpdateID: ID(), Trigger: "cli", Target: target, OldHash: Hash(old), CreatedAt: time.Now(), Artifact: Artifact{TargetVersion: "0.13.0", DownloadURL: serverURL + "/binary", FileHash: Hash(binary), FileSize: int64(len(binary))}}
	_ = fsutil.WriteJSON(JobPath(a), a, 0600)
	cooldownPath := filepath.Join(id.Directory(), "install-cooldown.json")
	if err := fsutil.WriteJSON(cooldownPath, time.Now().Add(4*time.Hour), 0600); err != nil {
		t.Fatal(err)
	}
	starts := 0
	skills := 0
	err := Run(context.Background(), c, a, Hooks{Inspect: func() (Runtime, error) { return Runtime{}, nil }, Stop: func() error { t.Fatal("stopped offline daemon"); return nil }, Start: func() error { starts++; return nil }, Skills: func() error { skills++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := fileHash(target)
	if got != Hash(binary) || starts != 0 || skills != 1 {
		t.Fatal("replacement/ownership mismatch")
	}
	saved, err := ReadAttempt(JobPath(a))
	if err != nil || !saved.Ready {
		t.Fatal("missing activation handoff")
	}
	if _, err := os.Stat(filepath.Join(id.Directory(), a.UpdateID+".result.json")); !os.IsNotExist(err) {
		t.Fatal("offline install reported activated")
	}
	ConfirmPending(id, target) // The old running version cannot confirm activation.
	if _, err := os.Stat(cooldownPath); err != nil {
		t.Fatal("unconfirmed install lost cooldown", err)
	}
	id.CurrentVersion = "0.13.0"
	ConfirmPending(id, target)
	if _, err := os.Stat(cooldownPath); !os.IsNotExist(err) {
		t.Fatal("activated install retained cooldown", err)
	}
	saved, _ = ReadAttempt(JobPath(a))
	if saved.Ready {
		t.Fatal("activation record not retired")
	}
	// A later rollback must not resurrect the prior installed-version handoff.
	id.CurrentVersion = "0.12.0"
	ConfirmPending(id, target)
	saved, _ = ReadAttempt(JobPath(a))
	if saved.Ready {
		t.Fatal("rollback reused stale handoff")
	}
}
func TestRejectHermesAndUnregisteredTargets(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "hermes-plugin", "bin", "yc")
	_ = os.MkdirAll(filepath.Dir(exe), 0700)
	_ = os.WriteFile(exe, []byte("fixture"), 0700)
	if err := Register(root, exe); err == nil {
		t.Fatal("registered embedded Hermes CLI")
	}
	exe = filepath.Join(root, "standalone")
	_ = os.WriteFile(exe, []byte("fixture"), 0700)
	if _, err := OwnedTarget(root, exe); err == nil {
		t.Fatal("accepted unregistered binary")
	}
}

func TestWorkerBusyRevocationAndRestartResults(t *testing.T) {
	old, binary := buildFixture(t, "0.12.0"), buildFixture(t, "0.13.0")
	for _, scenario := range []string{"busy", "revoked", "restart-failed", "activated"} {
		t.Run(scenario, func(t *testing.T) {
			var serverURL string
			var events []Event
			var results []map[string]any
			id, c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/binary":
					_, _ = w.Write(binary)
				case CheckPath:
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "000000", "data": map[string]any{"updateAvailable": scenario != "revoked", "targetVersion": "0.13.0", "downloadUrl": serverURL + "/binary", "fileSize": len(binary), "fileHash": Hash(binary)}})
				case ReportPath:
					var body struct {
						Events []Event `json:"events"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					events = append(events, body.Events...)
					_ = json.NewEncoder(w).Encode(map[string]int{"accepted": len(body.Events), "rejected": 0})
				case ResultPath:
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					results = append(results, body)
					_, _ = w.Write([]byte(`{"success":true,"code":"000000","data":true}`))
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			})
			serverURL = id.BaseURL
			target := filepath.Join(t.TempDir(), "yoooclaw")
			if runtime.GOOS == "windows" {
				target += ".exe"
			}
			_ = os.WriteFile(target, old, 0700)
			if runtime.GOOS == "windows" {
				_ = os.WriteFile(filepath.Join(filepath.Dir(target), "yc.exe"), old, 0700)
			}
			if err := Register(id.Root, target); err != nil {
				t.Fatal(err)
			}
			a := Attempt{Identity: id, UpdateID: ID(), Trigger: "silent", Target: target, OldHash: Hash(old), CreatedAt: time.Now(), Artifact: Artifact{TargetVersion: "0.13.0", DownloadURL: serverURL + "/binary", FileHash: Hash(binary), FileSize: int64(len(binary))}}
			_ = fsutil.WriteJSON(JobPath(a), a, 0600)
			cooldownPath := filepath.Join(id.Directory(), "install-cooldown.json")
			if err := fsutil.WriteJSON(cooldownPath, time.Now().Add(4*time.Hour), 0600); err != nil {
				t.Fatal(err)
			}
			stopped, started := 0, 0
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := Run(ctx, c, a, Hooks{
				Inspect: func() (Runtime, error) {
					v := "0.12.0"
					if started > 0 {
						v = "0.13.0"
					}
					return Runtime{Running: true, Busy: scenario == "busy", Connected: true, Version: v, Executable: target, Profile: id.Profile, Host: strings.TrimPrefix(id.BaseURL, "https://")}, nil
				},
				Stop: func() error { stopped++; return nil },
				Start: func() error {
					started++
					if scenario == "restart-failed" {
						return fail("RESTART_FAILED", "fixture restart failed")
					}
					return nil
				},
				Skills: func() error { return nil },
			})
			_, cooldownErr := os.Stat(cooldownPath)
			if scenario == "activated" {
				if !os.IsNotExist(cooldownErr) {
					t.Fatal("success retained cooldown", cooldownErr)
				}
			} else if cooldownErr != nil {
				t.Fatal("unconfirmed update lost cooldown", cooldownErr)
			}
			hash, _ := fileHash(target)
			if scenario == "busy" || scenario == "revoked" {
				if stopped != 0 || started != 0 || hash != Hash(old) {
					t.Fatal("touched runtime before eligibility/idle")
				}
			} else if stopped != 1 || started != 1 || hash != Hash(binary) {
				t.Fatal("wrong replacement/restart behavior")
			}
			switch scenario {
			case "busy":
				if err != nil || len(results) != 0 {
					t.Fatalf("deferred update finalized: %v %v", err, results)
				}
			case "revoked":
				if ErrorCode(err) != "OFFER_REVOKED" || len(results) != 1 {
					t.Fatalf("revocation: %v %v", err, results)
				}
			case "restart-failed":
				if ErrorCode(err) != "RESTART_FAILED" || len(results) != 1 || results[0]["errorCode"] != "RESTART_FAILED" {
					t.Fatalf("restart error missing: %v %v", err, results)
				}
				for _, e := range events {
					if e.Stage == "activate" && e.Result == "succeeded" {
						t.Fatal("failed restart reported activated")
					}
				}
			case "activated":
				if err != nil || len(results) != 1 || results[0]["errorCode"] != nil {
					t.Fatalf("activation: %v %v", err, results)
				}
				saved, _ := ReadAttempt(JobPath(a))
				if saved.Ready {
					t.Fatal("handoff not retired")
				}
			}
			for _, body := range results {
				if body["updateId"] != a.UpdateID || body["component"] != "CLI" {
					t.Fatal("lost result identity")
				}
			}
		})
	}
}

func TestRejectHermesExplicitSharedBinary(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("HERMES_HOME", home)
	target := filepath.Join(root, "yoooclaw")
	_ = os.WriteFile(target, []byte("fixture"), 0700)
	if err := Register(root, target); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home, ".env"), []byte("YOOOCLAW_CLI_PATH='"+target+"'\n"), 0600)
	if _, err := OwnedTarget(root, target); ErrorCode(err) != "SHARED_INSTALLATION" {
		t.Fatalf("shared target accepted: %v", err)
	}
}
