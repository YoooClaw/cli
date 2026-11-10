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

func (s *server) exportService() *recording.ExportService {
	s.exportOnce.Do(func() {
		if s.recordingStorage == nil {
			return
		}
		s.recordingExports = recording.NewExportService(s.recordingStorage, s.logger)
		s.recordingExports.RecoverInterrupted()
	})
	return s.recordingExports
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

func (s *server) handleRecordingExportStart(w http.ResponseWriter, r *http.Request, auth authResult) {
	var body struct {
		RecordingID string `json:"recordingId"`
		RequestID   string `json:"requestId"`
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
	entry, ok := s.recordingStorage.FindByID(recordingID)
	if !ok || !clientlabel.Visible(entry.ClientLabel, auth.scope()) {
		gatewayErr(w, "NOT_FOUND", "Recording not found: "+recordingID)
		return
	}
	scopeKey := s.recordingExportScope(auth)
	// 幂等重放先于来源条件检查：首次受理回执丢失后，来源进入转写或变得
	// 不可读时，同一 requestId 仍必须拿回原 exportId。
	if existingEntry, record, found := s.recordingStorage.FindExportByRequest(scopeKey, requestID); found {
		if existingEntry.ID != recordingID {
			gatewayErr(w, "REQUEST_CONFLICT", "requestId already belongs to another recording")
			return
		}
		gatewayOK(w, recording.ExportStartResult{
			RecordingID: recordingID, RequestID: requestID, ExportID: record.ExportID, State: record.State,
		})
		return
	}
	if !s.recordingStorage.Exportable(entry) {
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
	result, err := s.exportService().Start(r.Context(), recording.ExportStartRequest{
		RecordingID: recordingID, RequestID: requestID, ScopeKey: scopeKey,
	}, cloud)
	if err != nil {
		writeRecordingExportError(w, err)
		return
	}
	gatewayOK(w, result)
}

func (s *server) handleRecordingExportStatus(w http.ResponseWriter, r *http.Request, auth authResult) {
	var body struct {
		RecordingID string `json:"recordingId"`
		ExportID    string `json:"exportId"`
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
	entry, ok := s.recordingStorage.FindByID(recordingID)
	if !ok || !clientlabel.Visible(entry.ClientLabel, auth.scope()) {
		gatewayErr(w, "NOT_FOUND", "Recording not found: "+recordingID)
		return
	}
	cloud, err := s.recordingExportCloud(entry.ClientLabel)
	if err != nil {
		gatewayErr(w, "EXPORT_UPLOAD_FAILED", err.Error())
		return
	}
	status, err := s.exportService().Status(r.Context(), recording.ExportStatusRequest{
		RecordingID: recordingID, ExportID: exportID, ScopeKey: s.recordingExportScope(auth),
	}, cloud)
	if err != nil {
		writeRecordingExportError(w, err)
		return
	}
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
