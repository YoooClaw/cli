//go:build windows

// Built with -H=windowsgui. The regular CLI remains a console application.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/winproc"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "cleanup" {
		if cleanup(os.Args[2]) != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 5 || os.Args[1] != "run" {
		os.Exit(2)
	}
	root, exe := os.Args[2], os.Args[3]
	if !filepath.IsAbs(exe) || !filepath.IsAbs(root) {
		os.Exit(2)
	}
	if err := os.MkdirAll(filepath.Join(root, "logs"), 0o700); err != nil {
		os.Exit(1)
	}
	log, err := os.OpenFile(filepath.Join(root, "logs", "daemon-supervisor.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(1)
	}
	if _, err := winproc.OwnJob(); err != nil {
		fmt.Fprintln(log, "native host job:", err)
		os.Exit(1)
	}
	cmd := exec.Command(exe, os.Args[4:]...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(log, "native host start:", err)
		os.Exit(1)
	}
	host, hostErr := winproc.Inspect(uint32(os.Getpid()))
	child, childErr := winproc.Inspect(uint32(cmd.Process.Pid))
	if hostErr != nil || childErr != nil {
		fmt.Fprintln(log, "native host identity:", hostErr, childErr)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		os.Exit(1)
	}
	marker, _ := json.Marshal(struct{ Host, Child winproc.Identity }{host, child})
	if err := fsutil.WriteAtomic(filepath.Join(root, "daemon-host.json"), marker, 0o600); err != nil {
		fmt.Fprintln(log, "native host marker:", err)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		os.Exit(1)
	}
	err = cmd.Wait()
	_ = os.Remove(filepath.Join(root, "daemon-host.json"))
	if err != nil {
		fmt.Fprintln(log, "native host child:", err)
		os.Exit(1)
	}
}

func cleanup(path string) error {
	// The helper accepts only the CLI's private, same-volume uninstall files.
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Base(filepath.Dir(path)), "yoooclaw-uninstall") || !strings.HasPrefix(filepath.Base(path), "yoooclaw-") || !strings.HasSuffix(path, ".exe.pending") {
		return fmt.Errorf("invalid cleanup target")
	}
	var err error
	for i := 0; i < 120; i++ {
		err = os.Remove(path)
		if err == nil || os.IsNotExist(err) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	self, _ := os.Executable()
	selfErr := winproc.Unlink(self)
	// Never hide residual files behind an unconditional "complete" result.
	if selfErr != nil || (err != nil && !os.IsNotExist(err)) {
		message := fmt.Sprintf("pending cleanup: target=%s error=%v helper=%s error=%v\n", path, err, self, selfErr)
		_ = os.WriteFile(filepath.Join(filepath.Dir(path), "cleanup-error.log"), []byte(message), 0o600)
		return fmt.Errorf("%s", message)
	}
	_ = os.Remove(filepath.Dir(path)) // empty only; never recursively delete
	return nil
}
