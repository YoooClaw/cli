package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordingIndexRenameTarget(t *testing.T) {
	target := filepath.Join("recordings", "index.json")
	err := &os.LinkError{Op: "rename", Old: filepath.Join("recordings", ".abc.tmp"), New: target, Err: os.ErrPermission}
	got, ok := recordingIndexRenameTarget(err)
	if !ok || got != filepath.Clean(target) {
		t.Fatalf("target=(%q,%v), want (%q,true)", got, ok, filepath.Clean(target))
	}
	if _, ok := recordingIndexRenameTarget(&os.PathError{Op: "open", Path: target, Err: os.ErrPermission}); ok {
		t.Fatal("non-rename error unexpectedly selected for holder diagnosis")
	}
	if _, ok := recordingIndexRenameTarget(&os.LinkError{Op: "rename", Old: "a", New: "summary.json", Err: os.ErrPermission}); ok {
		t.Fatal("non-index target unexpectedly selected for holder diagnosis")
	}
	if _, ok := recordingIndexRenameTarget(errors.New("plain error")); ok {
		t.Fatal("plain error unexpectedly selected for holder diagnosis")
	}
}

func TestLogRecordingWriteFailureIncludesRequestContext(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	s := &server{logger: NewLogger(logPath, "info", false)}
	s.logRecordingWriteFailure("rec-123", "relay-256", errors.New("write failed"))

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	line := string(raw)
	for _, want := range []string{
		"[recording-result] 写入失败",
		"recordingId=rec-123",
		"relay=relay-256",
		"error=write failed",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("log missing %q: %s", want, line)
		}
	}
}
