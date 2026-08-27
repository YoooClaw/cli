package recording

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var orderedArtifactName = regexp.MustCompile(`^[^/\\]+\.r[1-9][0-9]*\.[0-9a-f]{32}(?:\.(?:json|transcript\.md|summary\.md)|\.[A-Za-z0-9]+)(?:\.staged)?$`)

func queueOrderedAudioDownload(request validatedOrderedWrite, storage *Storage, logger Logger, opts SyncOptions) {
	ctx, cancel := context.WithCancel(context.Background())
	if !storage.registerOrderedAudioTask(request.recordingID, request.revision, request.ossURL, cancel) {
		cancel()
		return
	}
	go func() {
		defer storage.orderedTasksWG.Done()
		defer storage.finishOrderedAudioTask(request.recordingID, request.revision, request.ossURL)
		downloadOrderedAudio(ctx, request.recordingID, request.revision, request.ossURL, storage, logger, opts)
	}()
}

func (s *Storage) registerOrderedAudioTask(recordingID string, revision int64, ossURL string, cancel context.CancelFunc) bool {
	s.orderedTasksMu.Lock()
	defer s.orderedTasksMu.Unlock()
	if s.orderedClosed {
		return false
	}
	if current := s.orderedAudioTasks[recordingID]; current != nil {
		if current.writeRevision == revision && current.ossURL == ossURL {
			return false
		}
		current.cancel()
	}
	s.orderedAudioTasks[recordingID] = &orderedAudioTask{writeRevision: revision, ossURL: ossURL, cancel: cancel}
	s.orderedTasksWG.Add(1)
	return true
}

func (s *Storage) finishOrderedAudioTask(recordingID string, revision int64, ossURL string) {
	s.orderedTasksMu.Lock()
	defer s.orderedTasksMu.Unlock()
	current := s.orderedAudioTasks[recordingID]
	if current != nil && current.writeRevision == revision && current.ossURL == ossURL {
		delete(s.orderedAudioTasks, recordingID)
	}
}

func (s *Storage) cancelSupersededOrderedTask(recordingID string, revision int64, ossURL string) {
	s.orderedTasksMu.Lock()
	defer s.orderedTasksMu.Unlock()
	current := s.orderedAudioTasks[recordingID]
	if current != nil && (current.writeRevision != revision || current.ossURL != ossURL) {
		current.cancel()
		delete(s.orderedAudioTasks, recordingID)
	}
}

func downloadOrderedAudio(ctx context.Context, recordingID string, revision int64, ossURL string, storage *Storage, logger Logger, opts SyncOptions) {
	applied, err := storage.setOrderedAudioDownloading(recordingID, revision, ossURL)
	if err != nil {
		logger.Error("[recording-result] 音频下载状态写入失败: " + err.Error() + ": " + recordingID)
		return
	}
	if !applied {
		return
	}
	attempt, err := randomAttemptID()
	if err != nil {
		logger.Error("[recording-result] 音频 attempt 创建失败: " + err.Error())
		return
	}
	filename := fmt.Sprintf("%s.r%d.%s%s", recordingID, revision, attempt, extractAudioExt(ossURL))
	staged := filepath.Join(storage.audioDir, "."+filename+".incoming")
	defer os.Remove(staged)
	logger.Info(fmt.Sprintf("[recording-result] 开始下载音频: %s, writeRevision=%d", recordingID, revision))
	result := DownloadFileContext(ctx, ossURL, staged, logger, opts.DownloadOptions)
	if result.Cancelled {
		logger.Info(fmt.Sprintf("[recording-result] 音频下载已取消: %s, writeRevision=%d", recordingID, revision))
		return
	}
	if !result.OK {
		message := "音频下载失败: " + result.Error
		applied, setErr := storage.setOrderedAudioFailed(recordingID, revision, ossURL, message)
		if setErr != nil {
			logger.Error("[recording-result] 音频失败状态写入失败: " + setErr.Error())
			return
		}
		if applied {
			emitResultDownloadStatusAtRevision(recordingID, &revision, storage, logger, opts.NotifyStatus)
		}
		return
	}
	applied, err = storage.commitOrderedAudio(recordingID, revision, ossURL, staged, filename)
	if err != nil {
		logger.Error("[recording-result] 音频提交失败: " + err.Error())
		return
	}
	if applied {
		logger.Info(fmt.Sprintf("[recording-result] 音频已更新: %s, writeRevision=%d", recordingID, revision))
		emitResultDownloadStatusAtRevision(recordingID, &revision, storage, logger, opts.NotifyStatus)
	}
}

