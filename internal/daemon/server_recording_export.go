package daemon

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/YoooClaw/cli/internal/clientlabel"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/transfercloud"
)

// exportServiceFor returns the export service bound to one recording source.
// Each source owns a separate index, so export records must stay in the same
// store as the recording they belong to.
func (s *server) exportServiceFor(store *recording.Storage) *recording.ExportService {
	s.exportOnce.Do(func() {
		if s.recordingStorage != nil {
			s.recordingExports = recording.NewExportService(s.recordingStorage, s.logger)
			s.recordingExports.RecoverInterrupted()
		}
		if s.captureRecordingStorage != nil {
			s.captureRecordingExports = recording.NewExportService(s.captureRecordingStorage, s.logger)
			s.captureRecordingExports.RecoverInterrupted()
		}
	})
	if store != nil && store == s.captureRecordingStorage {
		return s.captureRecordingExports
	}
	return s.recordingExports
}

func (s *server) initRecordingExports() {
	_ = s.exportServiceFor(nil)
}

func (s *server) closeRecordingExports() {
	for _, service := range []*recording.ExportService{s.recordingExports, s.captureRecordingExports} {
		if service != nil {
			service.Close()
		}
	}
}

func (s *server) recordingExportScope(auth authResult) string {
	scope := strings.TrimSpace(auth.scope())
	if scope == "" {
		return "local"
	}
	return "label:" + scope
}

type boundRecordingExportCloud struct {
	key, host string
	valid     func() bool
}

func (c *boundRecordingExportCloud) UploadRecording(ctx context.Context, path string, onTask func(taskID, objectKey string) error) (recording.ExportUpload, error) {
	if !c.valid() {
		return recording.ExportUpload{}, errors.New("recording export credentials missing or changed")
	}
	receipt, err := transfercloud.New(c.key, c.host).UploadRecordingWithTask(ctx, path, onTask)
	if !c.valid() {
		return recording.ExportUpload{}, errors.New("recording export credentials changed")
	}
	if err != nil {
		return recording.ExportUpload{}, err
	}
	return recording.ExportUpload{
		TaskID: receipt.TaskID, ObjectKey: receipt.ObjectKey, SizeBytes: receipt.FileSize,
		SignedURL: receipt.SignedURL, ExpiresAt: receipt.ExpiresAt,
	}, nil
}

func (c *boundRecordingExportCloud) RecordingDownloadLink(ctx context.Context, taskID string) (recording.DownloadLink, error) {
	if !c.valid() {
		return recording.DownloadLink{}, errors.New("recording export credentials missing or changed")
	}
	next, err := transfercloud.New(c.key, c.host).RecordingDownloadLink(ctx, taskID)
	if !c.valid() {
		return recording.DownloadLink{}, errors.New("recording export credentials changed")
	}
	if err != nil {
		// C-005 用 404/409 表示任务不存在或已删除；只把这个明确结果映射为
		// expired，网络/HTTP 暂时失败仍保留 ready。
		if transfercloud.IsTaskGone(err) {
			return recording.DownloadLink{}, recording.ErrExportObjectGone
		}
		return recording.DownloadLink{}, err
	}
	return recording.DownloadLink{URL: next.URL, ExpiresAt: next.ExpiresAt}, nil
}

func (s *server) recordingExportCloud(label string) (recording.ExportCloud, error) {
	if s.exportCloudFn != nil {
		return s.exportCloudFn(label)
	}
	key := s.recordingKeyForLabel(label)
	if key == "" {
		return nil, errors.New("recording export credentials missing")
	}
	host := config.ResolveCloudHost(s.cfg)
	valid := func() bool {
		return key != "" && s.recordingKeyForLabel(label) == key && config.ResolveCloudHost(s.cfg) == host
	}
	return &boundRecordingExportCloud{key: key, host: host, valid: valid}, nil
}

