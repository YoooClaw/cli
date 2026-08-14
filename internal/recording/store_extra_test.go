package recording

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func newStore(t *testing.T) *Storage {
	t.Helper()
	s := NewStorage(filepath.Join(t.TempDir(), "recordings"), testLogger{t})
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	return s
}

func meta(name, created string) Metadata {
	return Metadata{Name: name, CreatedAt: created, DurationSec: 1, OssAudioURL: "https://oss.invalid/a.ogg"}
}

func TestStoreIngestListFindRename(t *testing.T) {
	s := newStore(t)
	if _, err := s.Ingest("r1", meta("一", "2026-06-01T00:00:00Z"), "phone-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest("r2", meta("二", "2026-06-03T00:00:00Z"), ""); err != nil {
		t.Fatal(err)
	}
	all := s.ListAll()
	if len(all) != 2 || all[0].ID != "r2" {
		t.Fatalf("ListAll order wrong: %+v", all)
	}
	if all[0].Metadata.DurationDisplay != "1s" {
		t.Fatalf("Ingest duration_display = %q, want %q", all[0].Metadata.DurationDisplay, "1s")
	}
	if got, ok := s.FindByID("r1"); !ok || got.ClientLabel != "phone-a" {
		t.Errorf("FindByID r1: %+v ok=%v", got, ok)
	}
	if _, ok := s.FindByID("ghost"); ok {
		t.Error("missing id should be false")
	}
	entry, ok, err := s.Rename("r1", "新名字")
	if err != nil || !ok || entry.Metadata.Name != "新名字" {
		t.Errorf("rename: %+v ok=%v err=%v", entry, ok, err)
	}
	if _, ok, _ := s.Rename("ghost", "x"); ok {
		t.Error("rename missing should be false")
	}
}

func TestStoreListByStatus(t *testing.T) {
	s := newStore(t)
	s.Ingest("r1", meta("a", "2026-06-01T00:00:00Z"), "p")
	synced := s.ListByStatus(StatusSynced)
	if len(synced) != 1 {
		t.Errorf("expected 1 synced, got %d", len(synced))
	}
	if got := s.ListByStatus(StatusTranscribed); len(got) != 0 {
		t.Errorf("expected 0 transcribed, got %d", len(got))
	}
}

func TestSortByCreatedDescUsesActualInstantAcrossTimezones(t *testing.T) {
	earlier := "2026-07-29T11:02:00+08:00"
	later := "2026-07-29T05:47:00Z"
	if earlier <= later {
		t.Fatal("test fixture must reproduce the old lexical ordering bug")
	}
	entries := []Entry{
		{ID: "earlier-local", Metadata: Metadata{CreatedAt: earlier}},
		{ID: "later-utc", Metadata: Metadata{CreatedAt: later}},
	}

	SortByCreatedDesc(entries)

	if entries[0].ID != "later-utc" {
		t.Fatalf("expected actual latest recording first, got %+v", entries)
	}
}

func TestParseRecordingTimeSupportsLegacyFormats(t *testing.T) {
	cases := []string{
		"2026-07-29T14:34:22",
		"2026-07-29 14:34:22",
		"2026-07-29 14:34:22.123",
		"2026-07-29 14:34:22+08:00",
		"2026-07-29 14:34:22 +0800",
	}
	for _, input := range cases {
		if _, ok := parseRecordingTime(input); !ok {
			t.Errorf("expected legacy time %q to parse", input)
		}
	}
}

func TestSortByCreatedDescFallsBackToIngestedAt(t *testing.T) {
	entries := []Entry{
		{
			ID:         "valid-created",
			Metadata:   Metadata{CreatedAt: "2026-07-29T05:47:00Z"},
			IngestedAt: "2026-07-29T05:48:00Z",
		},
		{
			ID:         "invalid-created",
			Metadata:   Metadata{CreatedAt: "not-a-time"},
			IngestedAt: "2026-07-29T06:34:22Z",
		},
	}

	SortByCreatedDesc(entries)

	if entries[0].ID != "invalid-created" {
		t.Fatalf("expected ingestedAt fallback to determine latest, got %+v", entries)
	}
}

func TestSortByCreatedDescKeepsInvalidTimestampsStable(t *testing.T) {
	entries := []Entry{
		{ID: "first", Metadata: Metadata{CreatedAt: "invalid"}},
		{ID: "second"},
	}

	SortByCreatedDesc(entries)

	if entries[0].ID != "first" || entries[1].ID != "second" {
		t.Fatalf("unparseable entries must keep their index order, got %+v", entries)
	}
}