func (s *Storage) mutateOrderedAudio(recordingID string, revision int64, ossURL string, mutate func(*Entry) bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return false, nil
	}
	current := s.index.Recordings[idx]
	if current.WriteRevision == nil || *current.WriteRevision != revision || strings.TrimSpace(current.Metadata.OssAudioURL) != strings.TrimSpace(ossURL) {
		return false, nil
	}
	nextIndex, err := cloneRecordingIndex(s.index)
	if err != nil {
		return false, err
	}
	next := &nextIndex.Recordings[idx]
	if !mutate(next) {
		return false, nil
	}
	if err := fsWriteIndex(s.indexPath, nextIndex); err != nil {
		return false, err
	}
	s.index = nextIndex
	return true, nil
}

func (s *Storage) setOrderedAudioDownloading(recordingID string, revision int64, ossURL string) (bool, error) {
	return s.mutateOrderedAudio(recordingID, revision, ossURL, func(entry *Entry) bool {
		if entry.AudioStatus == AudioStatusDownloaded && s.hasReusableAudioLocked(*entry) {
			return false
		}
		entry.AudioStatus = AudioStatusDownloading
		if entry.Status == "sync_failed" {
			entry.Status = "syncing_openclaw"
			entry.Metadata.Status = entry.Status
		}
		entry.UpdatedAt = nowISO()
		return true
	})
}

func (s *Storage) setOrderedAudioFailed(recordingID string, revision int64, ossURL, message string) (bool, error) {
	return s.mutateOrderedAudio(recordingID, revision, ossURL, func(entry *Entry) bool {
		entry.AudioStatus = AudioStatusFailed
		if entry.Status == "syncing_openclaw" || entry.Status == "sync_failed" {
			entry.Status = "sync_failed"
			entry.Metadata.Status = entry.Status
		}
		entry.LastError = strings.TrimSpace(message)
		entry.UpdatedAt = nowISO()
		return true
	})
}

func (s *Storage) commitOrderedAudio(recordingID string, revision int64, ossURL, staged, filename string) (bool, error) {
	if filepath.Base(filename) != filename {
		return false, os.ErrInvalid
	}
	info, err := os.Stat(staged)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		if err != nil {
			return false, err
		}
		return false, fmt.Errorf("downloaded audio staging path is not a non-empty file")
	}
	s.mu.Lock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		s.mu.Unlock()
		return false, nil
	}
	current := s.index.Recordings[idx]
	if current.WriteRevision == nil || *current.WriteRevision != revision || strings.TrimSpace(current.Metadata.OssAudioURL) != strings.TrimSpace(ossURL) {
		s.mu.Unlock()
		return false, nil
	}
	nextIndex, err := cloneRecordingIndex(s.index)
	if err != nil {
		s.mu.Unlock()
		return false, err
	}
	next := &nextIndex.Recordings[idx]
	destination := filepath.Join(s.audioDir, filename)
	if err := os.Rename(staged, destination); err != nil {
		s.mu.Unlock()
		return false, err
	}
	_ = os.Chmod(destination, 0o600)
	previous := next.AudioFile
	next.AudioFile = filepath.ToSlash(filepath.Join(audioDirName, filename))
	next.AudioSourceURL = strings.TrimSpace(ossURL)
	next.AudioStatus = AudioStatusDownloaded
	next.Metadata.FileSizeDisplay = FormatShortFileSize(info.Size())
	if next.Status == "syncing_openclaw" || next.Status == "sync_failed" {
		next.Status = StatusSynced
		next.Metadata.Status = next.Status
	}
	next.LastError = ""
	next.UpdatedAt = nowISO()
	if err := fsWriteIndex(s.indexPath, nextIndex); err != nil {
		_ = os.Remove(destination)
		s.mu.Unlock()
		return false, err
	}
	s.index = nextIndex
	s.mu.Unlock()
	if previous != "" && previous != next.AudioFile {
		s.removeUnreferencedOrderedPaths([]string{previous})
	}
	return true, nil
}

func (s *Storage) cleanupUnreferencedOrderedArtifacts() {
	s.mu.Lock()
	referenced := map[string]bool{}
	for _, entry := range s.index.Recordings {
		for _, relative := range []string{entry.AudioFile, entry.TranscriptDataFile, entry.TranscriptFile, entry.SummaryFile} {
			if relative != "" {
				referenced[filepath.Clean(relative)] = true
			}
		}
	}
	s.mu.Unlock()
	for _, directory := range []string{s.audioDir, s.transcriptDataDir, s.transcriptsDir, s.summariesDir} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, item := range entries {
			if item.IsDir() {
				continue
			}
			name := item.Name()
			candidate := strings.TrimPrefix(name, ".")
			isIncoming := strings.Contains(name, ".incoming")
			if !isIncoming && !orderedArtifactName.MatchString(candidate) {
				continue
			}
			relative, _ := filepath.Rel(s.dir, filepath.Join(directory, name))
			if !referenced[filepath.Clean(relative)] {
				_ = os.Remove(filepath.Join(directory, name))
			}
		}
	}
}
