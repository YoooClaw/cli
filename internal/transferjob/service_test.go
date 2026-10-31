package transferjob

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func request(id string) Request { return Request{id, strings.Repeat("a", 32)} }
func wait(t *testing.T, f func() bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met")
}
func deps() Dependencies {
	return Dependencies{Identity: func(string) string { return "identity" }, Timeout: time.Minute, Run: func(context.Context, Request, string, func(string) error) error { return nil }, Report: func(context.Context, string, Event) error { return nil }}
}
func TestAsyncDedupConflictDeadlineAndRestart(t *testing.T) {
	dir := t.TempDir()
	d := deps()
	release := make(chan struct{})
	started := make(chan struct{})
	var runs atomic.Int32
	d.Run = func(ctx context.Context, _ Request, _ string, emit func(string) error) error {
		runs.Add(1)
		close(started)
		<-release
		return emit("DOWNLOAD_COMPLETED")
	}
	s, e := New(dir, d)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Accept(request("one"), "main"); e != nil {
		t.Fatal(e)
	}
	<-started
	raw, _ := os.ReadFile(filepath.Join(dir, "one.json"))
	if e = s.Accept(request("one"), "main"); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "one.json"))
	if string(raw) != string(after) {
		t.Fatal("duplicate rewrote persisted deadline")
	}
	changed := request("one")
	changed.FileTaskID = strings.Repeat("b", 32)
	if !errors.Is(s.Accept(changed, "main"), ErrConflict) {
		t.Fatal("conflicting file accepted")
	}
	if !errors.Is(s.Accept(request("two"), "main"), ErrBusy) {
		t.Fatal("parallel import accepted")
	}
	close(release)
	wait(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.jobs["one"].Reported })
	s.Stop()
	s, e = New(dir, d)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	if e = s.Accept(request("one"), "main"); e != nil {
		t.Fatal(e)
	}
	if runs.Load() != 1 {
		t.Fatal("restarted import")
	}
}

type denied struct{}

func (denied) Error() string   { return "denied" }
func (denied) Permanent() bool { return true }
func TestUnreportedResultDoesNotBlockNextImport(t *testing.T) {
	d := deps()
	var runs atomic.Int32
	d.Run = func(context.Context, Request, string, func(string) error) error { runs.Add(1); return nil }
	d.Report = func(_ context.Context, _ string, e Event) error {
		if e.EventType == "IMPORT_SUCCEEDED" {
			return denied{}
		}
		return nil
	}
	s, e := New(t.TempDir(), d)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	if e = s.Accept(request("one"), "main"); e != nil {
		t.Fatal(e)
	}
	wait(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.jobs["one"].Result != nil })
	if e = s.Accept(request("two"), "main"); e != nil {
		t.Fatal(e)
	}
	wait(t, func() bool { return runs.Load() == 2 })
}
func TestInterruptedJobReportsFailureWithoutReimport(t *testing.T) {
	dir := t.TempDir()
	d := deps()
	s, e := New(dir, d)
	if e != nil {
		t.Fatal(e)
	}
	s.Stop()
	j := &record{Request: request("interrupted"), Label: "main", Identity: "identity", State: "running", Deadline: time.Now().Add(time.Hour)}
	if e = s.save(j); e != nil {
		t.Fatal(e)
	}
	var runs atomic.Int32
	events := make(chan Event, 1)
	d.Run = func(context.Context, Request, string, func(string) error) error { runs.Add(1); return nil }
	d.Report = func(_ context.Context, _ string, e Event) error { events <- e; return nil }
	s, e = New(dir, d)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	select {
	case event := <-events:
		if event.EventType != "IMPORT_FAILED" {
			t.Fatal(event)
		}
	case <-time.After(time.Second):
		t.Fatal("no recovered result")
	}
	if runs.Load() != 0 {
		t.Fatal("interrupted import replayed")
	}
}
func TestOneDeadlineStopsWorkAndPersistsFailure(t *testing.T) {
	d := deps()
	d.Timeout = 20 * time.Millisecond
	d.Run = func(ctx context.Context, _ Request, _ string, _ func(string) error) error {
		<-ctx.Done()
		return ctx.Err()
	}
	s, e := New(t.TempDir(), d)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	s.Accept(request("timeout"), "main")
	wait(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		j := s.jobs["timeout"]
		return j.Reported && j.Result.EventType == "IMPORT_FAILED"
	})
}
func TestChangedIdentityNeverReportsOrImportsOldJob(t *testing.T) {
	dir := t.TempDir()
	d := deps()
	s, e := New(dir, d)
	if e != nil {
		t.Fatal(e)
	}
	s.Stop()
	j := &record{Request: request("old"), Label: "main", Identity: "old-account", State: "queued", Deadline: time.Now().Add(time.Hour)}
	s.save(j)
	var calls atomic.Int32
	d.Run = func(context.Context, Request, string, func(string) error) error { calls.Add(1); return nil }
	d.Report = func(context.Context, string, Event) error { calls.Add(1); return nil }
	s, e = New(dir, d)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	wait(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.jobs["old"].Result != nil })
	if calls.Load() != 0 {
		t.Fatal("old task used replacement account")
	}
}

