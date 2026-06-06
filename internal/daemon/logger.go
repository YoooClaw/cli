package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

var levelOrder = map[string]int{"error": 0, "warn": 1, "info": 2, "debug": 3, "trace": 4}

// Logger 是 daemon 文件 logger，写 daemon.log 并按日期轮转为 daemon.log.YYYY-MM-DD。
// 行格式：`<ISO本地时间> [LEVEL] message`（与 logread 解析一致）。
type Logger struct {
	logFile    string
	level      string
	alsoStderr bool
	mu         sync.Mutex
	currentDay string
}

// NewLogger 构造 logger。
func NewLogger(logFile, level string, alsoStderr bool) *Logger {
	if level == "" {
		level = "info"
	}
	_ = fsutil.EnsureDir(filepath.Dir(logFile), fsutil.DirMode)
	l := &Logger{logFile: logFile, level: level, alsoStderr: alsoStderr, currentDay: dateKey(time.Now())}
	l.rotateIfNeeded(time.Now())
	return l
}

func dateKey(t time.Time) string { return t.Format("2006-01-02") }

func isoLocal(t time.Time) string { return t.Format("2006-01-02T15:04:05.000Z07:00") }

func (l *Logger) enabled(level string) bool {
	lv, ok := levelOrder[level]
	if !ok {
		lv = 2
	}
	cur, ok := levelOrder[l.level]
	if !ok {
		cur = 2
	}
	return lv <= cur
}

func (l *Logger) rotateIfNeeded(now time.Time) {
	info, err := os.Stat(l.logFile)
	if err != nil {
		return
	}
	fileDay := dateKey(info.ModTime())
	if fileDay != dateKey(now) {
		_ = os.Rename(l.logFile, l.logFile+"."+fileDay)
	}
}

func (l *Logger) write(level, msg string) {
	if !l.enabled(level) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if dateKey(now) != l.currentDay {
		l.rotateIfNeeded(now)
		l.currentDay = dateKey(now)
	}
	line := isoLocal(now) + " [" + upper(level) + "] " + msg + "\n"
	if f, err := os.OpenFile(l.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_, _ = f.WriteString(line)
		_ = f.Close()
	}
	if l.alsoStderr {
		_, _ = os.Stderr.WriteString(line)
	}
}

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}

// Debug/Info/Warn/Error 写对应级别日志。
func (l *Logger) Debug(msg string) { l.write("debug", msg) }
func (l *Logger) Info(msg string)  { l.write("info", msg) }
func (l *Logger) Warn(msg string)  { l.write("warn", msg) }
func (l *Logger) Error(msg string) { l.write("error", msg) }