func (s *server) handleRecordingExportStart(w http.ResponseWriter, r *http.Request, auth authResult, method string) {
	var body struct {
		RecordingID string `json:"recordingId"`
		RequestID   string `json:"requestId"`
		SourceType  string `json:"sourceType"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	recordingID := strings.TrimSpace(body.RecordingID)
	requestID := strings.TrimSpace(body.RequestID)
	if recordingID == "" || requestID == "" {
		gatewayErr(w, "INVALID_PARAMS", "recordingId and requestId are required")
		return
	}
	if len(requestID) > 256 || strings.ContainsAny(requestID, "\r\n\x00") {
		gatewayErr(w, "INVALID_PARAMS", "requestId is invalid")
		return
	}
	if !fsutil.IsSafeName(recordingID) {
		gatewayErr(w, "INVALID_PARAMS", "recordingId 含非法字符")
		return
	}
	store, ok := s.recordingStore(w, body.SourceType, method)
	if !ok {
		return
	}
	// 回显实际使用的来源，省略 sourceType 时明确返回默认的 mobile_app。
	sourceType := recording.SourceMobileApp
	if store == s.captureRecordingStorage {
		sourceType = recording.SourceCaptureApp
	}
	entry, ok := store.FindByID(recordingID)
	if !ok || !clientlabel.Visible(entry.ClientLabel, auth.scope()) {
		gatewayErr(w, "NOT_FOUND", "Recording not found: "+recordingID)
		return
	}
	scopeKey := s.recordingExportScope(auth)
	// 幂等重放先于来源条件检查：首次受理回执丢失后，来源进入转写或变得
	// 不可读时，同一 requestId 仍必须拿回原 exportId。
	if existingEntry, record, found := store.FindExportByRequest(scopeKey, requestID); found {
		if existingEntry.ID != recordingID {
			gatewayErr(w, "REQUEST_CONFLICT", "requestId already belongs to another recording")
			return
		}
		sizeBytes := record.SizeBytes
		if sizeBytes <= 0 {
			// 旧记录在导出开始时尚未持久化大小，补一个当前预估。
			sizeBytes = store.ExportSourceSize(entry)
		}
		gatewayOK(w, recording.ExportStartResult{
			RecordingID: recordingID, RequestID: requestID, ExportID: record.ExportID, State: record.State,
			SourceType: sourceType, SizeBytes: sizeBytes,
		})
		return
	}
	if !store.Exportable(entry) {
		code := "SOURCE_UNREADABLE"
		if entry.Status == recording.StatusTranscribing || entry.AudioStatus == recording.AudioStatusPending || entry.AudioStatus == recording.AudioStatusDownloading {
			code = "SOURCE_BUSY"
		}
		gatewayErr(w, code, "Recording is not ready for export")
		return
	}
	cloud, err := s.recordingExportCloud(entry.ClientLabel)
	if err != nil {
		gatewayErr(w, "EXPORT_UPLOAD_FAILED", err.Error())
		return
	}
	result, err := s.exportServiceFor(store).Start(r.Context(), recording.ExportStartRequest{
		RecordingID: recordingID, RequestID: requestID, ScopeKey: scopeKey,
	}, cloud)
	if err != nil {
		writeRecordingExportError(w, err)
		return
	}
	result.SourceType = sourceType
	gatewayOK(w, result)
}

func (s *server) handleRecordingExportStatus(w http.ResponseWriter, r *http.Request, auth authResult, method string) {
	var body struct {
		RecordingID string `json:"recordingId"`
		ExportID    string `json:"exportId"`
		SourceType  string `json:"sourceType"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	recordingID := strings.TrimSpace(body.RecordingID)
	exportID := strings.TrimSpace(body.ExportID)
	if recordingID == "" || exportID == "" {
		gatewayErr(w, "INVALID_PARAMS", "recordingId and exportId are required")
		return
	}
	if len(exportID) > 256 || strings.ContainsAny(exportID, "\r\n\x00") {
		gatewayErr(w, "INVALID_PARAMS", "exportId is invalid")
		return
	}
	if !fsutil.IsSafeName(recordingID) || !fsutil.IsSafeName(exportID) {
		gatewayErr(w, "INVALID_PARAMS", "recordingId or exportId 含非法字符")
		return
	}
	store, ok := s.recordingStore(w, body.SourceType, method)
	if !ok {
		return
	}
	// 回显实际使用的来源，省略 sourceType 时明确返回默认的 mobile_app。
	sourceType := recording.SourceMobileApp
	if store == s.captureRecordingStorage {
		sourceType = recording.SourceCaptureApp
	}
	entry, ok := store.FindByID(recordingID)
	if !ok || !clientlabel.Visible(entry.ClientLabel, auth.scope()) {
		gatewayErr(w, "NOT_FOUND", "Recording not found: "+recordingID)
		return
	}
	cloud, err := s.recordingExportCloud(entry.ClientLabel)
	if err != nil {
		gatewayErr(w, "EXPORT_UPLOAD_FAILED", err.Error())
		return
	}
	status, err := s.exportServiceFor(store).Status(r.Context(), recording.ExportStatusRequest{
		RecordingID: recordingID, ExportID: exportID, ScopeKey: s.recordingExportScope(auth),
	}, cloud)
	if err != nil {
		writeRecordingExportError(w, err)
		return
	}
	status.SourceType = sourceType
	gatewayOK(w, status)
}

func writeRecordingExportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, recording.ErrExportRequestConflict):
		gatewayErr(w, "REQUEST_CONFLICT", "requestId already belongs to another recording")
	case errors.Is(err, recording.ErrExportBusy):
		gatewayErr(w, "EXPORT_BUSY", "another export is in progress for this recording")
	case errors.Is(err, recording.ErrExportNotFound):
		gatewayErr(w, "EXPORT_NOT_FOUND", "export not found")
	case errors.Is(err, recording.ErrExportNotExportable):
		gatewayErr(w, "SOURCE_UNREADABLE", "recording is not exportable")
	case errors.Is(err, recording.ErrExportLinkUnavailable):
		gatewayErr(w, "EXPORT_UPLOAD_FAILED", "download link temporarily unavailable")
	case errors.Is(err, recording.ErrStorageUnavailable):
		gatewayErr(w, "RECORDING_LIST_UNAVAILABLE", "recording storage unavailable")
	case strings.HasPrefix(err.Error(), "Recording not found:"):
		gatewayErr(w, "NOT_FOUND", err.Error())
	default:
		gatewayErr(w, "INTERNAL_ERROR", err.Error())
	}
}
