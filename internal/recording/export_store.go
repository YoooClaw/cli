package recording

import "errors"

var (
	ErrExportRequestConflict = errors.New("export request conflict")
	ErrExportBusy            = errors.New("export busy")
	ErrExportNotFound        = errors.New("export not found")
)

// BeginExport atomically checks requestId idempotency, rejects a concurrent
// attempt on the same recording, persists a preparing record and appends it to
// the bounded export history. sizeBytes is the current source-size estimate so
// planning fields are available before the asynchronous export starts.
// Entry.UpdatedAt is not modified.
func (s *Storage) BeginExport(recordingID, requestID, scopeKey, exportID, now string, sizeBytes int64) (Entry, ExportRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureWritableLocked(); err != nil {
		return Entry{}, ExportRecord{}, false, err
	}
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return Entry{}, ExportRecord{}, false, errors.New("Recording not found: " + recordingID)
	}
	for i := range s.index.Recordings {
		entry := &s.index.Recordings[i]
		for _, existing := range entry.Exports {
			if existing.ScopeKey != scopeKey || existing.RequestID != requestID {
				continue
			}
			if entry.ID != recordingID {
				return Entry{}, ExportRecord{}, false, ErrExportRequestConflict
			}
			return *entry, existing, false, nil
		}
	}
	entry := &s.index.Recordings[idx]
	if len(entry.Exports) > 0 {
		switch entry.Exports[len(entry.Exports)-1].State {
		case ExportStatePreparing, ExportStateUploading:
			return Entry{}, ExportRecord{}, false, ErrExportBusy
		}
	}
	record := ExportRecord{
		ExportID: exportID, RequestID: requestID, ScopeKey: scopeKey,
		State: ExportStatePreparing, CreatedAt: now, UpdatedAt: now,
		SizeBytes: sizeBytes,
	}
	previous := append([]ExportRecord(nil), entry.Exports...)
	entry.Exports = append(entry.Exports, record)
	if len(entry.Exports) > maxExportHistory {
		entry.Exports = append([]ExportRecord(nil), entry.Exports[len(entry.Exports)-maxExportHistory:]...)
	}
	if err := s.saveIndexLocked(); err != nil {
		// 未持久化成功不能视为已受理，也不能留下只存在于内存的 preparing
		// 记录，否则同一 requestId 重试会一直重放一个永远不会执行的任务。
		entry.Exports = previous
		return Entry{}, ExportRecord{}, false, err
	}
	return *entry, record, true, nil
}

// UpdateExport mutates the matching export attempt. It never changes the
// recording entry's business UpdatedAt or status.
func (s *Storage) UpdateExport(recordingID, exportID string, now string, fn func(*ExportRecord) bool) (Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureWritableLocked(); err != nil {
		return Entry{}, false, err
	}
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return Entry{}, false, ErrExportNotFound
	}
	entry := &s.index.Recordings[idx]
	for i := len(entry.Exports) - 1; i >= 0; i-- {
		if entry.Exports[i].ExportID != exportID {
			continue
		}
		if !fn(&entry.Exports[i]) {
			return *entry, false, nil
		}
		entry.Exports[i].UpdatedAt = now
		if err := s.saveIndexLocked(); err != nil {
			return Entry{}, false, err
		}
		return *entry, true, nil
	}
	return Entry{}, false, ErrExportNotFound
}

// FindExport returns one retained export attempt.
func (s *Storage) FindExport(recordingID, exportID string) (Entry, ExportRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return Entry{}, ExportRecord{}, false
	}
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return Entry{}, ExportRecord{}, false
	}
	entry := s.index.Recordings[idx]
	for i := len(entry.Exports) - 1; i >= 0; i-- {
		if entry.Exports[i].ExportID == exportID {
			return entry, entry.Exports[i], true
		}
	}
	return Entry{}, ExportRecord{}, false
}

// ActiveExport returns the newest in-flight attempt for recordingID within
// scopeKey. A start rejected as busy uses it to hand the caller the export it
// can poll, instead of leaving it with an error it cannot act on. Attempts from
// another client scope are never returned: status is scope-isolated, so the
// caller could not poll them anyway, and they must not be exposed.
func (s *Storage) ActiveExport(recordingID, scopeKey string) (ExportRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return ExportRecord{}, false
	}
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return ExportRecord{}, false
	}
	entry := s.index.Recordings[idx]
	for i := len(entry.Exports) - 1; i >= 0; i-- {
		record := entry.Exports[i]
		if record.ScopeKey != scopeKey {
			continue
		}
		switch record.State {
		case ExportStatePreparing, ExportStateUploading:
			return record, true
		}
	}
	return ExportRecord{}, false
}

// FindExportByRequest returns the retained attempt for an idempotency key
// across the whole index, so a retried start can be answered before source
// prerequisites are checked again.
func (s *Storage) FindExportByRequest(scopeKey, requestID string) (Entry, ExportRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return Entry{}, ExportRecord{}, false
	}
	for i := range s.index.Recordings {
		entry := s.index.Recordings[i]
		for j := len(entry.Exports) - 1; j >= 0; j-- {
			record := entry.Exports[j]
			if record.ScopeKey == scopeKey && record.RequestID == requestID {
				return entry, record, true
			}
		}
	}
	return Entry{}, ExportRecord{}, false
}

// RecoverInterruptedExports marks preparing/uploading attempts left by a
// previous process as failed. It does not create a replacement upload task.
func (s *Storage) RecoverInterruptedExports(now string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureWritableLocked(); err != nil {
		return 0, err
	}
	changed := 0
	for i := range s.index.Recordings {
		for j := range s.index.Recordings[i].Exports {
			record := &s.index.Recordings[i].Exports[j]
			if record.State != ExportStatePreparing && record.State != ExportStateUploading {
				continue
			}
			record.State = ExportStateFailed
			record.ErrorCode = "EXPORT_INTERRUPTED"
			record.ErrorMessage = "导出任务在进程退出时中断"
			record.ErrorScope = "item"
			record.Retryable = true
			record.UpdatedAt = now
			changed++
		}
	}
	if changed == 0 {
		return 0, nil
	}
	if err := s.saveIndexLocked(); err != nil {
		return 0, err
	}
	return changed, nil
}
