//go:build windows

package daemon

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

const writerLockHelperEnv = "YOOOCLAW_TEST_HOLD_WRITER_LOCK"

func TestWriterLockUsesHermesSentinelOffset(t *testing.T) {
	// Cross-repository contract with
	// yoooclaw_hermes/runtime/writer_lock.py:_WINDOWS_LOCK_OFFSET.
	if windowsWriterLockOffset != 0x7FFF_FFFF {
		t.Fatalf("writer lock offset = %#x, want %#x", windowsWriterLockOffset, 0x7FFF_FFFF)
	}

	path := filepath.Join(t.TempDir(), writerLockFileName)
	metadata := []byte(`{"owner":"hermes-plugin","pid":4242}`)
	if err := os.WriteFile(path, metadata, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestWriterLockHelperProcess$")
	cmd.Env = append(os.Environ(), writerLockHelperEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		_ = stdin.Close()
		if !stopped {
			_ = cmd.Wait()
		}
	})
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(ready) != "ready" {
		t.Fatalf("lock helper did not become ready: ready=%q err=%v stderr=%s", ready, err, stderr.String())
	}

	held, err := probeWriterFileLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("writer probe did not observe the Hermes sentinel lock")
	}

	// Relay/daemon singleton probing must remain at offset 0 and therefore
	// must not collide with the writer sentinel.
	held, err = probeFileLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatal("generic offset-0 probe unexpectedly collided with writer sentinel")
	}

	// The reason for the sentinel is to leave owner/PID diagnostics readable.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("writer metadata became unreadable while sentinel was held: %v", err)
	}
	if !bytes.Equal(raw, metadata) {
		t.Fatalf("writer metadata changed: got %q want %q", raw, metadata)
	}

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("lock helper failed: %v stderr=%s", err, stderr.String())
	}
	stopped = true
}

func TestWriterLockHelperProcess(t *testing.T) {
	path := os.Getenv(writerLockHelperEnv)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	overlapped := syscall.Overlapped{Offset: windowsWriterLockOffset}
	ret, _, callErr := procLockFileEx.Call(
		f.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0, 1, 0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if ret == 0 {
		t.Fatalf("LockFileEx failed: %v", callErr)
	}
	defer procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped))) //nolint:errcheck

	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
}
