package recording

import (
	"context"
	"fmt"
)

// SyncOptions 控制录音后台流程（转写、result.write 音频下载）。
type SyncOptions struct {
	OnContentCommit func(Entry, bool)
	URLRefresher    func(label, taskID string) func(context.Context) (string, error)
	NotifyStatus    func(StatusEvent)
	DownloadOptions DownloadOptions
	// ClientLabel 是本次调用的来源客户端 label（daemon 侧鉴权解析，非请求体字段）；
	// 留空按 "default" 入库。
	ClientLabel string
}

// TriggerTranscription 触发 ASR 转写。
func TriggerTranscription(recordingID string, storage *Storage, asr AsrConfig, logger Logger, opts SyncOptions) error {
	finish := observeContent(storage, recordingID, opts.OnContentCommit)
	success := false
	defer func() { finish(success) }()
	entry, ok := storage.FindByID(recordingID)
	if !ok {
		logger.Warn("[asr-trigger] 录音不存在: " + recordingID)
		return nil
	}
	if !CanStartTranscription(entry.Status) {
		logger.Warn("[asr-trigger] 当前状态不允许转写: " + recordingID + " (status=" + entry.Status + ")")
		return nil
	}
	if _, err := storage.UpdateStatus(recordingID, StatusTranscribing); err != nil {
		return err
	}
	_ = storage.SetLastError(recordingID, "")
	emitRecordingStatus(recordingID, storage, logger, opts.NotifyStatus, "", nil)

	entry, _ = storage.FindByID(recordingID)
	result := RunTranscriptionWorkflow(storage, entry, asr, logger)
	if !result.OK {
		message := "转写失败: " + result.Error
		logger.Error("[asr-trigger] " + message + ": " + recordingID)
		_, _ = storage.UpdateStatus(recordingID, StatusTranscribeFailed)
		_ = storage.SetLastError(recordingID, message)
		emitRecordingStatus(recordingID, storage, logger, opts.NotifyStatus, message, nil)
		return nil
	}
	if err := storage.SetTranscriptDataFile(recordingID, result.TranscriptDataFilename); err != nil {
		return err
	}
	if err := storage.SetTranscriptFile(recordingID, result.TranscriptFilename); err != nil {
		return err
	}
	if result.SummaryFilename != "" {
		if err := storage.SetSummaryFile(recordingID, result.SummaryFilename); err != nil {
			return err
		}
	}
	if err := storage.SetTitle(recordingID, result.Title); err != nil {
		return err
	}
	if _, err := storage.UpdateStatus(recordingID, StatusTranscribed); err != nil {
		return err
	}
	_ = storage.SetLastError(recordingID, "")
	success = true
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
		OssTaskID:          entry.OssTaskID,
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
