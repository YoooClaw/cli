package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

var levelOrder = map[string]int{"error": 0, "warn": 1, "info": 2, "debug": 3, "trace": 4}

// logRetentionDays 是轮转出的历史日志（daemon.log.YYYY-MM-DD）保留天数。
// 不清理的话长期运行的机器会被日志慢慢填满磁盘，进而拖垮通知落盘。
const logRetentionDays = 7

// Logger 是 daemon 文件 logger，写 daemon.log 并按日期轮转为 daemon.log.YYYY-MM-DD。
// 行格式：`<ISO本地时间> [LEVEL] message`（与 logread 解析一致）。
// 文件句柄常驻（每行 open/close 在心跳、ingest 高频写下开销可观），仅在跨天
// 轮转或写失败时重开。
type Logger struct {
	logFile    string
	level      string
	alsoStderr bool
	mu         sync.Mutex
	currentDay string
	f          *os.File
}

// NewLogger 构造 logger。
func NewLogger(logFile, level string, alsoStderr bool) *Logger {
	if level == "" {
		level = "info"
	}
	_ = fsutil.EnsureDir(filepath.Dir(logFile), fsutil.DirMode)
	l := &Logger{logFile: logFile, level: level, alsoStderr: alsoStderr, currentDay: dateKey(time.Now())}
	l.rotateIfNeeded(time.Now())
	l.cleanupRotated(time.Now())
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

// rotateIfNeeded 按日期轮转；调用方须已持有 l.mu（或在构造期单线程调用），
// 且已关闭常驻句柄——rename 一个仍被持有的 fd 会让后续写落进轮转后的文件。
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

// cleanupRotated 删除超过保留期的历史日志文件。
func (l *Logger) cleanupRotated(now time.Time) {
	dir := filepath.Dir(l.logFile)
	prefix := filepath.Base(l.logFile) + "."
	cutoff := dateKey(now.AddDate(0, 0, -logRetentionDays))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) {
			continue
		}
		day := strings.TrimPrefix(name, prefix)
		if len(day) == 10 && day < cutoff {
			_ = os.Remove(filepath.Join(dir, name))
		}
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
		if l.f != nil {
			_ = l.f.Close()
			l.f = nil
		}
		l.rotateIfNeeded(now)
		l.cleanupRotated(now)
		l.currentDay = dateKey(now)
	}
	line := isoLocal(now) + " [" + upper(level) + "] " + msg + "\n"
	l.writeLineLocked(line)
	if l.alsoStderr {
		_, _ = os.Stderr.WriteString(line)
	}
}

func (l *Logger) writeLineLocked(line string) {
	if l.f == nil && !l.reopenLocked() {
		return
	}
	if _, err := l.f.WriteString(line); err != nil {
		// 句柄可能已失效（文件被外部删除/移动），重开一次再试。
		_ = l.f.Close()
		l.f = nil
		if l.reopenLocked() {
			_, _ = l.f.WriteString(line)
		}
	}
}

func (l *Logger) reopenLocked() bool {
	f, err := os.OpenFile(l.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	l.f = f
	return true
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