func TestStoreSetFilesAndTitle(t *testing.T) {
	s := newStore(t)
	s.Ingest("r1", meta("a", "2026-06-01T00:00:00Z"), "p")
	if err := s.SetTranscriptDataFile("r1", "r1.json"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTranscriptFile("r1", "r1.md"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSummaryFile("r1", "r1.md"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitle("r1", "  我的标题  "); err != nil {
		t.Fatal(err)
	}
	got, _ := s.FindByID("r1")
	if got.TranscriptDataFile != "transcript-data/r1.json" || got.TranscriptFile != "transcripts/r1.md" ||
		got.SummaryFile != "summaries/r1.md" || got.Title != "我的标题" {
		t.Errorf("set files/title wrong: %+v", got)
	}
	// missing id
	if err := s.SetTitle("ghost", "x"); err != os.ErrNotExist {
		t.Errorf("set on missing id should be ErrNotExist, got %v", err)
	}
}

func TestStoreTracksAudioSourceAcrossFailedReplacement(t *testing.T) {
	t.Parallel()
	storage := newStore(t)
	const firstURL = "https://oss.invalid/first.ogg"
	const secondURL = "https://oss.invalid/second.ogg"
	_, _ = storage.Ingest("r1", Metadata{Name: "x", CreatedAt: "t", OssAudioURL: firstURL}, "")
	audioPath := storage.AudioFilePath("r1", firstURL)
	if err := os.WriteFile(audioPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetAudioFile("r1", AudioFilename("r1", firstURL)); err != nil {
		t.Fatal(err)
	}

	if err := storage.SetResultAudioPending("r1", secondURL); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.SetResultAudioFailed("r1", secondURL, "音频下载失败: 404"); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetResultAudioPending("r1", firstURL); err != nil {
		t.Fatal(err)
	}
	entry, _ := storage.FindByID("r1")
	if entry.Metadata.OssAudioURL != firstURL || entry.AudioSourceURL != firstURL ||
		entry.AudioStatus != AudioStatusDownloaded || entry.LastError != "" {
		t.Fatalf("known-good source was not restored: %+v", entry)
	}
}

func TestStoreDelete(t *testing.T) {
	s := newStore(t)
	s.Ingest("r1", meta("a", "2026-06-01T00:00:00Z"), "p")
	// 放一个真实音频文件，验证删除会清理引用文件
	audioRel := "audio/r1.ogg"
	if err := os.WriteFile(filepath.Join(s.dir, audioRel), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.updateEntry("r1", func(e *Entry) { e.AudioFile = audioRel })

	// localOnly: 保留索引、清文件引用
	ok, err := s.Delete("r1", true)
	if err != nil || !ok {
		t.Fatalf("local delete: ok=%v err=%v", ok, err)
	}
	got, _ := s.FindByID("r1")
	if got.AudioFile != "" {
		t.Error("localOnly delete should clear audioFile")
	}
	if _, err := os.Stat(filepath.Join(s.dir, audioRel)); !os.IsNotExist(err) {
		t.Error("audio file should be removed")
	}

	// 全删
	ok, _ = s.Delete("r1", false)
	if !ok {
		t.Error("full delete should report true")
	}
	if _, found := s.FindByID("r1"); found {
		t.Error("entry should be gone")
	}
	if ok, _ := s.Delete("ghost", false); ok {
		t.Error("delete missing should be false")
	}
}

func TestStoreClose(t *testing.T) {
	s := newStore(t)
	if err := s.Close(); err != nil {
		t.Errorf("Close should be nil: %v", err)
	}
}

func TestReadIndexAndEvents(t *testing.T) {
	s := newStore(t)
	s.Ingest("r1", meta("a", "2026-06-01T00:00:00Z"), "p")
	idx := ReadIndex(s.dir)
	if len(idx) != 1 || idx[0].ID != "r1" {
		t.Errorf("ReadIndex: %+v", idx)
	}
	if ReadIndex(filepath.Join(t.TempDir(), "nope")) != nil {
		t.Error("missing index -> nil")
	}

	eventsPath := filepath.Join(t.TempDir(), "events.jsonl")
	os.WriteFile(eventsPath, []byte("{\"type\":\"a\"}\n\nnot-json\n{\"type\":\"b\"}\n"), 0o600)
	events := ReadEvents(eventsPath)
	if len(events) != 2 || events[0]["type"] != "a" || events[1]["type"] != "b" {
		t.Errorf("ReadEvents should skip blank/bad lines: %+v", events)
	}
	if ReadEvents(filepath.Join(t.TempDir(), "none.jsonl")) != nil {
		t.Error("missing events -> nil")
	}
}

func TestLoadIndexRecoversTranscribing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 写一个 transcribing 状态的索引，Init 应把它改成 transcribe_failed
	os.WriteFile(filepath.Join(dir, "index.json"),
		[]byte(`{"recordings":[{"id":"r1","status":"transcribing","metadata":{"name":"x","created_at":"2026-06-01T00:00:00Z"}}]}`), 0o644)
	s := NewStorage(dir, testLogger{t})
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	got, _ := s.FindByID("r1")
	if got.Status != StatusTranscribeFailed || got.LastError == "" {
		t.Errorf("interrupted transcribing should recover to failed: %+v", got)
	}
}

func TestLoadIndexMarksMissingAudioForRecovery(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "recordings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{
	  "recordings": [{
	    "id": "r1",
	    "status": "transcribed",
	    "audioFile": "audio/r1.ogg",
	    "metadata": {
	      "name": "x",
	      "created_at": "2026-06-01T00:00:00Z",
	      "oss_audio_url": "https://oss.invalid/r1.ogg"
	    }
	  }]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	storage := NewStorage(dir, testLogger{t})
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	entry, _ := storage.FindByID("r1")
	if entry.AudioFile != "" || entry.AudioStatus != AudioStatusPending || entry.LastError == "" {
		t.Fatalf("missing audio was not queued for recovery: %+v", entry)
	}
	if got := storage.ListMissingAudio(); len(got) != 1 || got[0].ID != "r1" {
		t.Fatalf("missing audio list: %+v", got)
	}
}

func TestArtifactFilenameIsTimeFirst(t *testing.T) {
	t.Parallel()
	// 用 time.Date 独立算出期望值，不复用 artifactFilename 自己的解析逻辑，
	// 同时不依赖跑测试的机器处在哪个时区。
	stamp := time.Date(2026, 6, 9, 20, 30, 0, 0, time.FixedZone("CST", 8*3600)).
		In(time.Local).Format("2006010215")

	cases := []struct {
		name      string
		id        string
		title     string
		createdAt string
		want      string
	}{
		{"完整字段", "rec_1", "产品方案讨论", "2026-06-09T20:30:00+08:00", stamp + "_产品方案讨论_rec_1.md"},
		{"无标题退化", "rec_1", "  ", "2026-06-09T20:30:00+08:00", stamp + "_rec_1.md"},
		{"时间不可解析退化", "rec_1", "产品方案讨论", "not-a-time", "产品方案讨论_rec_1.md"},
		{"两者都缺退回裸 ID", "rec_1", "", "", "rec_1.md"},
		{"非法字符被剥掉", "rec_1", `a/b\c:d*e?f"g<h>i|j`, "2026-06-09T20:30:00+08:00", stamp + "_abcdefghij_rec_1.md"},
		{"控制字符被剥掉", "rec_1", "上半\n下半\t尾", "2026-06-09T20:30:00+08:00", stamp + "_上半下半尾_rec_1.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TranscriptFilename(c.id, c.title, c.createdAt); got != c.want {
				t.Fatalf("TranscriptFilename = %q, want %q", got, c.want)
			}
			if got := SummaryFilename(c.id, c.title, c.createdAt); got != c.want {
				t.Fatalf("SummaryFilename = %q, want %q", got, c.want)
			}
		})
	}
}

func TestArtifactFilenameStaysWithinNameLimit(t *testing.T) {
	t.Parallel()
	// 全 CJK 标题按码点截断到 60，且整体不越过 255 字节的文件名上限。
	name := TranscriptFilename(
		"1f2e3d4c-1234-5678-9abc-def012345678",
		strings.Repeat("会", 200),
		"2026-06-09T20:30:00+08:00",
	)
	title := strings.TrimSuffix(name, ".md")
	title = title[strings.Index(title, "_")+1 : strings.LastIndex(title, "_")]
	if got := len([]rune(title)); got != 60 {
		t.Fatalf("title truncated to %d code points, want 60", got)
	}
	if len(name) > 255 {
		t.Fatalf("filename is %d bytes, over the 255-byte limit: %s", len(name), name)
	}
}

func TestSanitizeFilenameDoesNotSplitMultibyte(t *testing.T) {
	t.Parallel()
	// 按字节截断会把 4 字节字符切成半个，产生非法 UTF-8；按码点截断不会。
	got := sanitizeFilename(strings.Repeat("🎧", 80))
	if !utf8.ValidString(got) {
		t.Fatalf("sanitized title is not valid UTF-8: %q", got)
	}
	if n := len([]rune(got)); n != 60 {
		t.Fatalf("got %d code points, want 60", n)
	}
}
