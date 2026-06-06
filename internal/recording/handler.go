package recording

import "fmt"

// SyncResult 是 recordings.sync 的即时返回。
type SyncResult struct {
	OK             bool   `json:"ok"`
	RecordingID    string `json:"recordingId"`
	TransferStatus string `json:"transfer_status"`
	Error          string `json:"error,omitempty"`
}

// SyncOptions 控制 sync 后台流程。
type SyncOptions struct {
	NotifyStatus    func(StatusEvent)
	DownloadOptions DownloadOptions
}

// HandleRecordingSync 处理 recordings.sync：元数据入库后后台下载 OSS，并按配置触发 ASR。
func HandleRecordingSync(recordingID string, metadata Metadata, storage *Storage, asr *AsrConfig, logger Logger, opts SyncOptions) SyncResult {
	existing, found := storage.FindByID(recordingID)
	shouldDownloadAndSync := !found ||
		existing.Metadata.OssAudioURL != metadata.OssAudioURL ||
		existing.AudioFile == "" ||
		existing.Status == StatusSyncingOpenClaw ||
		existing.Status == StatusSyncFailed

	if _, err := storage.Ingest(recordingID, metadata, ""); err != nil {
		return SyncResult{OK: false, RecordingID: recordingID, TransferStatus: StatusSyncFailed, Error: err.Error()}
	}
	if shouldDownloadAndSync {
		go func() {
			if err := runRecordingSyncInBackground(metadata, recordingID, storage, asr, logger, opts); err != nil {
				message := "录音同步失败: " + err.Error()
				logger.Error("[recording-sync] " + message + ": " + recordingID)
				if current, ok := storage.FindByID(recordingID); ok && current.Status == StatusSyncingOpenClaw {
					_, _ = storage.UpdateStatus(recordingID, StatusSyncFailed)
				}
				_ = storage.SetLastError(recordingID, message)
				emitRecordingStatus(recordingID, storage, logger, opts.NotifyStatus, message, nil)
			}
		}()
	} else {
		logger.Info("[recording-sync] 录音已存在且音频未变化，跳过重复下载: " + recordingID)
		emitRecordingStatus(recordingID, storage, logger, opts.NotifyStatus, "", nil)
		if current, ok := storage.FindByID(recordingID); ok && current.Status == StatusSynced && IsAsrConfigured(asr) {
			go func() {
				if err := TriggerTranscription(recordingID, storage, *asr, logger, opts); err != nil {
					logger.Error("[asr-trigger] 转写触发失败: " + recordingID + ", " + err.Error())
				}
			}()
		}
	}

	current, ok := storage.FindByID(recordingID)
	status := StatusSyncingOpenClaw
	lastErr := ""
	if ok {
		status = current.Status
		lastErr = current.LastError
	}
	return SyncResult{OK: true, RecordingID: recordingID, TransferStatus: status, Error: lastErr}
}

