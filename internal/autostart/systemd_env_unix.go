//go:build linux || darwin

package autostart

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Only retry a connection failure: retrying arbitrary failed mutations could
// execute them twice. Never change the caller's environment or select another UID.
func runUserSystemctl(args, env []string, uid, euid int, base string, run func([]string, []string) ([]byte, error)) ([]byte, error) {
	out, err := run(args, env)
	if err == nil || !strings.Contains(string(out), "Failed to connect to bus:") || uid != euid {
		return out, err
	}
	dir := filepath.Join(base, strconv.Itoa(euid))
	if !ownedRuntimePath(dir, euid, true) || !ownedRuntimePath(filepath.Join(dir, "bus"), euid, false) {
		return out, err
	}
	repaired := make([]string, 0, len(env)+2)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "XDG_RUNTIME_DIR=") && !strings.HasPrefix(entry, "DBUS_SESSION_BUS_ADDRESS=") {
			repaired = append(repaired, entry)
		}
	}
	repaired = append(repaired, "XDG_RUNTIME_DIR="+dir, "DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(dir, "bus"))
	return run(args, repaired)
}

func ownedRuntimePath(path string, uid int, directory bool) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid {
		return false
	}
	if directory {
		return info.IsDir() && info.Mode().Perm() == 0o700
	}
	return info.Mode()&os.ModeSocket != 0
}
