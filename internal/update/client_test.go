package update

import (
	"context"
	"encoding/json"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, handler http.HandlerFunc) (Identity, *Client) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	id := Identity{BaseURL: server.URL, KeyHash: Hash([]byte("fixture")), DeviceID: Hash([]byte(ID())), CurrentVersion: "0.12.0", Platform: "macos", Arch: "arm64", Profile: "default", Root: t.TempDir()}
	return id, &Client{HTTP: server.Client(), Key: func() (string, error) { return "fixture", nil }}
}
func TestCheckPlatformExactVersionAndAuthentication(t *testing.T) {
	calls := 0
	id, c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != CheckPath || r.Header.Get("X-Api-Key-Id") != "fixture" {
			t.Error("wrong request")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["component"] != "CLI" || body["platform"] != "macos-arm64" || body["arch"] != nil {
			t.Error(body)
		}
		if calls == 2 && body["targetVersion"] != "0.13.0" {
			t.Error("missing exact version")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "000000", "data": map[string]any{"updateAvailable": true, "targetVersion": "0.13.0", "downloadUrl": "https://example.test/cli", "fileSize": 3, "fileHash": Hash([]byte("abc"))}})
	})
	offer, err := c.Check(context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Recheck(context.Background(), id, *offer); err != nil {
		t.Fatal(err)
	}
}
func TestCheckRejectsWrongVersionAndMalformedOffer(t *testing.T) {
	for _, response := range []string{
		`{"success":true,"code":"000000","data":{"updateAvailable":true,"targetVersion":"0.11.0","downloadUrl":"https://x","fileSize":1,"fileHash":"bad"}}`,
		`{"success":true,"code":"000000","data":{}}`,
		`{"success":false,"code":"000019","message":"ock-secret-do-not-log"}`,
	} {
		t.Run(response[:20], func(t *testing.T) {
			id, c := fixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, response) })
			if _, err := c.Check(context.Background(), id, ""); err == nil {
				t.Fatal("accepted malformed offer")
			}
		})
	}
}
func TestSemverPrereleaseOrdering(t *testing.T) {
	for _, pair := range [][2]string{
		{"0.13.0-alpha.421", "0.13.0-alpha.422"},
		{"0.13.0-alpha.9", "0.13.0-alpha.10"},
		{"0.13.0-alpha.999", "0.13.0-beta.0"},
		{"0.13.0-beta.9", "0.13.0-beta.10"},
		{"0.13.0-beta.10", "0.13.0"},
		{"0.13.0", "0.13.1-alpha.0"},
	} {
		for _, direction := range []int{1, -1} {
			a, b := pair[0], pair[1]
			if direction < 0 {
				a, b = b, a
			}
			v, err := Compare(a, b)
			if err != nil || v*direction >= 0 {
				t.Fatalf("%s vs %s: %d %v", a, b, v, err)
			}
		}
		v, err := Compare(pair[0], pair[0])
		if err != nil || v != 0 {
			t.Fatalf("same version: %d %v", v, err)
		}
	}
	for _, version := range []string{"dev", "0.13.0-alpha", "0.13.0-alpha.01", "0.13.0-test.1", "0.13.0-rc.1"} {
		if _, err := Compare(version, "0.13.0"); err == nil {
			t.Fatalf("accepted %s", version)
		}
	}
}

func TestCheckAlphaOffer(t *testing.T) {
	id, c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"code":"000000","data":{"updateAvailable":true,"targetVersion":"0.13.0-alpha.422","downloadUrl":"https://example.com/cli","fileSize":3,"fileHash":"`+Hash([]byte("abc"))+`"}}`)
	})
	id.CurrentVersion = "0.13.0-alpha.421"
	offer, err := c.Check(context.Background(), id, "")
	if err != nil || offer == nil || offer.TargetVersion != "0.13.0-alpha.422" {
		t.Fatalf("offer=%v err=%v", offer, err)
	}
}
func TestDownloadChecksBytesWithoutCredentialHeader(t *testing.T) {
	id, c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key-Id") != "" {
			t.Error("credential leaked")
		}
		_, _ = w.Write([]byte("abc"))
	})
	a := Artifact{DownloadURL: id.BaseURL, FileSize: 3, FileHash: Hash([]byte("abc"))}
	if err := c.Download(context.Background(), a, filepath.Join(t.TempDir(), "payload")); err != nil {
		t.Fatal(err)
	}
	a.FileHash = Hash([]byte("bad"))
	path := filepath.Join(t.TempDir(), "payload")
	if err := c.Download(context.Background(), a, path); ErrorCode(err) != "HASH_MISMATCH" {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("corrupt payload retained")
	}
}
func TestSchedulerBusyAndFourHourCooldown(t *testing.T) {
	id, c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "000000", "data": map[string]any{"updateAvailable": true, "targetVersion": "0.13.0", "downloadUrl": "https://example.test/cli", "fileSize": 3, "fileHash": Hash([]byte("abc"))}})
	})
	s := &Scheduler{}
	now := time.Now()
	launches := 0
	launch := func(Artifact) error { launches++; return fail("TEST", "failed") }
	if err := s.Tick(context.Background(), id, c, now, false, true, launch); err != nil {
		t.Fatal(err)
	}
	if launches != 0 {
		t.Fatal("installed while busy")
	}
	_ = s.Tick(context.Background(), id, c, now.Add(time.Minute), true, true, launch)
	_ = s.Tick(context.Background(), id, c, now.Add(2*time.Minute), true, true, launch)
	if launches != 1 {
		t.Fatal(launches)
	}
	restarted := &Scheduler{}
	_ = restarted.Tick(context.Background(), id, c, now.Add(3*time.Minute), true, true, launch)
	if launches != 1 {
		t.Fatal("restart reset install cooldown")
	}
}

func TestPersistentDeviceIdentity(t *testing.T) {
	for _, stored := range []string{"", strings.Repeat("a", 32), strings.Repeat("b", 64), strings.Repeat("z", 32)} {
		t.Run(stored, func(t *testing.T) {
			t.Setenv("YOOOCLAW_HOME", t.TempDir())
			if err := fsutil.WriteJSON(paths.SharedCredentialsPath(), map[string]string{"apiKey": "fixture"}, 0600); err != nil {
				t.Fatal(err)
			}
			p := paths.For("default")
			if err := config.Save(p, config.Default(p.Credentials)); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(paths.RootDir(), "update", "device-id")
			if stored != "" {
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(stored), 0600); err != nil {
					t.Fatal(err)
				}
			}
			id, err := NewIdentity(p)
			if strings.Contains(stored, "z") {
				if err == nil {
					t.Fatal("accepted corrupt ID")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !deviceIDPattern.MatchString(id.DeviceID) {
				t.Fatal(id.DeviceID)
			}
			want := stored
			if len(stored) == 32 {
				want = Hash([]byte(stored))
			}
			if stored != "" && id.DeviceID != want {
				t.Fatal("unstable migration")
			}
			again, err := NewIdentity(p)
			if err != nil || again.DeviceID != id.DeviceID {
				t.Fatalf("restart changed identity: %v", err)
			}
			raw, _ := os.ReadFile(file)
			if string(raw) != id.DeviceID {
				t.Fatal("identity not persisted")
			}
		})
	}
}
