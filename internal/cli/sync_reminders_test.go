package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/assets"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/reminders"
)

func TestReminderMemoryCLI(t *testing.T) {
	sandbox(t)
	p := paths.For("default")
	store := reminders.NewStorage(p.Reminders)
	payload := reminders.Payload{SchemaVersion: 1, SourceID: "synthetic", CollectedAt: "2026-10-23T10:00:00+08:00", Items: []reminders.Item{}}
	for i := 0; i < 45; i++ {
		payload.Items = append(payload.Items, reminders.Item{Summary: reminders.Summary{ID: fmt.Sprint(i), Title: "Synthetic task", ListID: "list", ListName: "Tasks", IsCompleted: i >= 14}})
	}
	if _, err := store.Replace(payload); err != nil {
		t.Fatal(err)
	}
	runReminderMemoryCLI(t, p, store, 45, func() {
		payload.CollectedAt = "2026-10-23T11:00:00+08:00"
		payload.Items[0].Title = "Updated synthetic task"
		payload.Items[0].IsCompleted = true
		if _, err := store.Replace(payload); err != nil {
			t.Fatal(err)
		}
	}, func() {
		payload.CollectedAt = "2026-10-23T12:00:00+08:00"
		if _, err := store.Replace(payload); err != nil {
			t.Fatal(err)
		}
	})
}

// Optional private sample is copied into a temporary profile, never committed or
// logged. Its content and record count are not embedded in this regression.
func TestReminderMemoryPrivateSample(t *testing.T) {
	src := os.Getenv("YOOOCLAW_TEST_REMINDERS_DIR")
	if src == "" {
		t.Skip("no private fixture")
	}
	sandbox(t)
	p := paths.For("default")
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(src, path)
		if e != nil {
			return e
		}
		dest := filepath.Join(p.Reminders, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		return os.WriteFile(dest, raw, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	store := reminders.NewStorage(p.Reminders)
	var count int
	var payload reminders.Payload
	if err = store.View(func(idx reminders.Index, state reminders.State, detail func(string) (reminders.Record, error)) error {
		count = len(idx.Items)
		if count == 0 {
			return fmt.Errorf("sample is empty")
		}
		entries := append([]reminders.Entry(nil), idx.Items...)
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsCompleted != entries[j].IsCompleted {
				return !entries[i].IsCompleted
			}
			return entries[i].RecordID < entries[j].RecordID
		})
		source := entries[0].SourceID
		when, e := time.Parse(time.RFC3339Nano, state.Sources[source].LastCollectedAt)
		if e != nil {
			return e
		}
		payload = reminders.Payload{SchemaVersion: 1, SourceID: source, CollectedAt: when.Add(time.Second).Format(time.RFC3339Nano), Items: []reminders.Item{}}
		for _, entry := range entries {
			if entry.SourceID == source {
				r, e := detail(entry.RecordID)
				if e != nil {
					return e
				}
				payload.Items = append(payload.Items, r.Item)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runReminderMemoryCLI(t, p, store, count, func() {
		payload.Items[0].IsCompleted = !payload.Items[0].IsCompleted
		note := "isolated validation update"
		payload.Items[0].Notes = &note
		if _, e := store.Replace(payload); e != nil {
			t.Fatal(e)
		}
	}, func() {
		when, _ := time.Parse(time.RFC3339Nano, payload.CollectedAt)
		payload.CollectedAt = when.Add(time.Second).Format(time.RFC3339Nano)
		if _, e := store.Replace(payload); e != nil {
			t.Fatal(e)
		}
	})

}

func runReminderMemoryCLI(t *testing.T, p paths.Paths, store *reminders.Storage, count int, mutate, resync func()) {
	t.Helper()
	svc := assets.New("isolated", nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := assets.ReminderRoute(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		out, e := svc.ReadReminders(context.Background(), store, id, r.URL.Query())
		if e != nil {
			w.WriteHeader(e.Status)
			_ = json.NewEncoder(w).Encode(e)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	cfg := config.Default(p.Credentials)
	cfg.Daemon.Bind = u.Hostname()
	cfg.Daemon.Port = port
	if err := config.Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) map[string]any {
		t.Helper()
		out, code := execCLI(t, append(args, "--format", "json")...)
		if code != 0 {
			t.Fatalf("CLI failed, code %d", code)
		}
		return decode(t, out)
	}
	next := func() map[string]any { return call("sync", "next", "--source", "reminders") }
	scan := call("sync", "scan", "--source", "reminders")
	if scan["totalPending"] != float64(count) {
		t.Fatal("scan count")
	}
	call("reminders", "list", "--status", "all")
	if _, e := os.Stat(filepath.Join(p.State, "memory-sync", "reminders.json")); !os.IsNotExist(e) {
		t.Fatal("read-only query or scan persisted progress")
	}
	first := next()
	if first["returned"] != float64(20) {
		t.Fatal("expected 20-item first batch")
	}
	if _, code := execCLI(t, "sync", "next", "--source", "reminders", "--date", "1900-01-01", "--format", "json"); code == 0 {
		t.Fatal("pending scope switched")
	}
	if next()["token"] != first["token"] {
		t.Fatal("pending retry changed token")
	}
	if mutate != nil {
		mutate()
		if next()["token"] != first["token"] {
			t.Fatal("upstream update changed frozen batch")
		}
	}
	seen := map[string]bool{}
	batches := 0
	total := 0
	revisions := 0
	batch := first
	for batch["done"] != true {
		batches++
		if batches > 20 {
			t.Fatal("failed to drain")
		}
		items := batch["items"].([]any)
		if len(items) > 20 {
			t.Fatal("batch exceeds 20")
		}
		for _, raw := range items {
			item := raw.(map[string]any)
			key := item["id"].(string) + ":" + item["version"].(string)
			if seen[key] {
				t.Fatal("version consumed twice")
			}
			seen[key] = true
			total++
			if item["previousVersion"] != nil {
				revisions++
				if item["previousData"] == nil {
					t.Fatal("missing previous state")
				}
			}
		}
		token := batch["token"].(string)
		call("sync", "commit", "--source", "reminders", "--token", token)
		call("sync", "commit", "--source", "reminders", "--token", token)
		batch = next()
	}
	want := count
	if mutate != nil {
		want++
	}
	if total != want {
		t.Fatalf("got %d records, expected %d", total, want)
	}
	if mutate != nil && revisions != 1 {
		t.Fatal("concurrent revision missing")
	}
	if resync != nil {
		resync()
	}
	if next()["done"] != true {
		t.Fatal("unchanged content reconsumed")
	}
	t.Logf("consumed=%d batches=%d revisions=%d; retries and repeated commit passed; drained", total, batches, revisions)
}

func TestReminderMemoryRejectsMixedRevision(t *testing.T) {
	calls := 0
	_, err := readReminderMemory(func(_, _ string, _ any) (int, any, error) {
		calls++
		if calls == 1 {
			return 200, assets.ReminderResponse{Response: assets.Response{OK: true, Items: []assets.Item{{RecordID: reminders.RecordID("source", "one")}}}, Revision: "first"}, nil
		}
		return 410, map[string]any{"ok": false}, nil
	})
	if err == nil || calls != 2 {
		t.Fatal("accepted a changed snapshot")
	}
}
