package contextevent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func sample(label string) Event {
	return Event{Source: "data", Operation: "append", Scope: Scope{Kind: "client", ClientLabel: label}, Result: Result{CommittedCount: 1}}
}
func openTest(t *testing.T, dir string, opts Options) *Log {
	t.Helper()
	l, e := Open(dir, opts)
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func TestReplayRestartIsolationAndWake(t *testing.T) {
	dir := t.TempDir()
	l := openTest(t, dir, Options{})
	page, wake, e := l.Read("", 10, "a")
	if e != nil {
		t.Fatal(e)
	}
	start := page.HighWatermark
	first, e := l.Append(sample("a"))
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-wake:
	default:
		t.Fatal("lost wake")
	}
	if _, e = l.Append(sample("b")); e != nil {
		t.Fatal(e)
	}
	l = openTest(t, dir, Options{})
	page, _, e = l.Read(start, 10, "a")
	if e != nil || len(page.Events) != 1 || page.Events[0].EventID != first.EventID || page.NextCursor != page.HighWatermark {
		t.Fatalf("page=%+v err=%v", page, e)
	}
	page, _, e = l.Read(page.NextCursor, 10, "a")
	if e != nil || len(page.Events) != 0 {
		t.Fatal(page, e)
	}
	if _, _, e = l.Read("bad", 10, ""); !errors.Is(e, ErrCursor) {
		t.Fatal(e)
	}
}
func TestRetentionSizeAndCorruptRecovery(t *testing.T) {
	now := time.Now()
	opts := Options{Now: func() time.Time { return now }, Retention: time.Hour, SegmentBytes: 1, MaxBytes: 1000}
	dir := t.TempDir()
	l := openTest(t, dir, opts)
	p, _, _ := l.Read("", 10, "")
	old := p.HighWatermark
	for i := 0; i < 8; i++ {
		if _, e := l.Append(sample("a")); e != nil {
			t.Fatal(e)
		}
	}
	p, _, e := l.Read(old, 10, "")
	if !errors.Is(e, ErrExpired) {
		t.Fatal(e)
	}
	var total int64
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, f := range files {
		st, _ := os.Stat(f)
		total += st.Size()
	}
	if total > 1000 {
		t.Fatal(total)
	}
	// A torn final record is trimmed without changing the generation.
	tail := files[len(files)-1]
	f, _ := os.OpenFile(tail, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(`{"torn":`)
	f.Close()
	l = openTest(t, dir, opts)
	if _, _, e = l.Read(p.HighWatermark, 10, ""); e != nil {
		t.Fatal(e)
	}
	// Complete malformed records invalidate the generation explicitly.
	f, _ = os.OpenFile(tail, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("bad\n")
	f.Close()
	l = openTest(t, dir, opts)
	if _, _, e = l.Read(p.HighWatermark, 10, ""); !errors.Is(e, ErrExpired) {
		t.Fatal(e)
	}
	ev, e := l.Append(sample("a"))
	if e != nil {
		t.Fatal(e)
	}
	now = now.Add(2 * time.Hour)
	if _, _, e = l.Read(ev.Cursor, 10, ""); e != nil {
		t.Fatal(e)
	} // cursor at floor can continue
	l = openTest(t, dir, opts)
	p, _, e = l.Read("", 10, "")
	if e != nil || len(p.Events) != 0 {
		t.Fatal(p, e)
	}
}
func TestConcurrentPublishAndFailedWrite(t *testing.T) {
	l := openTest(t, t.TempDir(), Options{})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := l.Append(sample("a")); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	p, _, e := l.Read("", 100, "")
	if e != nil || len(p.Events) != 50 {
		t.Fatal(len(p.Events), e)
	}
	// Force a metadata write error without relying on root-sensitive chmod behavior.
	if e = os.Remove(filepath.Join(l.dir, "state.json")); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(filepath.Join(l.dir, "state.json"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = l.Append(sample("a")); e == nil {
		t.Fatal("expected persistence failure")
	}
	p, _, _ = l.Read("", 100, "")
	if len(p.Events) != 50 {
		t.Fatal("failed write became visible")
	}
}

func TestDateHintsBoundedAndSurviveReplay(t *testing.T) {
	dir := t.TempDir()
	log := openTest(t, dir, Options{})
	ev := sample("a")
	ev.Details = Details{ReadScope: "dates", Revision: "opaque-revision"}
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 104; i >= 0; i-- {
		ev.ChangedDates = append(ev.ChangedDates, start.AddDate(0, 0, i).Format("2006-01-02"))
	}
	ev.ChangedDates = append(ev.ChangedDates, ev.ChangedDates[0])
	original := append([]string(nil), ev.ChangedDates...)
	got, err := log.Append(ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ChangedDates) != 100 || !got.DatesTruncated || got.ChangedDates[0] != "2020-01-01" {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(original, ev.ChangedDates) {
		t.Fatal("modified caller slice")
	}
	reopened := openTest(t, dir, Options{})
	page, _, err := reopened.Read("", 100, "a")
	if err != nil || len(page.Events) != 1 || !reflect.DeepEqual(page.Events[0].Details, got.Details) {
		t.Fatal(page, err)
	}
	raw, _ := json.Marshal(got)
	var fields map[string]any
	json.Unmarshal(raw, &fields)
	if fields["readScope"] != "dates" || fields["revision"] != "opaque-revision" || fields["datesTruncated"] != true {
		t.Fatal(string(raw))
	}
}
