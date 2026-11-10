package recording

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestStorageFullClassification(t *testing.T) {
	for name, err := range map[string]error{
		"syscall": fmt.Errorf("write index: %w", syscall.ENOSPC),
		"unix":    errors.New("write: no space left on device"),
		"windows": errors.New("There is not enough space on the disk."),
		"quota":   errors.New("quota exceeded"),
		"chinese": errors.New("磁盘空间不足"),
	} {
		t.Run(name, func(t *testing.T) {
			if got := storageErrorCode(err); got != "STORAGE_FULL" {
				t.Fatalf("storageErrorCode(%v)=%q", err, got)
			}
		})
	}
	if got := storageErrorCode(os.ErrPermission); got != "" {
		t.Fatalf("permission misclassified: %q", got)
	}
	if got := storageErrorCode(errors.New("disc full")); got != "" {
		t.Fatalf("unrelated error misclassified: %q", got)
	}
}

func TestRecordingErrorCodePersistsAndClears(t *testing.T) {
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_full", Metadata{Name: "x"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	const url = "https://example.test/audio.m4a"
	if err := storage.SetResultAudioPending("rec_full", url); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.SetResultAudioFailedWithCode("rec_full", url, "音频下载失败: 空间不足", "STORAGE_FULL"); err != nil {
		t.Fatal(err)
	}
	entry, _ := storage.FindByID("rec_full")
	if entry.AudioStatus != AudioStatusFailed || entry.ErrorCode != "STORAGE_FULL" || entry.LastError == "" {
		t.Fatalf("entry=%+v", entry)
	}

	// 重启后错误码仍可查询。
	reloaded := NewStorage(storage.dir, testLogger{t})
	if err := reloaded.Init(); err != nil {
		t.Fatal(err)
	}
	entry, _ = reloaded.FindByID("rec_full")
	if entry.ErrorCode != "STORAGE_FULL" {
		t.Fatalf("reloaded errorCode=%q", entry.ErrorCode)
	}

	// 新尝试开始清除旧错误码。
	if err := reloaded.SetResultAudioPending("rec_full", url); err != nil {
		t.Fatal(err)
	}
	entry, _ = reloaded.FindByID("rec_full")
	if entry.ErrorCode != "" {
		t.Fatalf("new attempt kept errorCode=%q", entry.ErrorCode)
	}
}
