package daemon

import (
	"bytes"
	"path/filepath"
	"strings"
)

func linuxProcessState(stat []byte) byte {
	// /proc/<pid>/stat starts with "pid (comm) state". comm may contain spaces
	// or ')', so find the final closing parenthesis instead of splitting fields.
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 || end+2 >= len(stat) {
		return 0
	}
	return stat[end+2]
}

func isDaemonCommandLine(cmdline []byte) bool {
	args := bytes.Split(bytes.TrimRight(cmdline, "\x00"), []byte{0})
	for i, arg := range args {
		if string(arg) == "--yclib-daemon-bootstrap" {
			return true
		}
		if string(arg) == "daemon" && i+1 < len(args) && string(args[i+1]) == "run-foreground" {
			return true
		}
	}
	return false
}

func sameExecutable(expected, actual string) bool {
	actual = strings.TrimSuffix(actual, " (deleted)")
	return canonicalExecutable(expected) == canonicalExecutable(actual)
}

func canonicalExecutable(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