func TestLifecycleLogsCorrelateJobsAndRedactProviderErrors(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			var mu sync.Mutex
			var lines []string
			log := func(line string) { mu.Lock(); defer mu.Unlock(); lines = append(lines, line) }
			d := deps()
			d.Info = log
			d.Warn = log
			d.Run = func(_ context.Context, _ Request, _ string, emit func(string) error) error {
				if failed {
					return errors.New("https://private.example/?token=SECRET")
				}
				return emit("DOWNLOAD_COMPLETED")
			}
			s, err := New(t.TempDir(), d)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Accept(request("log_job"), "main"); err != nil {
				t.Fatal(err)
			}
			wait(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.jobs["log_job"].Reported })
			if err = s.Accept(request("log_job"), "main"); err != nil {
				t.Fatal(err)
			}
			s.Stop()
			mu.Lock()
			text := strings.Join(lines, "\n")
			mu.Unlock()
			for _, needle := range []string{"transferId=log_job", "import.accepted", "import.started", "event.reported", "import.finished", "import.duplicate"} {
				if !strings.Contains(text, needle) {
					t.Fatalf("missing %s: %s", needle, text)
				}
			}
			if failed && !strings.Contains(text, "import.failed code=TRANSFER_IMPORT_FAILED") {
				t.Fatal(text)
			}
			if strings.Contains(text, "SECRET") || strings.Contains(text, "private.example") {
				t.Fatal("provider data leaked")
			}
		})
	}
}

func TestStartupLogsShowDurableJobRecoveryWithoutReexecution(t *testing.T) {
	dir := t.TempDir()
	var runs atomic.Int32
	d := deps()
	d.Run = func(context.Context, Request, string, func(string) error) error { runs.Add(1); return nil }
	first, err := New(dir, d)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Accept(request("restore_log"), "main"); err != nil {
		first.Stop()
		t.Fatal(err)
	}
	wait(t, func() bool { first.mu.Lock(); defer first.mu.Unlock(); return first.jobs["restore_log"].Reported })
	first.Stop()
	var lines []string
	var mu sync.Mutex
	d.Info = func(line string) { mu.Lock(); defer mu.Unlock(); lines = append(lines, line) }
	restored, err := New(dir, d)
	if err != nil {
		t.Fatal(err)
	}
	restored.Stop()
	mu.Lock()
	text := strings.Join(lines, "\n")
	mu.Unlock()
	for _, want := range []string{"transferId=restore_log import.recovered state=done reported=true", "import.jobs_loaded count=1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s: %s", want, text)
		}
	}
	if runs.Load() != 1 {
		t.Fatal("recovered successful task was run again")
	}
}
