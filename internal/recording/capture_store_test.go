package recording

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureDownloadIndexCommitFailureIsNotReportedAsDownloaded(t *testing.T) {
	store := NewCaptureStorage(t.TempDir(), testLogger{t})
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	const id, source = "capture", "https://oss.invalid/audio.m4a"
	if _, err := store.Ingest(id, Metadata{OssAudioURL: source}, "wuying"); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(store.AudioDir(), "incoming.m4a")
	if err := os.WriteFile(staged, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := store.indexPath + ".backup"
	if err := os.Rename(store.indexPath, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.indexPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if committed, err := store.CommitResultAudioDownloaded(id, source, staged, "capture.m4a"); err == nil || committed {
		t.Fatalf("commit without index = %v, %v", committed, err)
	}
	if entry, _ := store.FindByID(id); entry.AudioStatus != AudioStatusPending || entry.AudioFile != "" {
		t.Fatalf("status leaked a download with no durable index: %+v", entry)
	}
	if err := os.Remove(store.indexPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, store.indexPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if committed, err := store.CommitResultAudioDownloaded(id, source, staged, "capture.m4a"); err != nil || !committed {
		t.Fatalf("retry commit = %v, %v", committed, err)
	}
	if records := ReadIndex(store.dir); len(records) != 1 || records[0].AudioStatus != AudioStatusDownloaded {
		t.Fatalf("retry did not persist Capture download: %+v", records)
	}
}
