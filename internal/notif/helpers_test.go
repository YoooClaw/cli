package notif

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// timeNowISO 返回当前本地时间的 RFC3339 串（用于 prune 等"今天"场景）。
func timeNowISO() string {
	return time.Now().Format(time.RFC3339)
}

// writeDateFile 在通知目录写一个 YYYY-MM-DD.json 文件。
func writeDateFile(t *testing.T, dir, dateKey string, items []StoredNotification) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, dateKey+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
