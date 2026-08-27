package recording

import "fmt"

// SyncOptions 控制录音后台流程（转写、result.write 音频下载）。
type SyncOptions struct {
	NotifyStatus    func(StatusEvent)
	DownloadOptions DownloadOptions
	// ClientLabel 是本次调用的来源客户端 label（daemon 侧鉴权解析，非请求体字段）；
	// 留空按 "default" 入库。
	ClientLabel string
}

// TriggerTranscription 触发 ASR 转写。
func TriggerTranscription(recordingID string, storage *Storage, asr AsrConfig, logger Logger, opts SyncOptions) error {
	entry, ok := storage.FindByID(recordingID)
	if !ok {
		logger.Warn("[asr-trigger] 录音不存在: " + recordingID)
		return nil
	}
	if !CanStartTranscription(entry.Status) {
		logger.Warn("[asr-trigger] 当前状态不允许转写: " + recordingID + " (status=" + entry.Status + ")")
		return nil
	}
	baseRevision := cloneInt64(entry.WriteRevision)
	started, err := storage.beginTranscriptionAtRevision(recordingID, baseRevision)
	if err != nil {
		return err
	}
	if !started {
		logger.Info("[asr-trigger] revision 已变化或状态不允许，跳过: " + recordingID)
		return nil
	}
	emitRecordingStatus(recordingID, storage, logger, opts.NotifyStatus, "", nil)

	entry, _ = storage.FindByID(recordingID)
	result := RunTranscriptionWorkflow(storage, entry, asr, logger)
	if !result.OK {
		message := "转写失败: " + result.Error
		logger.Error("[asr-trigger] " + message + ": " + recordingID)
		applied, applyErr := storage.failTranscriptionAtRevision(recordingID, baseRevision, message)
		if applyErr != nil {
			return applyErr
		}
		if applied {
			emitRecordingStatus(recordingID, storage, logger, opts.NotifyStatus, message, nil)
		}
		return nil
	}
	applied, applyErr := storage.commitTranscriptionAtRevision(recordingID, baseRevision, result)
	if applyErr != nil {
		storage.cleanupSupersededTranscription(result)
		return applyErr
	}
	if !applied {
		storage.cleanupSupersededTranscription(result)
		logger.Info("[asr-trigger] 转写结果已被更高 writeRevision 取代: " + recordingID)
		return nil
	}
	logger.Info("[asr-trigger] 转写完成: " + recordingID + ", summary=\"" + result.Title + "\"")
	emitRecordingStatus(recordingID, storage, logger, opts.NotifyStatus, "", &StatusEvent{
		Transcript: result.Transcript,
		Summary:    result.Summary,
		Title:      result.Title,
	})
	return nil
}

func emitRecordingStatus(recordingID string, storage *Storage, logger Logger, notify func(StatusEvent), errMessage string, extras *StatusEvent) {
	if notify == nil {
		return
	}
	entry, ok := storage.FindByID(recordingID)
	if !ok {
		return
	}
	title := entry.Title
	if extras != nil && extras.Title != "" {
		title = extras.Title
	}
	if title == "" {
		title = entry.Metadata.Name
	}
	if title == "" {
		title = entry.ID
	}
	persistedErr := entry.LastError
	if errMessage == "" {
		errMessage = persistedErr
	}
	event := StatusEvent{
		RecordingID:        entry.ID,
		WriteRevision:      cloneInt64(entry.WriteRevision),
		TransferStatus:     entry.Status,
		AudioStatus:        entry.AudioStatus,
		AudioFile:          entry.AudioFile,
		SrtFile:            entry.SrtFile,
		TranscriptDataFile: entry.TranscriptDataFile,
		TranscriptFile:     entry.TranscriptFile,
		SummaryFile:        entry.SummaryFile,
		Title:              title,
		UpdatedAt:          entry.UpdatedAt,
		Error:              errMessage,
	}
	if extras != nil {
		event.Transcript = extras.Transcript
		event.Summary = extras.Summary
	}
	defer func() {
		if rec := recover(); rec != nil {
			logger.Error(fmt.Sprintf("[recording-status] 状态事件发送失败: %s, %v", recordingID, rec))
		}
	}()
	notify(event)
}
