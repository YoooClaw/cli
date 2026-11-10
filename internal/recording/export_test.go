package recording

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fakeExportCloud struct {
	mu           sync.Mutex
	uploads      int
	links        int
	block        chan struct{}
	started      chan struct{}
	duringUpload func()
	duringLink   func()
	linkErr      error
	uploadErr    error
	linkURL      string
}

func (f *fakeExportCloud) UploadRecording(ctx context.Context, path string, onTask func(string, string) error) (ExportUpload, error) {
	f.mu.Lock()
	f.uploads++
	f.mu.Unlock()
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	info, statErr := os.Stat(path)
	if onTask != nil {
		if err := onTask("file-task-1", "users/audio/20261110/0123456789abcdef0123456789abcdef.m4a"); err != nil {
			return ExportUpload{}, err
		}
	}
	if f.duringUpload != nil {
		f.duringUpload()
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ExportUpload{}, ctx.Err()
		}
	}
	if f.uploadErr != nil {
		return ExportUpload{}, f.uploadErr
	}
	if statErr != nil {
		return ExportUpload{}, statErr
	}
	expires := time.Now().Add(time.Hour)
	return ExportUpload{
		TaskID: "file-task-1", ObjectKey: "users/audio/20261110/0123456789abcdef0123456789abcdef.m4a",
		SizeBytes: info.Size(), SignedURL: "https://bucket.oss-cn-hangzhou.aliyuncs.com/a.m4a?signature=s",
		ExpiresAt: expires,
	}, nil
}

func (f *fakeExportCloud) RecordingDownloadLink(ctx context.Context, taskID string) (DownloadLink, error) {
	f.mu.Lock()
	f.links++
	duringLink := f.duringLink
	f.mu.Unlock()
	if duringLink != nil {
		duringLink()
	}
	if f.linkErr != nil {
		return DownloadLink{}, f.linkErr
	}
	url := f.linkURL
	if url == "" {
		url = "https://bucket.oss-cn-hangzhou.aliyuncs.com/a.m4a?signature=refreshed"
	}
	return DownloadLink{URL: url, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (f *fakeExportCloud) counts() (uploads, links int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uploads, f.links
}

func newExportFixture(t *testing.T) (*Storage, *ExportService, *fakeExportCloud) {
	t.Helper()
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_export", Metadata{Name: "方案评审", CreatedAt: "2026-11-09T10:00:00+08:00", DurationSec: 120}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_export",
		Transcript: &ResultTranscript{
			Text: "已有转写正文", Brief: "简述",
			Segments: []ResultTranscriptSegment{{Text: "已有转写正文", StartMS: ptrF(0), EndMS: ptrF(3000)}},
		},
		Summary: &ResultSummary{Markdown: "已有总结正文"},
	}, storage, testLogger{t}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	path := storage.AudioFilePath("rec_export", "https://example.test/audio.m4a")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 1024)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetAudioFile("rec_export", path[len(storage.AudioDir())+1:]); err != nil {
		t.Fatal(err)
	}
	service := NewExportService(storage, testLogger{t})
	t.Cleanup(service.Close)
	return storage, service, &fakeExportCloud{}
}

