package recording

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/ossdownload"
)

const (
	defaultExportTimeout = 2 * time.Hour
	exportIDBytes        = 16
)

var (
	ErrExportNotExportable   = errors.New("recording is not exportable")
	ErrExportLinkUnavailable = errors.New("export download link unavailable")
	// ErrExportObjectGone means the file service explicitly reports that the
	// export task/object no longer exists (for example 404/409 from C-005).
	// It is different from a transient signing/network failure.
	ErrExportObjectGone = errors.New("export object no longer exists")
)

type ExportStartRequest struct {
	RecordingID string
	RequestID   string
	ScopeKey    string
}

type ExportStatusRequest struct {
	RecordingID string
	ExportID    string
	ScopeKey    string
}

type ExportStartResult struct {
	RecordingID string `json:"recordingId"`
	RequestID   string `json:"requestId"`
	ExportID    string `json:"exportId"`
	State       string `json:"state"`
	SourceType  string `json:"sourceType"`
}

type ExportFailure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Scope     string `json:"scope"`
	Retryable bool   `json:"retryable"`
}

type ExportTransfer struct {
	ExpiresAt int64  `json:"expiresAt"`
	FileName  string `json:"fileName"`
	MimeType  string `json:"mimeType"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

type ExportResult struct {
	SourceVersion  string               `json:"sourceVersion"`
	WritePayload   ExportPayload        `json:"writePayload"`
	Transfer       ExportTransfer       `json:"transfer"`
	ContentPresent ExportContentPresent `json:"contentPresent"`
}

type ExportStatus struct {
	RecordingID string         `json:"recordingId"`
	ExportID    string         `json:"exportId"`
	State       string         `json:"state"`
	UpdatedAt   string         `json:"updatedAt"`
	SourceType  string         `json:"sourceType"`
	Result      *ExportResult  `json:"result"`
	Error       *ExportFailure `json:"error"`
}

// ExportUpload is what a file-service adapter reports after uploading.
type ExportUpload struct {
	TaskID    string
	ObjectKey string
	SizeBytes int64
	SignedURL string
	ExpiresAt time.Time
}

// ExportCloud is the file-service boundary. onTask is called once a task has
// been created so the export record can persist taskId/objectKey before bytes
// are uploaded.
type ExportCloud interface {
	UploadRecording(ctx context.Context, path string, onTask func(taskID, objectKey string) error) (ExportUpload, error)
	RecordingDownloadLink(ctx context.Context, taskID string) (DownloadLink, error)
}

// ExportService owns the asynchronous export state machine. It never holds the
// index lock across network or OSS operations.
type ExportService struct {
	storage *Storage
	logger  Logger
	timeout time.Duration
	now     func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	linkMu sync.Mutex
	links  map[string]*ossdownload.LinkRefresher
}

func NewExportService(storage *Storage, logger Logger) *ExportService {
	ctx, cancel := context.WithCancel(context.Background())
	return &ExportService{
		storage: storage, logger: logger, timeout: defaultExportTimeout, now: time.Now,
		ctx: ctx, cancel: cancel, links: map[string]*ossdownload.LinkRefresher{},
	}
}

// Close cancels background exports. Interrupted records are marked by Recover
// on the next daemon startup.
func (s *ExportService) Close() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
}

// RecoverInterrupted marks attempts left by a previous process.
func (s *ExportService) RecoverInterrupted() int {
	count, err := s.storage.RecoverInterruptedExports(s.stamp())
	if err != nil {
		s.logger.Error("[recording-export] 中断导出恢复失败: " + err.Error())
		return 0
	}
	if count > 0 {
		s.logger.Info(fmt.Sprintf("[recording-export] 已标记 %d 个中断导出", count))
	}
	return count
}

func (s *ExportService) stamp() string { return s.now().UTC().Format(time.RFC3339Nano) }

func (s *ExportService) Start(ctx context.Context, req ExportStartRequest, cloud ExportCloud) (ExportStartResult, error) {
	recordingID := strings.TrimSpace(req.RecordingID)
	requestID := strings.TrimSpace(req.RequestID)
	if recordingID == "" || requestID == "" || len(requestID) > 256 || strings.ContainsAny(requestID, "\r\n\x00") {
		return ExportStartResult{}, errors.New("recordingId and requestId are required")
	}
	if err := s.storage.LoadError(); err != nil {
		return ExportStartResult{}, ErrStorageUnavailable
	}
	entry, ok := s.storage.FindByID(recordingID)
	if !ok {
		return ExportStartResult{}, errors.New("Recording not found: " + recordingID)
	}
	// 幂等重放先于来源状态检查：来源后来不可读时，同一 requestId 仍应返回
	// 原 exportId 及当前状态，而不是把重试变成一次新失败。
	if existingEntry, record, found := s.storage.FindExportByRequest(req.ScopeKey, requestID); found {
		if existingEntry.ID != recordingID {
			return ExportStartResult{}, ErrExportRequestConflict
		}
		return ExportStartResult{RecordingID: recordingID, RequestID: requestID, ExportID: record.ExportID, State: record.State}, nil
	}
	if !s.storage.Exportable(entry) {
		return ExportStartResult{}, ErrExportNotExportable
	}
	if cloud == nil {
		return ExportStartResult{}, errors.New("export cloud is unavailable")
	}
	exportID, err := newExportID()
	if err != nil {
		return ExportStartResult{}, err
	}
	_, record, created, err := s.storage.BeginExport(recordingID, requestID, req.ScopeKey, exportID, s.stamp())
	if err != nil {
		return ExportStartResult{}, err
	}
	if created {
		s.wg.Add(1)
		go s.runExport(recordingID, exportID, cloud)
	}
	return ExportStartResult{RecordingID: recordingID, RequestID: requestID, ExportID: record.ExportID, State: record.State}, nil
}

func (s *ExportService) Status(ctx context.Context, req ExportStatusRequest, cloud ExportCloud) (ExportStatus, error) {
	recordingID := strings.TrimSpace(req.RecordingID)
	exportID := strings.TrimSpace(req.ExportID)
	if recordingID == "" || exportID == "" {
		return ExportStatus{}, errors.New("recordingId and exportId are required")
	}
	if err := s.storage.LoadError(); err != nil {
		return ExportStatus{}, ErrStorageUnavailable
	}
	_, record, ok := s.storage.FindExport(recordingID, exportID)
	if !ok || record.ScopeKey != req.ScopeKey {
		return ExportStatus{}, ErrExportNotFound
	}
	status := exportStatus(recordingID, record)
	switch record.State {
	case ExportStatePreparing, ExportStateUploading:
		return status, nil
	case ExportStateFailed, ExportStateExpired:
		status.Error = &ExportFailure{Code: record.ErrorCode, Message: record.ErrorMessage, Scope: record.ErrorScope, Retryable: record.Retryable}
		return status, nil
	case ExportStateReady:
	default:
		return status, nil
	}

	source, err := prepareExportSource(s.storage, recordingID, false)
	if err != nil {
		// ready 之后来源从可读变为不可读，按来源变化处理；只有明确删除才报 NOT_FOUND。
		return s.failStatus(recordingID, exportID, readySourceFailureCode(err), readySourceFailureMessage(err), "item", false), nil
	}
	if source.sourceVersion != record.SourceVersion {
		return s.failStatus(recordingID, exportID, "SOURCE_CHANGED", "Recording source changed", "item", false), nil
	}
	if cloud == nil {
		return status, ErrExportLinkUnavailable
	}
	linkState := s.linkState(record.ExportID, record.TaskID, cloud)
	// 每次 status 都是独立的刷新尝试：上一次失败不应让 ready 导出永久不可用。
	linkState.ResetError()
	link, err := linkState.Current(ctx)
	if err != nil {
		if errors.Is(err, ErrExportObjectGone) {
			return s.expireStatus(recordingID, exportID), nil
		}
		s.logger.Warn("[recording-export] 签名链接刷新失败: " + safeExportError(err) + ": " + exportID)
		return status, ErrExportLinkUnavailable
	}
	// 刷新签名可能耗时，返回前再复核一次来源；仍使用廉价文件身份和文本
	// hash，不重新计算整段音频。
	current, err := prepareExportSource(s.storage, recordingID, false)
	if err != nil {
		return s.failStatus(recordingID, exportID, readySourceFailureCode(err), readySourceFailureMessage(err), "item", false), nil
	}
	if current.sourceVersion != record.SourceVersion {
		return s.failStatus(recordingID, exportID, "SOURCE_CHANGED", "Recording source changed", "item", false), nil
	}
	payload := current.payload
	payload.OssTaskID = record.TaskID
	payload.OssURL = link.URL
	status.Result = &ExportResult{
		SourceVersion:  record.SourceVersion,
		WritePayload:   payload,
		Transfer:       ExportTransfer{ExpiresAt: link.ExpiresAt.UnixMilli(), FileName: record.FileName, MimeType: record.MimeType, SizeBytes: record.SizeBytes, SHA256: record.SHA256},
		ContentPresent: current.contentPresent,
	}
	return status, nil
}

func exportStatus(recordingID string, record ExportRecord) ExportStatus {
	return ExportStatus{RecordingID: recordingID, ExportID: record.ExportID, State: record.State, UpdatedAt: record.UpdatedAt}
}

func (s *ExportService) failStatus(recordingID, exportID, code, message, scope string, retryable bool) ExportStatus {
	s.fail(recordingID, exportID, code, message, scope, retryable)
	entry, record, ok := s.storage.FindExport(recordingID, exportID)
	if !ok {
		return ExportStatus{RecordingID: recordingID, ExportID: exportID, State: ExportStateFailed}
	}
	status := exportStatus(entry.ID, record)
	status.Error = &ExportFailure{Code: record.ErrorCode, Message: record.ErrorMessage, Scope: record.ErrorScope, Retryable: record.Retryable}
	return status
}

func (s *ExportService) expireStatus(recordingID, exportID string) ExportStatus {
	_, _, err := s.storage.UpdateExport(recordingID, exportID, s.stamp(), func(record *ExportRecord) bool {
		record.State = ExportStateExpired
		record.ErrorCode = "EXPORT_EXPIRED"
		record.ErrorMessage = "Export object no longer exists"
		record.ErrorScope = "item"
		record.Retryable = false
		return true
	})
	if err != nil {
		s.logger.Error("[recording-export] 导出过期状态写入失败: " + err.Error() + ": " + exportID)
	}
	entry, record, ok := s.storage.FindExport(recordingID, exportID)
	if !ok {
		return ExportStatus{RecordingID: recordingID, ExportID: exportID, State: ExportStateExpired}
	}
	status := exportStatus(entry.ID, record)
	status.Error = &ExportFailure{Code: record.ErrorCode, Message: record.ErrorMessage, Scope: record.ErrorScope, Retryable: record.Retryable}
	return status
}

func (s *ExportService) fail(recordingID, exportID, code, message, scope string, retryable bool) {
	if message == "" {
		message = "Recording export failed"
	}
	_, _, err := s.storage.UpdateExport(recordingID, exportID, s.stamp(), func(record *ExportRecord) bool {
		record.State = ExportStateFailed
		record.ErrorCode, record.ErrorMessage, record.ErrorScope, record.Retryable = code, message, scope, retryable
		return true
	})
	if err != nil {
		s.logger.Error("[recording-export] 导出失败状态写入失败: " + err.Error() + ": " + exportID)
	}
}

func (s *ExportService) linkState(exportID, taskID string, cloud ExportCloud) *ossdownload.LinkRefresher {
	s.linkMu.Lock()
	defer s.linkMu.Unlock()
	if state := s.links[exportID]; state != nil {
		return state
	}
	refresh := func(ctx context.Context) (DownloadLink, error) {
		return cloud.RecordingDownloadLink(ctx, taskID)
	}
	state := newDownloadLinkRefresher(DownloadLink{}, refresh)
	s.links[exportID] = state
	return state
}

func (s *ExportService) setInitialLink(exportID string, link DownloadLink, cloud ExportCloud, taskID string) {
	s.linkMu.Lock()
	defer s.linkMu.Unlock()
	refresh := func(ctx context.Context) (DownloadLink, error) {
		return cloud.RecordingDownloadLink(ctx, taskID)
	}
	s.links[exportID] = newDownloadLinkRefresher(link, refresh)
}

func (s *ExportService) runExport(recordingID, exportID string, cloud ExportCloud) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(s.ctx, s.timeout)
	defer cancel()

	source, err := prepareExportSource(s.storage, recordingID, true)
	if err != nil {
		s.failSource(recordingID, exportID, err)
		return
	}
	_, updated, err := s.storage.UpdateExport(recordingID, exportID, s.stamp(), func(record *ExportRecord) bool {
		if record.State != ExportStatePreparing {
			return false
		}
		record.SourceVersion = source.sourceVersion
		record.SizeBytes = source.sizeBytes
		record.SHA256 = source.sha256
		record.FileName = source.fileName
		record.MimeType = source.mimeType
		identity := source.identity
		record.Audio = &identity
		return true
	})
	if err != nil || !updated {
		code, message, scope, retryable := exportPersistFailure(err)
		s.fail(recordingID, exportID, code, message, scope, retryable)
		return
	}

	var taskID string
	upload, err := cloud.UploadRecording(ctx, source.audioPath, func(id, key string) error {
		taskID = id
		_, _, updateErr := s.storage.UpdateExport(recordingID, exportID, s.stamp(), func(record *ExportRecord) bool {
			if record.State != ExportStatePreparing {
				return false
			}
			record.State = ExportStateUploading
			record.TaskID, record.ObjectKey = id, key
			return true
		})
		return updateErr
	})
	if err != nil {
		code, message, scope, retryable := exportRunFailure(err)
		s.fail(recordingID, exportID, code, message, scope, retryable)
		s.logger.Warn("[recording-export] 导出失败: " + exportID + ", " + safeExportError(err))
		return
	}
	if upload.TaskID == "" || upload.ObjectKey == "" || (taskID != "" && upload.TaskID != taskID) {
		s.fail(recordingID, exportID, "EXPORT_UPLOAD_FAILED", "Recording export returned an invalid task", "item", true)
		return
	}
	if upload.SizeBytes != source.sizeBytes {
		s.fail(recordingID, exportID, "EXPORT_SIZE_MISMATCH", "Uploaded audio size does not match source", "item", false)
		return
	}
	current, verifyErr := prepareExportSource(s.storage, recordingID, false)
	if verifyErr != nil {
		s.failSource(recordingID, exportID, verifyErr)
		return
	}
	if current.sourceVersion != source.sourceVersion {
		s.fail(recordingID, exportID, "SOURCE_CHANGED", "Recording source changed", "item", false)
		return
	}
	_, updated, err = s.storage.UpdateExport(recordingID, exportID, s.stamp(), func(record *ExportRecord) bool {
		if record.State != ExportStateUploading && record.State != ExportStatePreparing {
			return false
		}
		record.State = ExportStateReady
		record.TaskID, record.ObjectKey = upload.TaskID, upload.ObjectKey
		record.SizeBytes, record.ErrorCode, record.ErrorMessage, record.ErrorScope, record.Retryable = upload.SizeBytes, "", "", "", false
		return true
	})
	if err != nil || !updated {
		code, message, scope, retryable := exportPersistFailure(err)
		s.fail(recordingID, exportID, code, message, scope, retryable)
		return
	}
	s.setInitialLink(exportID, DownloadLink{URL: upload.SignedURL, ExpiresAt: upload.ExpiresAt}, cloud, upload.TaskID)
	s.logger.Info("[recording-export] 导出就绪: " + exportID + ", recording=" + recordingID + ", bytes=" + fmt.Sprint(upload.SizeBytes))
}

func (s *ExportService) failSource(recordingID, exportID string, err error) {
	code := sourceFailureCode(err)
	s.fail(recordingID, exportID, code, sourceFailureMessage(err), "item", false)
}

func sourceFailureCode(err error) string {
	if errors.Is(err, ErrExportSourceNotFound) {
		return "SOURCE_NOT_FOUND"
	}
	if errors.Is(err, ErrExportSourceChanged) {
		return "SOURCE_CHANGED"
	}
	return "SOURCE_UNREADABLE"
}

func sourceFailureMessage(err error) string {
	switch sourceFailureCode(err) {
	case "SOURCE_NOT_FOUND":
		return "Source file no longer exists"
	case "SOURCE_CHANGED":
		return "Recording source changed"
	default:
		return "Recording source cannot be read"
	}
}

func readySourceFailureCode(err error) string {
	if errors.Is(err, ErrExportSourceNotFound) {
		return "SOURCE_NOT_FOUND"
	}
	return "SOURCE_CHANGED"
}

func readySourceFailureMessage(err error) string {
	if errors.Is(err, ErrExportSourceNotFound) {
		return "Source file no longer exists"
	}
	return "Recording source changed"
}

func exportPersistFailure(err error) (code, message, scope string, retryable bool) {
	if isStorageFullError(err) {
		return "STORAGE_FULL", "Not enough disk space", "batch", false
	}
	return "EXPORT_UPLOAD_FAILED", "Recording export could not be persisted", "item", true
}

func exportRunFailure(err error) (code, message, scope string, retryable bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "EXPORT_INTERRUPTED", "Recording export interrupted", "item", true
	}
	if isStorageFullError(err) {
		// 本地空间不足按契约停止整批，而不是只结算当前项。
		return "STORAGE_FULL", "Not enough disk space", "batch", false
	}
	return "EXPORT_UPLOAD_FAILED", safeExportError(err), "item", true
}

func safeExportError(err error) string {
	if err == nil {
		return "export failed"
	}
	text := err.Error()
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

func newExportID() (string, error) {
	raw := make([]byte, exportIDBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "export-" + hex.EncodeToString(raw), nil
}
