package logread

import (
	"os"
	"path/filepath"
	"testing"
)

func writeLog(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestParseLine(t *testing.T) {
	t.Parallel()
	l := parseLine("2026-06-07T10:00:00+08:00 [INFO] started")
	if l == nil {
		t.Fatal("expected parse")
	}
	if l.Date != "2026-06-07" || l.Level != "info" || l.Message != "started" {
		t.Errorf("parsed = %+v", l)
	}
	if parseLine("garbage line") != nil {
		t.Error("non-matching line should return nil")
	}
}

func TestSearchNewestFirstAndLimit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	log := filepath.Join(dir, "daemon.log")
	writeLog(t, log, "2026-06-07T01:00:00Z [INFO] first\n2026-06-07T02:00:00Z [ERROR] second\n2026-06-07T03:00:00Z [INFO] third\n")
	got := Search(log, Query{Limit: 10})
	if len(got) != 3 || got[0].Message != "third" || got[2].Message != "first" {
		t.Fatalf("expected newest-first order: %+v", got)
	}
	limited := Search(log, Query{Limit: 2})
	if len(limited) != 2 || limited[0].Message != "third" {
		t.Errorf("limit not applied: %+v", limited)
	}
}

func TestSearchLevelAndKeyword(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	log := filepath.Join(dir, "daemon.log")
	writeLog(t, log, "2026-06-07T01:00:00Z [INFO] hello world\n2026-06-07T02:00:00Z [ERROR] boom failure\n")
	errOnly := Search(log, Query{Level: "error", Limit: 10})
	if len(errOnly) != 1 || errOnly[0].Message != "boom failure" {
		t.Errorf("level filter failed: %+v", errOnly)
	}
	kw := Search(log, Query{Keyword: "WORLD", Limit: 10})
	if len(kw) != 1 || kw[0].Message != "hello world" {
		t.Errorf("keyword filter failed: %+v", kw)
	}
}

func TestSearchDateRange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	log := filepath.Join(dir, "daemon.log")
	writeLog(t, log, "2026-06-05T01:00:00Z [INFO] old\n2026-06-07T01:00:00Z [INFO] mid\n2026-06-09T01:00:00Z [INFO] new\n")
	got := Search(log, Query{From: "2026-06-06", To: "2026-06-08", Limit: 10})
	if len(got) != 1 || got[0].Message != "mid" {
		t.Errorf("date range failed: %+v", got)
	}
}

func TestSearchRotatedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	log := filepath.Join(dir, "daemon.log")
	writeLog(t, log, "2026-06-07T01:00:00Z [INFO] current\n")
	writeLog(t, filepath.Join(dir, "daemon.log.2026-06-05"), "2026-06-05T01:00:00Z [INFO] rotated-old\n")
	writeLog(t, filepath.Join(dir, "daemon.log.2026-06-06"), "2026-06-06T01:00:00Z [INFO] rotated-new\n")
	got := Search(log, Query{Limit: 10})
	// current 在最前，轮转按日期倒序
	if len(got) != 3 || got[0].Message != "current" || got[1].Message != "rotated-new" || got[2].Message != "rotated-old" {
		t.Fatalf("rotated ordering wrong: %+v", got)
	}
}

func TestSearchUnparsedLineKept(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	log := filepath.Join(dir, "daemon.log")
	writeLog(t, log, "panic: raw stack trace\n")
	got := Search(log, Query{Limit: 10})
	if len(got) != 1 || got[0].Raw != "panic: raw stack trace" || got[0].Level != "" {
		t.Errorf("unparsed line should be kept raw: %+v", got)
	}
}

func TestSearchMissingDir(t *testing.T) {
	t.Parallel()
	got := Search(filepath.Join(t.TempDir(), "nope", "daemon.log"), Query{Limit: 10})
	if len(got) != 0 {
		t.Errorf("missing dir -> empty, got %+v", got)
	}
}
