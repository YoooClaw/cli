package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoggerWritesLevels(t *testing.T) {
	t.Parallel()
	logFile := filepath.Join(t.TempDir(), "daemon.log")
	l := NewLogger(logFile, "info", false)
	l.Info("hello")
	l.Warn("careful")
	l.Error("boom")
	l.Debug("verbose") // info 级别下 debug 被过滤

	raw, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.Contains(content, "[INFO] hello") || !strings.Contains(content, "[ERROR] boom") {
		t.Errorf("missing expected lines: %s", content)
	}
	if strings.Contains(content, "verbose") {
		t.Error("debug should be filtered at info level")
	}
	// 行格式应可被 logread 解析（ISO 时间 + [LEVEL]）
	if !strings.Contains(content, "[WARN] careful") {
		t.Errorf("warn line missing: %s", content)
	}
}

func TestLoggerCleanupRotated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "daemon.log")
	stale := logFile + ".2020-01-01"
	fresh := logFile + "." + dateKey(time.Now().AddDate(0, 0, -1))
	for _, f := range []string{stale, fresh} {
		if err := os.WriteFile(f, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	NewLogger(logFile, "info", false)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("rotated log beyond retention should be removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("recent rotated log should survive cleanup")
	}
}

func TestLoggerLevelDefaults(t *testing.T) {
	t.Parallel()
	l := NewLogger(filepath.Join(t.TempDir(), "x.log"), "", false)
	if l.level != "info" {
		t.Errorf("empty level should default to info, got %q", l.level)
	}
	if !l.enabled("error") || !l.enabled("info") {
		t.Error("info logger should enable info/error")
	}
	if l.enabled("debug") {
		t.Error("info logger should not enable debug")
	}
	// 未知级别按 info 处理
	if !l.enabled("bogus") {
		t.Error("unknown level treated as info should be enabled")
	}
}

func TestUpperHelper(t *testing.T) {
	t.Parallel()
	if upper("info") != "INFO" || upper("Warn") != "WARN" {
		t.Error("upper")
	}
}

func TestDateKey(t *testing.T) {
	t.Parallel()
	d := time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC)
	if dateKey(d) != "2026-06-07" {
		t.Errorf("dateKey = %q", dateKey(d))
	}
}
