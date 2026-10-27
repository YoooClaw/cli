package calendar

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func record(t *testing.T, raw string) Record {
	t.Helper()
	var r Record
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func replace(t *testing.T, s *Storage, p Payload) *Batch {
	t.Helper()
	b, err := Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func replaceResult(t *testing.T, s *Storage, p Payload) Result {
	t.Helper()
	b, err := Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Replace(b)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func readDay(t *testing.T, dir, date string) []Record {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, date+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		return nil
	}
	if raw[len(raw)-1] != '\n' {
		t.Fatal("JSONL must end with a newline")
	}
	var records []Record
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		records = append(records, record(t, string(line)))
	}
	return records
}

func TestReplaceGroupsCrossDayAndPreservesClientFields(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "calendars")
	s := NewStorage(dir)
	cross := record(t, `{"id":"cross","calendarName":"工作","title":"跨天日程","startAt":"2026-10-16T23:00:00+08:00","endAt":"2026-10-18T01:00:00+08:00","timeZone":"Asia/Shanghai","isAllDay":false,"description":"备注\n换行","extra":{"n":9007199254740993}}`)
	b := replace(t, s, Payload{StartDate: "2026-10-16", EndDate: "2026-10-19", CollectedAt: testCollectedAt, Items: []Record{cross}})
	if b.ItemCount() != 1 || b.DayCount() != 4 {
		t.Fatalf("counts: items=%d days=%d", b.ItemCount(), b.DayCount())
	}
	for _, day := range []string{"2026-10-16", "2026-10-17", "2026-10-18"} {
		got := readDay(t, dir, day)
		if len(got) != 1 || len(got[0]) != len(cross) {
			t.Fatalf("%s: unexpected records: %+v", day, got)
		}
		for key, value := range cross {
			if !bytes.Equal(got[0][key], value) {
				t.Fatalf("%s field %s changed: %s != %s", day, key, got[0][key], value)
			}
		}
	}
	if len(readDay(t, dir, "2026-10-19")) != 0 {
		t.Fatal("empty date must have an empty file")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "2026-10-16.jsonl"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file permissions: %v %v", info, err)
		}
	}
}

func TestReplaceRescheduleEmptySnapshotAndRetainHistory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewStorage(dir)
	for _, date := range []string{"2026-10-15", "2026-10-19"} {
		if err := os.WriteFile(filepath.Join(dir, date+".jsonl"), []byte("history\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first := record(t, `{"id":"event","startAt":"2026-10-16T09:00:00+08:00","endAt":"2026-10-16T09:30:00+08:00","timeZone":"Asia/Shanghai"}`)
	second := record(t, `{"id":"event-new-id","startAt":"2026-10-17T10:00:00+08:00","endAt":"2026-10-17T10:30:00+08:00","timeZone":"Asia/Shanghai"}`)
	p := Payload{StartDate: "2026-10-16", EndDate: "2026-10-18", CollectedAt: testCollectedAt, Items: []Record{first}}
	replace(t, s, p)
	p.Items = []Record{second}
	// 重发同一份完整快照应覆盖，不追加或依赖旧 ID。
	replace(t, s, p)
	replace(t, s, p)
	if len(readDay(t, dir, "2026-10-16")) != 0 || len(readDay(t, dir, "2026-10-17")) != 1 {
		t.Fatal("reschedule left stale or duplicate records")
	}
	p.Items = []Record{}
	replace(t, s, p)
	for _, day := range []string{"2026-10-16", "2026-10-17", "2026-10-18"} {
		if len(readDay(t, dir, day)) != 0 {
			t.Fatalf("empty snapshot did not clear %s", day)
		}
	}
	for _, date := range []string{"2026-10-15", "2026-10-19"} {
		raw, err := os.ReadFile(filepath.Join(dir, date+".jsonl"))
		if err != nil || string(raw) != "history\n" {
			t.Fatalf("out-of-range file changed: %s %v", raw, err)
		}
	}
}

func TestReplaceSerializesConcurrentBatches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewStorage(dir)
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c", "d"} {
		r := record(t, `{"id":"`+id+`","startAt":"2026-10-16T23:00:00+08:00","endAt":"2026-10-17T01:00:00+08:00","timeZone":"Asia/Shanghai"}`)
		b, err := Prepare(Payload{StartDate: "2026-10-16", EndDate: "2026-10-17", CollectedAt: testCollectedAt, Items: []Record{r}})
		if err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			if _, err := s.Replace(b); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	a := readDay(t, dir, "2026-10-16")
	b := readDay(t, dir, "2026-10-17")
	if len(a) != 1 || len(b) != 1 || !bytes.Equal(a[0]["id"], b[0]["id"]) {
		t.Fatalf("batches interleaved: %+v %+v", a, b)
	}
}

func TestReplaceSkipsStaleSnapshotAcrossRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fresh := record(t, `{"id":"fresh","startAt":"2026-10-16T09:00:00+08:00","endAt":"2026-10-16T09:30:00+08:00","timeZone":"Asia/Shanghai"}`)
	stale := record(t, `{"id":"stale","startAt":"2026-10-16T09:00:00+08:00","endAt":"2026-10-16T09:30:00+08:00","timeZone":"Asia/Shanghai"}`)
	freshPayload := Payload{StartDate: "2026-10-16", EndDate: "2026-10-16", CollectedAt: "2026-10-16T12:00:00+08:00", Items: []Record{fresh}}
	stalePayload := Payload{StartDate: "2026-10-16", EndDate: "2026-10-16", CollectedAt: "2026-10-16T11:00:00+08:00", Items: []Record{stale}}

	if result := replaceResult(t, NewStorage(dir), freshPayload); !result.Applied || result.FilesWritten != 1 {
		t.Fatalf("fresh snapshot not applied: %+v", result)
	}
	// 模拟 daemon 重启：水位从 state.json 重新加载，迟到的旧快照整批跳过。
	if result := replaceResult(t, NewStorage(dir), stalePayload); result.Applied || result.FilesWritten != 0 {
		t.Fatalf("stale snapshot applied: %+v", result)
	}
	got := readDay(t, dir, "2026-10-16")
	if len(got) != 1 || !bytes.Equal(got[0]["id"], []byte(`"fresh"`)) {
		t.Fatalf("stale snapshot overwrote newer data: %+v", got)
	}
	// 同一份快照重发（时间戳相同）必须幂等应用，不能因为「不新」被丢掉。
	if result := replaceResult(t, NewStorage(dir), freshPayload); !result.Applied {
		t.Fatalf("idempotent resend skipped: %+v", result)
	}
	raw, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil || !bytes.Contains(raw, []byte(`2026-10-16T12:00:00+08:00`)) {
		t.Fatalf("state watermark missing: %s %v", raw, err)
	}
	// 损坏的水位文件不应永久阻塞上报：视为没有历史，继续应用。
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := replaceResult(t, NewStorage(dir), stalePayload); !result.Applied {
		t.Fatalf("corrupt state blocked ingest: %+v", result)
	}
}
