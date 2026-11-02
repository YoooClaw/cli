package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/data"
	"github.com/YoooClaw/cli/internal/paths"
)

func TestDataCLIPutReadAndOffline(t *testing.T) {
	sandbox(t)
	p := paths.For("default")
	cfg := config.Default(p.Credentials)
	config.Save(p, cfg)
	store := data.New(p.Data, cfg.Data)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		result, e := store.Put(r.Body, data.Metadata{Name: q.Get("name"), Title: q.Get("title"), Tags: q["tag"], Source: q.Get("source"), Meta: json.RawMessage(q.Get("meta")), MIME: r.Header.Get("Content-Type")}, "local")
		if e != nil {
			t.Error(e)
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(result)
	}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	port, _ := strconv.Atoi(u.Port())
	daemon.WriteLock(p, daemon.Lock{PID: os.Getpid(), Bind: "127.0.0.1", Port: port})
	meta := filepath.Join(t.TempDir(), "meta.json")
	os.WriteFile(meta, []byte(`{"owner":"test"}`), 0600)
	out, code := execCLI(t, "data", "put", "--text", "hello", "--meta", "@"+meta, "--tag", "work", "--format", "json")
	if code != 0 {
		t.Fatal(out)
	}
	id := decode(t, out)["data"].(map[string]any)["id"].(string)
	raw := []byte{0, 255, 1, 13, 10}
	root := newRootCmd()
	root.SetIn(bytes.NewReader(raw))
	root.SetOut(io.Discard)
	root.SetArgs([]string{"data", "put", "-", "--name", "raw.bin"})
	old := os.Stdout
	rd, wr, _ := os.Pipe()
	os.Stdout = wr
	exitCode = 0
	e := root.Execute()
	wr.Close()
	os.Stdout = old
	b, _ := io.ReadAll(rd)
	rd.Close()
	if e != nil || exitCode != 0 {
		t.Fatal(e, string(b))
	}
	binaryID := decode(t, string(b))["data"].(map[string]any)["id"].(string)
	daemon.RemoveLock(p)
	ts.Close()
	out, code = execCLI(t, "data", "show", id, "--format", "json")
	if code != 0 {
		t.Fatal(out)
	}
	out, code = execCLI(t, "data", "list", "--tag", "work", "--format", "json")
	if code != 0 || len(decode(t, out)["items"].([]any)) != 1 {
		t.Fatal(out)
	}
	dest := filepath.Join(t.TempDir(), "copy.bin")
	out, code = execCLI(t, "data", "get", binaryID, "--out", dest)
	got, _ := os.ReadFile(dest)
	if code != 0 || !bytes.Equal(raw, got) {
		t.Fatal(out, got)
	}
	root = newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"data", "cat", binaryID})
	exitCode = 0
	if e = root.Execute(); e != nil || exitCode != 0 || !bytes.Equal(buf.Bytes(), raw) {
		t.Fatal(e, buf.Bytes())
	}
	buf.Reset()
	root.SetArgs([]string{"data", "path", binaryID})
	if e = root.Execute(); e != nil || !filepath.IsAbs(strings.TrimSpace(buf.String())) {
		t.Fatal(e, buf.String())
	}
	out, code = execCLI(t, "data", "put", "--text", "offline")
	if code == 0 || !strings.Contains(out, "YOOOCLAW_DAEMON_NOT_RUNNING") {
		t.Fatal(out)
	}
	out, code = execCLI(t, "data", "rm", id)
	if code == 0 || !strings.Contains(out, "CONFIRMATION_REQUIRED") {
		t.Fatal(out)
	}
}
func TestDataTTYDetection(t *testing.T) {
	f, e := os.OpenFile(os.DevNull, os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if !dataOutputTTY(f) {
		t.Fatal("character device must be guarded")
	}
	if dataOutputTTY(&bytes.Buffer{}) {
		t.Fatal("pipe must allow bytes")
	}
}
