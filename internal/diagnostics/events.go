// Package diagnostics records low-frequency lifecycle events without dumping
// command lines, configuration, environment values, or credential objects.
package diagnostics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var secret = regexp.MustCompile(`(?i)(api[_-]?key|token|authorization|password)([=:]\s*)([^\s&,;]+)`)
var bearer = regexp.MustCompile(`(?i)Bearer\s+[^\s",;]+`)
var apiKey = regexp.MustCompile(`ock-[a-zA-Z0-9_-]+`)

// SafeError is defense in depth for errors from native service commands.
// Callers must still never supply credentials, raw config or environment dumps.
func SafeError(err error) string {
	if err == nil {
		return ""
	}
	s := bearer.ReplaceAllString(err.Error(), "Bearer [redacted]")
	s = secret.ReplaceAllString(s, "${1}${2}[redacted]")
	s = apiKey.ReplaceAllString(s, "[redacted]")
	if len(s) > 2048 {
		s = s[:2048] + "…"
	}
	return s
}

func Message(event string, fields map[string]any) string {
	data := make(map[string]any, len(fields)+2)
	data["event"] = event
	data["pid"] = os.Getpid()
	for key, value := range fields {
		data[key] = value
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Sprintf("event=%q encoding_failed=true", event)
	}
	return string(raw)
}

// Write uses one append write per event. Failures go to stderr so broken log
// permissions remain visible in the service journal; diagnostics never stop work.
func Write(root, level, event string, fields map[string]any) {
	if root == "" {
		return
	}
	path := filepath.Join(root, "logs", "daemon-supervisor.log")
	line := time.Now().Format("2006-01-02T15:04:05.000Z07:00") + " [" + strings.ToUpper(level) + "] " + Message(event, fields) + "\n"
	err := os.MkdirAll(filepath.Dir(path), 0700)
	if err == nil {
		var file *os.File
		file, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			_, err = file.WriteString(line)
			_ = file.Close()
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "diagnostic log write failed path=%q: %s\n%s", path, SafeError(err), line)
	}
}