func startExport(t *testing.T, service *ExportService, cloud ExportCloud, recordingID, requestID, scope string) ExportStartResult {
	t.Helper()
	result, err := service.Start(context.Background(), ExportStartRequest{RecordingID: recordingID, RequestID: requestID, ScopeKey: scope}, cloud)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func waitExport(t *testing.T, service *ExportService, cloud ExportCloud, recordingID, exportID string) ExportStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, err := service.Status(context.Background(), ExportStatusRequest{RecordingID: recordingID, ExportID: exportID, ScopeKey: "local"}, cloud)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == ExportStateReady || status.State == ExportStateFailed || status.State == ExportStateExpired {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("export did not finish: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExportReadyBuildsWritePayload(t *testing.T) {
	_, service, cloud := newExportFixture(t)
	start := startExport(t, service, cloud, "rec_export", "req-1", "local")
	if start.State != ExportStatePreparing || start.ExportID == "" {
		t.Fatalf("start=%+v", start)
	}
	status := waitExport(t, service, cloud, "rec_export", start.ExportID)
	if status.State != ExportStateReady || status.Result == nil {
		t.Fatalf("status=%+v", status)
	}
	result := status.Result
	if result.WritePayload.RecordingID != "rec_export" || result.WritePayload.OssTaskID != "file-task-1" || result.WritePayload.OssURL == "" {
		t.Fatalf("payload=%+v", result.WritePayload)
	}
	if result.WritePayload.DurationMillis == nil || *result.WritePayload.DurationMillis != 120000 {
		t.Fatalf("durationMillis=%v", result.WritePayload.DurationMillis)
	}
	if result.WritePayload.Recording.RecordingType != RecordingTypeNormal {
		t.Fatalf("recording_type=%s", result.WritePayload.Recording.RecordingType)
	}
	if result.WritePayload.Transcript == nil || result.WritePayload.Transcript.Text != "已有转写正文" || len(result.WritePayload.Transcript.Segments) != 1 {
		t.Fatalf("transcript=%+v", result.WritePayload.Transcript)
	}
	if result.WritePayload.Summary == nil || !strings.Contains(result.WritePayload.Summary.Markdown, "已有总结正文") {
		t.Fatalf("summary=%+v", result.WritePayload.Summary)
	}
	if !result.ContentPresent.Transcript || !result.ContentPresent.Summary {
		t.Fatalf("contentPresent=%+v", result.ContentPresent)
	}
	if result.Transfer.ExpiresAt <= time.Now().UnixMilli() || result.Transfer.SizeBytes != 1024 || result.Transfer.SHA256 == "" {
		t.Fatalf("transfer=%+v", result.Transfer)
	}
	if result.Transfer.FileName != "rec_export.m4a" || result.Transfer.MimeType != "audio/mp4" {
		t.Fatalf("transfer=%+v", result.Transfer)
	}
}

func TestExportStartIsIdempotentAndConflicts(t *testing.T) {
	storage, service, cloud := newExportFixture(t)
	if _, err := storage.Ingest("other", Metadata{Name: "other", CreatedAt: "2026-11-09T10:00:00+08:00"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	otherPath := storage.AudioFilePath("other", "https://example.test/other.m4a")
	if err := os.WriteFile(otherPath, []byte("other audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetAudioFile("other", otherPath[len(storage.AudioDir())+1:]); err != nil {
		t.Fatal(err)
	}

	cloud.block = make(chan struct{})
	cloud.started = make(chan struct{}, 1)
	first := startExport(t, service, cloud, "rec_export", "same-request", "local")
	<-cloud.started

	again := startExport(t, service, cloud, "rec_export", "same-request", "local")
	if again.ExportID != first.ExportID {
		t.Fatalf("idempotent start changed exportId: %+v vs %+v", again, first)
	}
	if uploads, _ := cloud.counts(); uploads != 1 {
		t.Fatalf("uploads=%d", uploads)
	}
	if _, err := service.Start(context.Background(), ExportStartRequest{RecordingID: "other", RequestID: "same-request", ScopeKey: "local"}, cloud); err != ErrExportRequestConflict {
		t.Fatalf("conflict err=%v", err)
	}

	// Same recording with a new requestId while the first is still in flight.
	if _, err := service.Start(context.Background(), ExportStartRequest{RecordingID: "rec_export", RequestID: "new-request", ScopeKey: "local"}, cloud); err != ErrExportBusy {
		t.Fatalf("busy err=%v", err)
	}

	close(cloud.block)
	waitExport(t, service, cloud, "rec_export", first.ExportID)
}

func TestExportScopeIsolation(t *testing.T) {
	_, service, cloud := newExportFixture(t)
	start := startExport(t, service, cloud, "rec_export", "req-shared", "label:phone-a")
	waitExportWithScope(t, service, cloud, "rec_export", start.ExportID, "label:phone-a")
	if _, err := service.Status(context.Background(), ExportStatusRequest{RecordingID: "rec_export", ExportID: start.ExportID, ScopeKey: "label:phone-b"}, cloud); err != ErrExportNotFound {
		t.Fatalf("cross-scope status err=%v", err)
	}
	// Same requestId in another scope is allowed and creates a separate export.
	other := startExport(t, service, cloud, "rec_export", "req-shared", "label:phone-b")
	if other.ExportID == start.ExportID {
		t.Fatal("scopes shared an export attempt")
	}
	waitExportWithScope(t, service, cloud, "rec_export", other.ExportID, "label:phone-b")
}

func waitExportWithScope(t *testing.T, service *ExportService, cloud ExportCloud, recordingID, exportID, scope string) ExportStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, err := service.Status(context.Background(), ExportStatusRequest{RecordingID: recordingID, ExportID: exportID, ScopeKey: scope}, cloud)
		if err != nil {
			t.Fatal(err)
		}
		if status.State != ExportStatePreparing && status.State != ExportStateUploading {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("export did not finish: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExportReadyDetectsSourceChangeAndDeletion(t *testing.T) {
	storage, service, cloud := newExportFixture(t)
	start := startExport(t, service, cloud, "rec_export", "req-1", "local")
	waitExport(t, service, cloud, "rec_export", start.ExportID)

	if _, _, err := storage.Rename("rec_export", "改名后"); err != nil {
		t.Fatal(err)
	}
	status := waitExport(t, service, cloud, "rec_export", start.ExportID)
	if status.State != ExportStateFailed || status.Error == nil || status.Error.Code != "SOURCE_CHANGED" || status.Result != nil {
		t.Fatalf("status=%+v", status)
	}

	// A fresh export then loses its audio file.
	second := startExport(t, service, cloud, "rec_export", "req-2", "local")
	waitExport(t, service, cloud, "rec_export", second.ExportID)
	if err := os.Remove(storage.AudioFilePath("rec_export", "https://example.test/audio.m4a")); err != nil {
		t.Fatal(err)
	}
	status = waitExport(t, service, cloud, "rec_export", second.ExportID)
	if status.State != ExportStateFailed || status.Error == nil || status.Error.Code != "SOURCE_NOT_FOUND" {
		t.Fatalf("status=%+v", status)
	}
}

func TestExportAudioOnlyPayload(t *testing.T) {
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_audio", Metadata{Name: "纯音频", CreatedAt: "2026-11-09T10:00:00+08:00", RecordingType: strPtr(RecordingTypeCall)}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	path := storage.AudioFilePath("rec_audio", "https://example.test/audio.ogg")
	if err := os.WriteFile(path, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetAudioFile("rec_audio", "rec_audio.ogg"); err != nil {
		// SetAudioFile uses the file's basename; write it with the actual name instead.
		t.Fatal(err)
	}
	service := NewExportService(storage, testLogger{t})
	t.Cleanup(service.Close)
	cloud := &fakeExportCloud{}
	start := startExport(t, service, cloud, "rec_audio", "req-a", "local")
	status := waitExport(t, service, cloud, "rec_audio", start.ExportID)
	if status.Result == nil {
		t.Fatalf("status=%+v", status)
	}
	if status.Result.ContentPresent.Transcript || status.Result.ContentPresent.Summary {
		t.Fatalf("contentPresent=%+v", status.Result.ContentPresent)
	}
	if status.Result.WritePayload.Transcript == nil || status.Result.WritePayload.Summary == nil {
		t.Fatalf("audio-only must keep empty DTO objects: %+v", status.Result.WritePayload)
	}
	if status.Result.WritePayload.Recording.RecordingType != RecordingTypeCall {
		t.Fatalf("recording_type=%s", status.Result.WritePayload.Recording.RecordingType)
	}
}

func forceLinkRefresh(t *testing.T, service *ExportService, exportID string) {
	t.Helper()
	service.linkMu.Lock()
	state := service.links[exportID]
	service.linkMu.Unlock()
	if state == nil {
		t.Fatalf("missing link state for %s", exportID)
	}
	state.mu.Lock()
	state.link.ExpiresAt = time.Now().Add(time.Minute)
	state.mu.Unlock()
}

func TestExportLinkRefreshFailureKeepsReady(t *testing.T) {
	_, service, cloud := newExportFixture(t)
	start := startExport(t, service, cloud, "rec_export", "req-1", "local")
	waitExport(t, service, cloud, "rec_export", start.ExportID)
	// Force the cached signature close to expiry so status must refresh it.
	forceLinkRefresh(t, service, start.ExportID)
	cloud.mu.Lock()
	cloud.linkErr = context.DeadlineExceeded
	cloud.mu.Unlock()
	if _, err := service.Status(context.Background(), ExportStatusRequest{RecordingID: "rec_export", ExportID: start.ExportID, ScopeKey: "local"}, cloud); err != ErrExportLinkUnavailable {
		t.Fatalf("err=%v", err)
	}
	_, record, ok := service.storage.FindExport("rec_export", start.ExportID)
	if !ok || record.State != ExportStateReady {
		t.Fatalf("link failure changed state: %+v", record)
	}
	cloud.mu.Lock()
	cloud.linkErr = nil
	cloud.mu.Unlock()
	if status, err := service.Status(context.Background(), ExportStatusRequest{RecordingID: "rec_export", ExportID: start.ExportID, ScopeKey: "local"}, cloud); err != nil || status.Result == nil {
		t.Fatalf("recovered status=%+v err=%v", status, err)
	}
}

func TestExportExpiresWhenLinkTaskGone(t *testing.T) {
	_, service, cloud := newExportFixture(t)
	start := startExport(t, service, cloud, "rec_export", "req-1", "local")
	waitExport(t, service, cloud, "rec_export", start.ExportID)
	forceLinkRefresh(t, service, start.ExportID)
	cloud.mu.Lock()
	cloud.linkErr = ErrExportObjectGone
	cloud.mu.Unlock()

	status, err := service.Status(context.Background(), ExportStatusRequest{RecordingID: "rec_export", ExportID: start.ExportID, ScopeKey: "local"}, cloud)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if status.State != ExportStateExpired || status.Result != nil || status.Error == nil ||
		status.Error.Code != "EXPORT_EXPIRED" || status.Error.Scope != "item" || status.Error.Retryable {
		t.Fatalf("status=%+v", status)
	}
}

func TestExportReadyRechecksSourceAfterLinkRefresh(t *testing.T) {
	for name, mutate := range map[string]func(*Storage){
		"changed": func(storage *Storage) {
			if _, _, err := storage.Rename("rec_export", "改名后"); err != nil {
				t.Fatal(err)
			}
		},
		"deleted": func(storage *Storage) {
			if err := os.Remove(storage.AudioFilePath("rec_export", "https://example.test/audio.m4a")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			storage, service, cloud := newExportFixture(t)
			start := startExport(t, service, cloud, "rec_export", "req-1", "local")
			waitExport(t, service, cloud, "rec_export", start.ExportID)
			forceLinkRefresh(t, service, start.ExportID)
			cloud.mu.Lock()
			cloud.duringLink = func() { mutate(storage) }
			cloud.mu.Unlock()

			status, err := service.Status(context.Background(), ExportStatusRequest{RecordingID: "rec_export", ExportID: start.ExportID, ScopeKey: "local"}, cloud)
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			want := "SOURCE_CHANGED"
			if name == "deleted" {
				want = "SOURCE_NOT_FOUND"
			}
			if status.State != ExportStateFailed || status.Result != nil || status.Error == nil || status.Error.Code != want {
				t.Fatalf("status=%+v", status)
			}
		})
	}
}

func TestExportRecoverInterrupted(t *testing.T) {
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_export", Metadata{Name: "x", CreatedAt: "2026-11-09T10:00:00+08:00"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	_, record, _, err := storage.BeginExport("rec_export", "req-1", "local", "export-1", "2026-11-10T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	service := NewExportService(storage, testLogger{t})
	t.Cleanup(service.Close)
	if count := service.RecoverInterrupted(); count != 1 {
		t.Fatalf("recovered=%d", count)
	}
	_, got, ok := storage.FindExport("rec_export", record.ExportID)
	if !ok || got.State != ExportStateFailed || got.ErrorCode != "EXPORT_INTERRUPTED" || got.ErrorScope != "item" {
		t.Fatalf("record=%+v", got)
	}
}

func TestExportStartRejectsUnreadableSource(t *testing.T) {
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_export", Metadata{Name: "x", CreatedAt: "2026-11-09T10:00:00+08:00"}, "phone-a"); err != nil {
		t.Fatal(err)
	}
	service := NewExportService(storage, testLogger{t})
	t.Cleanup(service.Close)
	if _, err := service.Start(context.Background(), ExportStartRequest{RecordingID: "rec_export", RequestID: "r", ScopeKey: "local"}, &fakeExportCloud{}); err != ErrExportNotExportable {
		t.Fatalf("err=%v", err)
	}
}

func TestExportStartReplaysBeforeSourceCheck(t *testing.T) {
	storage, service, cloud := newExportFixture(t)
	first := startExport(t, service, cloud, "rec_export", "req-replay", "local")
	waitExport(t, service, cloud, "rec_export", first.ExportID)
	if err := os.Remove(storage.AudioFilePath("rec_export", "https://example.test/audio.m4a")); err != nil {
		t.Fatal(err)
	}

	replay, err := service.Start(context.Background(), ExportStartRequest{RecordingID: "rec_export", RequestID: "req-replay", ScopeKey: "local"}, cloud)
	if err != nil {
		t.Fatalf("replay err=%v", err)
	}
	if replay.ExportID != first.ExportID || replay.State != ExportStateReady {
		t.Fatalf("replay=%+v first=%+v", replay, first)
	}
	if uploads, _ := cloud.counts(); uploads != 1 {
		t.Fatalf("replay triggered another upload: %d", uploads)
	}
}

func TestExportUploadTimeSourceChange(t *testing.T) {
	audioPath := func(storage *Storage) string {
		return storage.AudioFilePath("rec_export", "https://example.test/audio.m4a")
	}
	for name, tc := range map[string]struct {
		mutate func(t *testing.T, storage *Storage)
		want   string
	}{
		"audio content changed": {func(t *testing.T, storage *Storage) {
			if err := os.WriteFile(audioPath(storage), []byte("changed audio bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "SOURCE_CHANGED"},
		"audio removed": {func(t *testing.T, storage *Storage) {
			if err := os.Remove(audioPath(storage)); err != nil {
				t.Fatal(err)
			}
		}, "SOURCE_NOT_FOUND"},
		"summary changed": {func(t *testing.T, storage *Storage) {
			entry, _ := storage.FindByID("rec_export")
			if err := os.WriteFile(storage.dir+"/"+entry.SummaryFile, []byte("changed summary"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "SOURCE_CHANGED"},
	} {
		t.Run(name, func(t *testing.T) {
			storage, service, cloud := newExportFixture(t)
			cloud.block = make(chan struct{})
			cloud.started = make(chan struct{}, 1)
			start := startExport(t, service, cloud, "rec_export", "req-1", "local")
			<-cloud.started
			tc.mutate(t, storage)
			close(cloud.block)
			status := waitExport(t, service, cloud, "rec_export", start.ExportID)
			if status.State != ExportStateFailed || status.Error == nil || status.Error.Code != tc.want {
				t.Fatalf("status=%+v", status)
			}
		})
	}
}

func TestExportUploadFailureMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		err       error
		code      string
		scope     string
		retryable bool
	}{
		"disk full": {fmt.Errorf("write: %w", syscall.ENOSPC), "STORAGE_FULL", "batch", false},
		"network":   {errors.New("network boom"), "EXPORT_UPLOAD_FAILED", "item", true},
	} {
		t.Run(name, func(t *testing.T) {
			_, service, cloud := newExportFixture(t)
			cloud.uploadErr = tc.err
			start := startExport(t, service, cloud, "rec_export", "req-1", "local")
			status := waitExport(t, service, cloud, "rec_export", start.ExportID)
			if status.State != ExportStateFailed || status.Error == nil || status.Error.Code != tc.code ||
				status.Error.Retryable != tc.retryable || status.Error.Scope != tc.scope {
				t.Fatalf("status=%+v", status)
			}
		})
	}
}

func TestExportStartRollsBackWhenInitialStateCannotPersist(t *testing.T) {
	storage, service, cloud := newExportFixture(t)
	storage.writeIndex = func(string, any, os.FileMode) error {
		return fmt.Errorf("write index: %w", syscall.ENOSPC)
	}
	if _, err := service.Start(context.Background(), ExportStartRequest{
		RecordingID: "rec_export", RequestID: "req-1", ScopeKey: "local",
	}, cloud); err == nil {
		t.Fatal("start must fail when preparing state cannot be persisted")
	}
	storage.writeIndex = nil

	// 同一 requestId 重试必须重新创建并真正执行，而不是重放内存幽灵记录。
	start := startExport(t, service, cloud, "rec_export", "req-1", "local")
	status := waitExport(t, service, cloud, "rec_export", start.ExportID)
	if status.State != ExportStateReady {
		t.Fatalf("status=%+v", status)
	}
	if uploads, _ := cloud.counts(); uploads != 1 {
		t.Fatalf("uploads=%d", uploads)
	}
}

func TestExportIndexWriteDiskFullStopsBatch(t *testing.T) {
	for name, failAt := range map[string]int{
		"initial persist": 2, // 1=BeginExport, 2=runExport 首次写 sourceVersion
		"ready persist":   4, // 3=onTask, 4=ready 落盘
	} {
		t.Run(name, func(t *testing.T) {
			storage, service, cloud := newExportFixture(t)
			calls := 0
			storage.writeIndex = func(string, any, os.FileMode) error {
				calls++
				if calls >= failAt {
					return fmt.Errorf("write index: %w", syscall.ENOSPC)
				}
				return nil
			}
			start := startExport(t, service, cloud, "rec_export", "req-1", "local")
			status := waitExport(t, service, cloud, "rec_export", start.ExportID)
			if status.State != ExportStateFailed || status.Error == nil ||
				status.Error.Code != "STORAGE_FULL" || status.Error.Scope != "batch" || status.Error.Retryable {
				t.Fatalf("status=%+v", status)
			}
		})
	}
}

func TestExportReadySurvivesRestartAndRefreshesLink(t *testing.T) {
	storage, service, cloud := newExportFixture(t)
	start := startExport(t, service, cloud, "rec_export", "req-1", "local")
	waitExport(t, service, cloud, "rec_export", start.ExportID)
	service.Close()

	restarted := NewExportService(storage, testLogger{t})
	t.Cleanup(restarted.Close)
	if count := restarted.RecoverInterrupted(); count != 0 {
		t.Fatalf("ready export was treated as interrupted: %d", count)
	}
	status, err := restarted.Status(context.Background(), ExportStatusRequest{RecordingID: "rec_export", ExportID: start.ExportID, ScopeKey: "local"}, cloud)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != ExportStateReady || status.Result == nil || status.Result.WritePayload.OssURL == "" {
		t.Fatalf("status=%+v", status)
	}
	if _, links := cloud.counts(); links == 0 {
		t.Fatal("restart did not re-sign the ready export")
	}
}

func TestExportStatusNeverCreatesTasks(t *testing.T) {
	storage, service, cloud := newExportFixture(t)
	start := startExport(t, service, cloud, "rec_export", "req-1", "local")
	waitExport(t, service, cloud, "rec_export", start.ExportID)
	uploads, _ := cloud.counts()
	for i := 0; i < 3; i++ {
		if _, err := service.Status(context.Background(), ExportStatusRequest{RecordingID: "rec_export", ExportID: start.ExportID, ScopeKey: "local"}, cloud); err != nil {
			t.Fatal(err)
		}
	}
	if after, _ := cloud.counts(); after != uploads {
		t.Fatalf("status created uploads: %d -> %d", uploads, after)
	}
	if _, err := service.Status(context.Background(), ExportStatusRequest{RecordingID: "rec_export", ExportID: "export-missing", ScopeKey: "local"}, cloud); err != ErrExportNotFound {
		t.Fatalf("err=%v", err)
	}
	entry, _ := storage.FindByID("rec_export")
	if len(entry.Exports) != 1 {
		t.Fatalf("status created export records: %+v", entry.Exports)
	}
}

func TestExportReadyRejectsReplacedUnsupportedSource(t *testing.T) {
	storage, service, cloud := newExportFixture(t)
	start := startExport(t, service, cloud, "rec_export", "req-1", "local")
	waitExport(t, service, cloud, "rec_export", start.ExportID)
	replaced := storage.dir + "/audio/replaced.txt"
	if err := os.WriteFile(replaced, []byte("not audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := storage.updateEntry("rec_export", func(entry *Entry) {
		entry.AudioFile = "audio/replaced.txt"
	}); err != nil {
		t.Fatal(err)
	}
	status := waitExport(t, service, cloud, "rec_export", start.ExportID)
	if status.State != ExportStateFailed || status.Error == nil || status.Error.Code != "SOURCE_CHANGED" {
		t.Fatalf("status=%+v", status)
	}
}
