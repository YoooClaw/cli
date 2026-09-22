//go:build linux || darwin

package autostart

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestUserSystemctlSessionRecovery(t *testing.T) {
	base, err := os.MkdirTemp("", "bus-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	uid := os.Getuid()
	dir := filepath.Join(base, strconv.Itoa(uid))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "bus"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	for _, tc := range []struct {
		name, output string
		env          []string
		euid         int
		wantCalls    int
	}{
		{"missing", "Failed to connect to bus: No medium found", []string{"PATH=/bin"}, uid, 2},
		{"inherited root", "Failed to connect to bus: Permission denied", []string{"XDG_RUNTIME_DIR=/run/user/0", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/0/bus", "PATH=/bin"}, uid, 2},
		{"task denied", "Failed to start unit: Access denied", nil, uid, 1},
		{"different effective user", "Failed to connect to bus: No medium found", nil, uid + 1, 1},
		{"healthy", "", nil, uid, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			original := append([]string{}, tc.env...)
			_, gotErr := runUserSystemctl([]string{"start", "test.service"}, tc.env, uid, tc.euid, base, func(args, env []string) ([]byte, error) {
				calls++
				if !reflect.DeepEqual(args, []string{"start", "test.service"}) {
					t.Fatal(args)
				}
				if calls == 1 && tc.output != "" {
					return []byte(tc.output), errors.New("failed")
				}
				if calls == 2 {
					joined := strings.Join(env, "\n")
					for _, want := range []string{"XDG_RUNTIME_DIR=" + dir, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + dir + "/bus", "PATH=/bin"} {
						if !strings.Contains(joined, want) {
							t.Fatalf("missing %s: %s", want, joined)
						}
					}
					if strings.Contains(joined, "=/run/user/0") {
						t.Fatal("stale environment retained")
					}
				}
				return []byte("ok"), nil
			})
			if calls != tc.wantCalls {
				t.Fatalf("calls=%d", calls)
			}
			if tc.wantCalls == 2 && gotErr != nil {
				t.Fatal(gotErr)
			}
			if strings.Join(original, "\n") != strings.Join(tc.env, "\n") {
				t.Fatal("caller environment mutated")
			}
		})
	}
	if ownedRuntimePath(dir, uid+1, true) {
		t.Fatal("accepted foreign owner")
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if ownedRuntimePath(dir, uid, true) {
		t.Fatal("accepted writable runtime directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if ownedRuntimePath(link, uid, true) {
		t.Fatal("accepted symlink")
	}
	listener.Close()
	if err := os.WriteFile(filepath.Join(dir, "bus"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runUserSystemctl(nil, nil, uid, uid, base, func(_, _ []string) ([]byte, error) {
		calls++
		return []byte("Failed to connect to bus: No medium found"), errors.New("failed")
	})
	if calls != 1 {
		t.Fatal("retried with a non-socket bus")
	}
}
