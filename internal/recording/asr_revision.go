package recording

import "strings"

func sameWriteRevision(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Storage) beginTranscriptionAtRevision(recordingID string, baseRevision *int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 || !sameWriteRevision(s.index.Recordings[idx].WriteRevision, baseRevision) {
		return false, nil
	}
	if !CanStartTranscription(s.index.Recordings[idx].Status) {
		return false, nil
	}
	nextIndex, err := cloneRecordingIndex(s.index)
	if err != nil {
		return false, err
	}
	next := &nextIndex.Recordings[idx]
	next.Status = StatusTranscribing
	next.Metadata.Status = next.Status
	next.LastError = ""
	next.UpdatedAt = nowISO()
	if err := fsWriteIndex(s.indexPath, nextIndex); err != nil {
		return false, err
	}
	s.index = nextIndex
	return true, nil
}

func (s *Storage) failTranscriptionAtRevision(recordingID string, baseRevision *int64, message string) (bool, error) {
	return s.mutateTranscriptionAtRevision(recordingID, baseRevision, func(entry *Entry) {
		entry.Status = StatusTranscribeFailed
		entry.Metadata.Status = entry.Status
		entry.LastError = strings.TrimSpace(message)
	})
}

func (s *Storage) commitTranscriptionAtRevision(recordingID string, baseRevision *int64, result WorkflowResult) (bool, error) {
	return s.mutateTranscriptionAtRevision(recordingID, baseRevision, func(entry *Entry) {
		entry.TranscriptDataFile = transcriptDataDirName + "/" + result.TranscriptDataFilename
		entry.TranscriptFile = transcriptsDirName + "/" + result.TranscriptFilename
		if result.SummaryFilename != "" {
			entry.SummaryFile = summariesDirName + "/" + result.SummaryFilename
		}
		entry.Title = strings.TrimSpace(result.Title)
		entry.Status = StatusTranscribed
		entry.Metadata.Status = entry.Status
		entry.LastError = ""
	})
}

func (s *Storage) mutateTranscriptionAtRevision(recordingID string, baseRevision *int64, mutate func(*Entry)) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 || !sameWriteRevision(s.index.Recordings[idx].WriteRevision, baseRevision) {
		return false, nil
	}
	nextIndex, err := cloneRecordingIndex(s.index)
	if err != nil {
		return false, err
	}
	next := &nextIndex.Recordings[idx]
	mutate(next)
	next.UpdatedAt = nowISO()
	if err := fsWriteIndex(s.indexPath, nextIndex); err != nil {
		return false, err
	}
	s.index = nextIndex
	return true, nil
}

func (s *Storage) cleanupSupersededTranscription(result WorkflowResult) {
	paths := []string{
		transcriptDataDirName + "/" + result.TranscriptDataFilename,
		transcriptsDirName + "/" + result.TranscriptFilename,
	}
	if result.SummaryFilename != "" {
		paths = append(paths, summariesDirName+"/"+result.SummaryFilename)
	}
	s.removeUnreferencedOrderedPaths(paths)
}
