package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/paths"
)

func TestDataImportCommands(t *testing.T) {
	sandbox(t)
	p := paths.For("default")
	config.Save(p, config.Default(p.Credentials))
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if r.URL.Path != "/data/import" || b["ossTaskId"] != "oss-task" || b["sizeBytes"] != float64(100000000) || b["sha256"] != strings.Repeat("a", 64) {
				t.Error(b, r.URL)
			}
			w.WriteHeader(202)
			w.Write([]byte(`{"ok":true,"data":{"taskId":"job","state":"queued"}}`))
		case 2:
			if r.Method != "GET" || r.URL.Path != "/data/imports/job" {
				t.Error(r.URL)
			}
			w.Write([]byte(`{"ok":true,"data":{"taskId":"job","state":"failed"}}`))
		case 3:
			if r.Method != "POST" || r.URL.Path != "/data/imports/job/retry" {
				t.Error(r.URL)
			}
			w.Write([]byte(`{"ok":true,"data":{"taskId":"job","state":"queued"}}`))
		}
	}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	port, _ := strconv.Atoi(u.Port())
	daemon.WriteLock(p, daemon.Lock{PID: os.Getpid(), Bind: "127.0.0.1", Port: port})
	for _, args := range [][]string{{"data", "import", "--oss-task-id", "oss-task", "--size", "100000000", "--sha256", strings.Repeat("a", 64), "--tag", "audio"}, {"data", "import-status", "job"}, {"data", "import-retry", "job", "--url", "https://bucket.oss-cn-hangzhou.aliyuncs.com/new"}} {
		out, code := execCLI(t, append(args, "--format", "json")...)
		if code != 0 || decode(t, out)["ok"] != true {
			t.Fatal(out)
		}
	}
	if calls != 3 {
		t.Fatal(calls)
	}
	daemon.RemoveLock(p)
	out, code := execCLI(t, "data", "import-status", "job")
	if code == 0 || !strings.Contains(out, "DAEMON_NOT_RUNNING") {
		t.Fatal(out)
	}
}