// HandleRecordingSyncWithClientLabel 与 HandleRecordingSync 相同，但显式写入 clientLabel。
func HandleRecordingSyncWithClientLabel(recordingID string, metadata Metadata, clientLabel string, storage *Storage, asr *AsrConfig, logger Logger, opts SyncOptions) SyncResult {
	existing, found := storage.FindByID(recordingID)
	shouldDownloadAndSync := !found ||
		existing.Metadata.OssAudioURL != metadata.OssAudioURL ||
		existing.AudioFile == "" ||
		existing.Status == StatusSyncingOpenClaw ||
		existing.Status == StatusSyncFailed
	if _, err := storage.Ingest(recordingID, metadata, clientLabel); err != nil {
		return SyncResult{OK: false, RecordingID: recordingID, TransferStatus: StatusSyncFailed, Error: err.Error()}
	}
	if shouldDownloadAndSync {
		go func() {
			if err := runRecordingSyncInBackground(metadata, recordingID, storage, asr, logger, opts); err != nil {
				message := "录音同步失败: " + err.Error()
				logger.Error("[recording-sync] " + message + ": " + recordingID)
				if current, ok := storage.FindByID(recordingID); ok && current.Status == StatusSyncingOpenClaw {
					_, _ = storage.UpdateStatus(recordingID, StatusSyncFailed)
				}
				_ = storage.SetLastError(recordingID, message)
				emitRecordingStatus(recordingID, storage, logger, opts.NotifyStatus, message, nil)
			}
		}()
	} else {
		logger.Info("[recording-sync] 录音已存在且音频未变化，跳过重复下载: " + recordingID)
		emitRecordingStatus(recordingID, storage, logger, opts.NotifyStatus, "", nil)
		if current, ok := storage.FindByID(recordingID); ok && current.Status == StatusSynced && IsAsrConfigured(asr) {
			go func() {
				if err := TriggerTranscription(recordingID, storage, *asr, logger, opts); err != nil {
					logger.Error("[asr-trigger] 转写触发失败: " + recordingID + ", " + err.Error())
				}
			}()
		}
	}
	current, ok := storage.FindByID(recordingID)
	status := StatusSyncingOpenClaw
	lastErr := ""
	if ok {
		status = current.Status
		lastErr = current.LastError
	}
	return SyncResult{OK: true, RecordingID: recordingID, TransferStatus: status, Error: lastErr}
}

func runRecordingSyncInBackground(metadata Metadata, recordingID string, storage *Storage, asr *AsrConfig, logger Logger, opts SyncOptions) error {
	audioDestPath := storage.AudioFilePath(recordingID, metadata.OssAudioURL)
	srtDestPath := storage.SrtFilePath(recordingID)
	logger.Info(fmt.Sprintf("[recording-sync] 开始下载录音文件: %s, audio=%s", recordingID, metadata.OssAudioURL))
	download := DownloadRecordingFiles(metadata.OssAudioURL, metadata.OssSrtURL, audioDestPath, srtDestPath, logger, opts.DownloadOptions)
	if !download.Audio.OK {
		message := "音频下载失败: " + download.Audio.Error
		logger.Error("[recording-sync] " + message + ": " + recordingID)
		_, _ = storage.UpdateStatus(recordingID, StatusSyncFailed)
		_ = storage.SetLastError(recordingID, message)
		emitRecordingStatus(recordingID, storage, logger, opts.NotifyStatus, message, nil)
		return nil
	}
	_ = storage.SetLastError(recordingID, "")
	_ = storage.SetAudioFile(recordingID, AudioFilename(recordingID, metadata.OssAudioURL))
	if download.SRT != nil && download.SRT.OK {
		_ = storage.SetSrtFile(recordingID, SrtFilename(recordingID))
	}
	_, _ = storage.UpdateStatus(recordingID, StatusSynced)
	logger.Info("[recording-sync] 录音已同步: " + recordingID)
	emitRecordingStatus(recordingID, storage, logger, opts.NotifyStatus, "", nil)

	if IsAsrConfigured(asr) && CanStartTranscription(StatusSynced) {
		return TriggerTranscription(recordingID, storage, *asr, logger, opts)
	}
	return nil
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
	_ = storage.SetTranscriptDataFile(recordingID, result.TranscriptDataFilename)
	_ = storage.SetTranscriptFile(recordingID, result.TranscriptFilename)
	if result.SummaryFilename != "" {
		_ = storage.SetSummaryFile(recordingID, result.SummaryFilename)
	}
	_ = storage.SetTitle(recordingID, result.Title)
	_, _ = storage.UpdateStatus(recordingID, StatusTranscribed)
	_ = storage.SetLastError(recordingID, "")
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
		TransferStatus:     entry.Status,
		AudioFile:          entry.AudioFile,
		SrtFile:            entry.SrtFile,
		TranscriptDataFile: entry.TranscriptDataFile,
		TranscriptFile:     entry.TranscriptFile,
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
