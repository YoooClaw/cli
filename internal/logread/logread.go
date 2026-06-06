// Package logread 读取 daemon 日志（daemon.log + 轮转文件 daemon.log.YYYY-MM-DD）。
//
// 行格式（与 daemon logger 写入一致）：`<ISO时间> [LEVEL] message`。
package logread

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Line 是一条解析后的日志行。
type Line struct {
	Date    string `json:"date"`
	Level   string `json:"level"`
	Time    string `json:"time"`
	Message string `json:"message"`
	Raw     string `json:"raw"`
}

// Query 是日志检索条件。
type Query struct {
	Keyword string
	Level   string
	From    string
	To      string
	Limit   int
}

var lineRE = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})(T\S+)?\s+\[(\w+)\]\s+(.*)$`)
var rotatedRE = regexp.MustCompile(`\.\d{4}-\d{2}-\d{2}$`)

// logFiles 收集 current + 轮转日志路径（current 在最前，轮转按日期倒序）。
func logFiles(daemonLog string) []string {
	dir := filepath.Dir(daemonLog)
	base := filepath.Base(daemonLog)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var rotated []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, base+".") && rotatedRE.MatchString(name) {
			rotated = append(rotated, filepath.Join(dir, name))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(rotated)))
	var files []string
	if _, err := os.Stat(daemonLog); err == nil {
		files = append(files, daemonLog)
	}
	return append(files, rotated...)
}

func parseLine(raw string) *Line {
	m := lineRE.FindStringSubmatch(raw)
	if m == nil {
		return nil
	}
	return &Line{Date: m[1], Time: m[1] + m[2], Level: strings.ToLower(m[3]), Message: m[4], Raw: raw}
}

// Search 检索日志，最新行靠前，受 limit 截断。
func Search(daemonLog string, q Query) []Line {
	keyword := strings.ToLower(q.Keyword)
	level := strings.ToLower(q.Level)
	var results []Line

	for _, file := range logFiles(daemonLog) {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		lines := nonEmptyLines(string(content))
		// 单文件内反转使最新行靠前。
		for i := len(lines) - 1; i >= 0; i-- {
			if len(results) >= q.Limit {
				return results
			}
			raw := lines[i]
			parsed := parseLine(raw)
			date := ""
			if parsed != nil {
				date = parsed.Date
			}
			if q.From != "" && date != "" && date < q.From {
				continue
			}
			if q.To != "" && date != "" && date > q.To {
				continue
			}
			if level != "" && parsed != nil && parsed.Level != level {
				continue
			}
			if keyword != "" && !strings.Contains(strings.ToLower(raw), keyword) {
				continue
			}
			if parsed != nil {
				results = append(results, *parsed)
			} else {
				results = append(results, Line{Message: raw, Raw: raw})
			}
		}
	}
	return results
}

func nonEmptyLines(content string) []string {
	var out []string
	for _, l := range strings.Split(content, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
