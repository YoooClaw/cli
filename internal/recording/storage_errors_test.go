package recording

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func TestAudioFailureStateStaysInMemoryWhenIndexWriteFails(t *testing.T) {
	storage := newResultStorage(t)
	const id, url = "rec_full", "https://example.test/audio.m4a"
	if _, err := storage.Ingest(id, Metadata{Name: "x", OssAudioURL: url}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetResultAudioPending(id, url); err != nil {
		t.Fatal(err)
	}
	storage.writeIndex = func(string, any, os.FileMode) error {
		return fmt.Errorf("write index: %w", syscall.ENOSPC)
	}

	applied, err := storage.SetResultAudioFailedWithCode(id, url, "音频下载失败: 空间不足", "STORAGE_FULL")
	if err == nil || applied {
		t.Fatalf("index write failure = applied %v, err %v", applied, err)
	}
	// 落盘失败时仍需在内存保留失败状态，App 才能查询到 STORAGE_FULL 并停止同步。
	entry, _ := storage.FindByID(id)
	if entry.AudioStatus != AudioStatusFailed || entry.ErrorCode != "STORAGE_FULL" || entry.LastError == "" {
		t.Fatalf("failure state not visible in memory: %+v", entry)
	}
}

func TestAudioMoveFailureUpdatesInMemoryIndex(t *testing.T) {
	storage := newResultStorage(t)
	const id, url = "rec_move", "https://example.test/audio.m4a"
	if _, err := storage.Ingest(id, Metadata{Name: "x", OssAudioURL: url}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetResultAudioPending(id, url); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(storage.AudioDir(), "staged.m4a")
	if err := os.WriteFile(staged, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 目标路径被目录占住，使 os.Rename 失败。
	if err := os.Mkdir(filepath.Join(storage.AudioDir(), "blocked.m4a"), 0o700); err != nil {
		t.Fatal(err)
	}

	applied, err := storage.CommitResultAudioDownloaded(id, url, staged, "blocked.m4a")
	if err == nil || applied {
		t.Fatalf("move failure = applied %v, err %v", applied, err)
	}
	entry, _ := storage.FindByID(id)
	if entry.AudioStatus != AudioStatusFailed || entry.LastError == "" {
		t.Fatalf("move failure not visible in memory: %+v", entry)
	}
}
