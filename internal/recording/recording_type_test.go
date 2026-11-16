package recording

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/YoooClaw/cli/internal/fsutil"
)

func strPtr(v string) *string { return &v }

func TestRecordingTypePersistsAndDefaultsOnRead(t *testing.T) {
	storage := newResultStorage(t)
	summary := &ResultSummary{Markdown: "test"}
	params := ResultWriteParams{
		RecordingID: "rec_type",
		Recording:   &Metadata{Name: "通话", RecordingType: strPtr(RecordingTypeCall)},
		Summary:     summary,
	}
	if _, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	entry, _ := storage.FindByID("rec_type")
	if got := NormalizeRecordingType(entry.Metadata.RecordingType); got != RecordingTypeCall {
		t.Fatalf("type=%s", got)
	}

	// 更新省略类型时保留已有 call。
	params.Recording = &Metadata{Name: "通话-改名"}
	if _, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	entry, _ = storage.FindByID("rec_type")
	if got := NormalizeRecordingType(entry.Metadata.RecordingType); got != RecordingTypeCall {
		t.Fatalf("omitted update lost type: %s", got)
	}

	// 显式 normal 覆盖。
	params.Recording = &Metadata{Name: "通话-改名", RecordingType: strPtr(RecordingTypeNormal)}
	if _, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	entry, _ = storage.FindByID("rec_type")
	if got := NormalizeRecordingType(entry.Metadata.RecordingType); got != RecordingTypeNormal {
		t.Fatalf("explicit normal not stored: %s", got)
	}
}

func TestRecordingTypeRejectsInvalidValues(t *testing.T) {
	storage := newResultStorage(t)
	for _, value := range []string{"", "CALL", "unknown", " call "} {
		t.Run(value, func(t *testing.T) {
			params := ResultWriteParams{
				RecordingID: "rec_invalid_type",
				Recording:   &Metadata{Name: "x", RecordingType: strPtr(value)},
				Summary:     &ResultSummary{Markdown: "test"},
			}
			if _, err := HandleRecordingResultWrite(params, storage, testLogger{t}, SyncOptions{}); err == nil {
				t.Fatal("invalid type accepted")
			}
			if _, ok := storage.FindByID("rec_invalid_type"); ok {
				t.Fatal("invalid request mutated storage")
			}
		})
	}
}

func TestHistoricalRecordingTypeReadsAsNormal(t *testing.T) {
	entry := Entry{Metadata: Metadata{}}
	if got := NormalizeRecordingType(entry.Metadata.RecordingType); got != RecordingTypeNormal {
		t.Fatalf("type=%s", got)
	}
}

func TestIngestPreservesRecordingTypeWhenOmitted(t *testing.T) {
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_keep_type", Metadata{Name: "x", RecordingType: strPtr(RecordingTypeCall)}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Ingest("rec_keep_type", Metadata{Name: "y"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	entry, _ := storage.FindByID("rec_keep_type")
	if got := NormalizeRecordingType(entry.Metadata.RecordingType); got != RecordingTypeCall {
		t.Fatalf("type=%s", got)
	}
}

func TestStorageDegradesWhenIndexMissingButAssetsExist(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, audioDirName), fsutil.DirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, audioDirName, "orphan.ogg"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage := NewStorage(dir, testLogger{t})
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	if storage.LoadError() == nil {
		t.Fatal("missing index with assets was not degraded")
	}
	if _, err := storage.ReadIndexSnapshot(t.Context()); err == nil {
		t.Fatal("degraded snapshot read succeeded")
	}
	if _, err := storage.Ingest("rec_new", Metadata{Name: "x"}, ""); !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("write err=%v", err)
	}
}

func TestStorageAllowsFirstUseEmptyDirectory(t *testing.T) {
	storage := NewStorage(t.TempDir(), testLogger{t})
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	if storage.LoadError() != nil {
		t.Fatalf("empty storage degraded: %v", storage.LoadError())
	}
	entries, err := storage.ReadIndexSnapshot(t.Context())
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if _, err := storage.Ingest("rec_first", Metadata{Name: "x"}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestStorageDegradesOnCorruptIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, indexFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage := NewStorage(dir, testLogger{t})
	if err := storage.Init(); err != nil {
		t.Fatal(err)
	}
	if storage.LoadError() == nil {
		t.Fatal("corrupt index was not degraded")
	}
}

func TestExportableRequiresStableStatusAndReadableAudio(t *testing.T) {
	storage := newResultStorage(t)
	entry, err := storage.Ingest("rec_export", Metadata{Name: "x", CreatedAt: "2026-06-09T20:30:00+08:00"}, "phone-a")
	if err != nil {
		t.Fatal(err)
	}
	if storage.Exportable(entry) {
		t.Fatal("entry without audio is exportable")
	}
	path := storage.AudioFilePath("rec_export", "https://example.test/audio.ogg")
	if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetAudioFile("rec_export", filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
	entry, _ = storage.FindByID("rec_export")
	if !storage.Exportable(entry) {
		t.Fatal("synced downloaded audio is not exportable")
	}
	entry.Status = StatusTranscribing
	if storage.Exportable(entry) {
		t.Fatal("transcribing entry is exportable")
	}
	entry.Status = StatusSynced
	entry.AudioFile = "../escape.ogg"
	if storage.Exportable(entry) {
		t.Fatal("path escape considered exportable")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	entry.AudioFile = filepath.Join(audioDirName, filepath.Base(path))
	if storage.Exportable(entry) {
		t.Fatal("missing audio considered exportable")
	}
}
