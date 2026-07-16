package daemon

import (
	"net/http"
	"strings"

	imgstore "github.com/YoooClaw/cli/internal/image"
	"github.com/YoooClaw/cli/internal/recording"
)

var successfulRecordingStatuses = map[string]bool{
	recording.StatusSynced:      true,
	recording.StatusTranscribed: true,
}

type recordingIDBody struct {
	RecordingID string               `json:"recordingId"`
	ASR         *recording.AsrConfig `json:"asr,omitempty"`
}

func (s *server) handleRecordingGateway(w http.ResponseWriter, r *http.Request, path string) {
	method := strings.TrimPrefix(path, "/gateway/")
	switch method {
	case "recordings.result.write":
		var body recording.ResultWriteParams
		if !decodeBody(w, r, &body) {
			return
		}
		recordingID := strings.TrimSpace(body.RecordingID)
		if recordingID == "" {
			gatewayErr(w, "INVALID_PARAMS", "recordingId is required")
			return
		}
		if body.Transcript == nil && body.Summary == nil {
			gatewayErr(w, "INVALID_PARAMS", "transcript or summary is required")
			return
		}
		body.RecordingID = recordingID
		result, err := recording.HandleRecordingResultWrite(body, s.recordingStorage, s.logger, recording.SyncOptions{
			NotifyStatus: s.notifyRecordingStatus,
		})
		if err != nil {
			code := "RESULT_WRITE_FAILED"
			switch {
			case strings.HasPrefix(err.Error(), "Recording not found:"):
				code = "NOT_FOUND"
			case err.Error() == "recordingId is required" || err.Error() == "transcript or summary is required":
				code = "INVALID_PARAMS"
			}
			gatewayErr(w, code, err.Error())
			return
		}
		gatewayOK(w, result)

	case "recordings.retranscribe":
		var body recordingIDBody
		if !decodeBody(w, r, &body) {
			return
		}
		recordingID := strings.TrimSpace(body.RecordingID)
		if recordingID == "" {
			gatewayErr(w, "INVALID_PARAMS", "recordingId is required")
			return
		}
		if body.ASR != nil {
			if errMsg := recording.ValidateAsrConfig(body.ASR); errMsg != "" {
				gatewayErr(w, "ASR_NOT_CONFIGURED", errMsg)
				return
			}
		}
		asr := s.resolveConfiguredASR(body.ASR)
		if errMsg := recording.ValidateAsrConfig(asr); errMsg != "" {
			gatewayErr(w, "ASR_NOT_CONFIGURED", errMsg)
			return
		}
		entry, ok := s.recordingStorage.FindByID(recordingID)
		if !ok {
			gatewayErr(w, "NOT_FOUND", "Recording not found: "+recordingID)
			return
		}
		if !recording.CanStartTranscription(entry.Status) {
			gatewayErr(w, "INVALID_STATE", "Recording status does not allow retranscribe: "+entry.Status)
			return
		}
		if body.ASR == nil {
			s.logger.Info("[recording-retranscribe] 使用本地 asr-config.json（mode=" + asr.Mode + maskedKeyLog(asr) + "）")
		}
		go func() {
			if err := recording.TriggerTranscription(recordingID, s.recordingStorage, *asr, s.logger, recording.SyncOptions{
				NotifyStatus: s.notifyRecordingStatus,
			}); err != nil {
				s.logger.Error("[recording-retranscribe] 重试转写失败: " + recordingID + ", " + err.Error())
			}
		}()
		gatewayOK(w, map[string]any{"ok": true, "recordingId": recordingID, "message": "转写已重新触发"})

	case "recordings.asr.init":
		var body struct {
			ASR *recording.AsrConfig `json:"asr"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		asr := s.resolveConfiguredASR(body.ASR)
		result := recording.InitializeAsr(asr)
		if !result.OK {
			gatewayErr(w, "ASR_INIT_FAILED", result.Error)
			return
		}
		gatewayOK(w, result)

	case "recordings.list":
		var body struct {
			Status string `json:"status"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		status := strings.TrimSpace(body.Status)
		if status != "" && !recording.IsRecordingStatus(status) {
			gatewayErr(w, "INVALID_PARAMS", "invalid recording status: "+status)
			return
		}
		entries := s.recordingStorage.ListAll()
		if status != "" {
			entries = s.recordingStorage.ListByStatus(status)
		}
		items := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			items = append(items, recordingListItem(entry))
		}
		gatewayOK(w, map[string]any{"total": len(items), "recordings": items})

	case "recordings.status":
		var body recordingIDBody
		if !decodeBody(w, r, &body) {
			return
		}
		recordingID := strings.TrimSpace(body.RecordingID)
		if recordingID == "" {
			gatewayErr(w, "INVALID_PARAMS", "recordingId is required")
			return
		}
		entry, ok := s.recordingStorage.FindByID(recordingID)
		if !ok {
			gatewayErr(w, "NOT_FOUND", "Recording not found: "+recordingID)
			return
		}
		gatewayOK(w, recordingDetail(entry))

	case "recordings.rename":
		var body struct {
			RecordingID string `json:"recordingId"`
			Name        string `json:"name"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		recordingID := strings.TrimSpace(body.RecordingID)
		name := strings.TrimSpace(body.Name)
		if recordingID == "" || name == "" {
			gatewayErr(w, "INVALID_PARAMS", "recordingId and name are required")
			return
		}
		entry, ok, err := s.recordingStorage.Rename(recordingID, name)
		if err != nil {
			gatewayErr(w, "INTERNAL_ERROR", err.Error())
			return
		}
		if !ok {
			gatewayErr(w, "NOT_FOUND", "Recording not found: "+recordingID)
			return
		}
		gatewayOK(w, recordingDetail(entry))

	case "recordings.delete":
		var body struct {
			RecordingID  string `json:"recordingId"`
			DeleteRemote bool   `json:"deleteRemote"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		recordingID := strings.TrimSpace(body.RecordingID)
		if recordingID == "" {
			gatewayErr(w, "INVALID_PARAMS", "recordingId is required")
			return
		}
		deleted, err := s.recordingStorage.Delete(recordingID, !body.DeleteRemote)
		if err != nil {
			gatewayErr(w, "INTERNAL_ERROR", err.Error())
			return
		}
		if !deleted {
			gatewayErr(w, "NOT_FOUND", "Recording not found: "+recordingID)
			return
		}
		gatewayOK(w, map[string]any{"ok": true, "recordingId": recordingID, "deletedRemote": body.DeleteRemote})

	default:
		writeJSON(w, 404, errBody("YOOOCLAW_NOT_FOUND", "未知路径："+path))
	}
}

func (s *server) handleImageHTTP(w http.ResponseWriter, r *http.Request, auth authResult) {
	var body imgstore.SyncPayload
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := imgstore.Ingest(s.ctx.Paths.Images, body, auth.clientLabel, s.cfg.Image.MaxBytes, s.logger)
	if err != nil {
		writeJSON(w, 400, errBody("INVALID_PARAMS", err.Error()))
		return
	}
	writeJSON(w, 200, result)
}

func (s *server) handleImageGateway(w http.ResponseWriter, r *http.Request, auth authResult) {
	var body imgstore.SyncPayload
	if !decodeBody(w, r, &body) {
		return
	}
	result, err := imgstore.Ingest(s.ctx.Paths.Images, body, auth.clientLabel, s.cfg.Image.MaxBytes, s.logger)
	if err != nil {
		gatewayErr(w, "INVALID_PARAMS", err.Error())
		return
	}
	gatewayOK(w, result)
}

func (s *server) resolveConfiguredASR(caller *recording.AsrConfig) *recording.AsrConfig {
	fallback := ""
	if set := s.snapshotCreds(); set.DefaultEntry != nil {
		fallback = set.DefaultEntry.Key
	}
	return recording.ResolveAsrConfig(caller, recording.LoadLocalAsrConfig(s.ctx.Paths.Recordings), fallback)
}

func (s *server) notifyRecordingStatus(event recording.StatusEvent) {
	s.logger.Info("[recording-status] " + event.RecordingID + " -> " + event.TransferStatus + errorSuffix(event.Error))
	if s.recordingEventLog != nil {
		if err := s.recordingEventLog.Append(event); err != nil {
			s.logger.Warn("[recording-status] 事件落盘失败: " + err.Error())
		}
	}
	if egress := s.currentEgress(); egress != nil {
		if err := egress.PushEvent("recording.status", event); err != nil {
			s.logger.Warn("[recording-status] 出站事件投递失败: " + err.Error())
		}
	}
	if successfulRecordingStatuses[event.TransferStatus] {
		if entry, ok := s.recordingStorage.FindByID(event.RecordingID); ok && entry.LastError != "" {
			_ = s.recordingStorage.SetLastError(event.RecordingID, "")
			s.logger.Info("[recording-status] 终态 " + event.TransferStatus + " 清理 lastError 残留: " + event.RecordingID)
		}
	}
}

func recordingListItem(entry recording.Entry) map[string]any {
	return map[string]any{
		"recordingId":        entry.ID,
		"name":               entry.Metadata.Name,
		"duration_sec":       entry.Metadata.DurationSec,
		"file_size_bytes":    entry.Metadata.FileSizeBytes,
		"created_at":         entry.Metadata.CreatedAt,
		"transfer_status":    entry.Status,
		"marker_count":       len(entry.Metadata.Markers),
		"has_audio":          entry.AudioFile != "",
		"has_srt":            entry.SrtFile != "",
		"has_transcript":     entry.TranscriptFile != "",
		"audioFile":          nilIfEmptyStr(entry.AudioFile),
		"transcriptDataFile": nilIfEmptyStr(entry.TranscriptDataFile),
		"transcriptFile":     nilIfEmptyStr(entry.TranscriptFile),
		"updatedAt":          entry.UpdatedAt,
		"error":              nilIfEmptyStr(entry.LastError),
	}
}

func recordingDetail(entry recording.Entry) map[string]any {
	title := firstNonEmptyStr(entry.Title, entry.Metadata.Name, entry.ID)
	return map[string]any{
		"recordingId":        entry.ID,
		"name":               entry.Metadata.Name,
		"duration_sec":       entry.Metadata.DurationSec,
		"file_size_bytes":    entry.Metadata.FileSizeBytes,
		"created_at":         entry.Metadata.CreatedAt,
		"location":           entry.Metadata.Location,
		"oss_audio_url":      entry.Metadata.OssAudioURL,
		"oss_srt_url":        nilIfEmptyStr(entry.Metadata.OssSrtURL),
		"markers":            entry.Metadata.Markers,
		"transfer_status":    entry.Status,
		"audioFile":          nilIfEmptyStr(entry.AudioFile),
		"srtFile":            nilIfEmptyStr(entry.SrtFile),
		"transcriptDataFile": nilIfEmptyStr(entry.TranscriptDataFile),
		"transcriptFile":     nilIfEmptyStr(entry.TranscriptFile),
		"summaryFile":        nilIfEmptyStr(entry.SummaryFile),
		"title":              title,
		"ingestedAt":         entry.IngestedAt,
		"updatedAt":          entry.UpdatedAt,
		"error":              nilIfEmptyStr(entry.LastError),
	}
}

func maskedKeyLog(asr *recording.AsrConfig) string {
	if asr != nil && asr.API != nil && strings.TrimSpace(asr.API.APIKey) != "" {
		return ", key=ock-***"
	}
	return ""
}

func errorSuffix(err string) string {
	if err == "" {
		return ""
	}
	return " err=" + err
}
