package recording

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/YoooClaw/cli/internal/fsutil"
)

const maxSafeWriteRevision int64 = 9007199254740991

// WriteError 是 Gateway 与 Relay 共用的有序写入错误。
type WriteError struct {
	Code                 string `json:"code"`
	Message              string `json:"message"`
	RecordingID          string `json:"recordingId,omitempty"`
	WriteRevision        *int64 `json:"writeRevision,omitempty"`
	CurrentWriteRevision *int64 `json:"currentWriteRevision,omitempty"`
}

func (e *WriteError) Error() string { return e.Message }

func newWriteError(code, message, recordingID string, revision, current *int64) *WriteError {
	return &WriteError{
		Code: code, Message: message, RecordingID: recordingID,
		WriteRevision: cloneInt64(revision), CurrentWriteRevision: cloneInt64(current),
	}
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// UnmarshalJSON 保留 writeRevision 的字段存在性；null/字符串/浮点不能退回 legacy。
func (p *ResultWriteParams) UnmarshalJSON(data []byte) error {
	type alias ResultWriteParams
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = ResultWriteParams(decoded)

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["writeRevision"]; ok {
		p.writeRevisionPresent = true
		var number json.Number
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil {
			if parsed, ok := value.(json.Number); ok {
				number = parsed
				if floatValue, err := strconv.ParseFloat(number.String(), 64); err == nil &&
					!math.IsNaN(floatValue) && !math.IsInf(floatValue, 0) &&
					math.Trunc(floatValue) == floatValue && floatValue >= 1 &&
					floatValue <= float64(maxSafeWriteRevision) {
					revision := int64(floatValue)
					p.WriteRevision = &revision
					p.writeRevisionValid = true
				}
			}
		}
	}
	if raw, ok := fields["recording"]; ok && string(raw) != "null" {
		var recordingFields map[string]json.RawMessage
		if json.Unmarshal(raw, &recordingFields) == nil {
			if markerRaw, ok := recordingFields["markers"]; ok {
				p.recordingMarkersPresent = true
				p.recordingMarkersValid = validRawMarkers(markerRaw)
			}
			if durationRaw, ok := recordingFields["duration_sec"]; ok {
				p.recordingDurationPresent = true
				p.recordingDurationValid = validRawNonNegativeNumber(durationRaw)
			}
			_, p.recordingLocationPresent = recordingFields["location"]
		}
	}
	return nil
}

func (p ResultWriteParams) hasWriteRevision() bool {
	return p.writeRevisionPresent || p.WriteRevision != nil
}

// HasWriteRevision 返回 JSON 是否显式携带有序写入字段。
func (p ResultWriteParams) HasWriteRevision() bool { return p.hasWriteRevision() }

func (t *ResultTranscript) UnmarshalJSON(data []byte) error {
	type alias ResultTranscript
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*t = ResultTranscript(decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["text"]; ok {
		t.textPresent = rawJSONString(raw)
	}
	if raw, ok := fields["markdown"]; ok {
		t.markdownPresent = rawJSONString(raw)
	}
	if raw, ok := fields["segments"]; ok {
		t.segmentsPresent = true
		t.segmentsValid = validRawTranscriptSegments(raw)
	}
	return nil
}

func (s *ResultSummary) UnmarshalJSON(data []byte) error {
	type alias ResultSummary
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = ResultSummary(decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["markdown"]; ok {
		s.markdownPresent = rawJSONString(raw)
	}
	if raw, ok := fields["structured"]; ok {
		s.structuredPresent = true
		trimmed := bytes.TrimSpace(raw)
		s.structuredValid = len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
	}
	return nil
}

func rawJSONString(raw json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func validRawNonNegativeNumber(raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	parsed, err := number.Float64()
	return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) && parsed >= 0
}

func validRawMarkers(raw json.RawMessage) bool {
	var markers []map[string]json.RawMessage
	if json.Unmarshal(raw, &markers) != nil || markers == nil || len(markers) > 100 {
		return false
	}
	for _, marker := range markers {
		indexRaw, indexOK := marker["index"]
		timestampRaw, timestampOK := marker["timestamp_ms"]
		if !indexOK || !timestampOK || !validRawNonNegativeInteger(indexRaw) || !validRawNonNegativeNumber(timestampRaw) {
			return false
		}
	}
	return true
}

func validRawNonNegativeInteger(raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	parsed, err := number.Float64()
	return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) && parsed >= 0 && math.Trunc(parsed) == parsed && parsed <= float64(maxSafeWriteRevision)
}

func validRawTranscriptSegments(raw json.RawMessage) bool {
	var segments []map[string]json.RawMessage
	if json.Unmarshal(raw, &segments) != nil || segments == nil {
		return false
	}
	for _, segment := range segments {
		textRaw, ok := segment["text"]
		if !ok || !rawJSONString(textRaw) {
			return false
		}
		for _, key := range []string{"startMs", "endMs"} {
			if numberRaw, present := segment[key]; present && !validRawFiniteNumber(numberRaw) {
				return false
			}
		}
		if speakerRaw, present := segment["speakerId"]; present && !validRawInteger(speakerRaw) {
			return false
		}
	}
	return true
}

func validRawFiniteNumber(raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	parsed, err := number.Float64()
	return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
}

func validRawInteger(raw json.RawMessage) bool {
	if !validRawFiniteNumber(raw) {
		return false
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	_ = decoder.Decode(&value)
	number, _ = value.(json.Number)
	parsed, _ := number.Float64()
	return math.Trunc(parsed) == parsed && math.Abs(parsed) <= float64(maxSafeWriteRevision)
}

type orderedWriteMode string

const (
	orderedAudioOnly orderedWriteMode = "audio-only"
	orderedFull      orderedWriteMode = "full"
)

type validatedOrderedWrite struct {
	recordingID string
	revision    int64
	ossURL      string
	metadata    Metadata
	mode        orderedWriteMode
	transcript  *ResultTranscript
	summary     *ResultSummary
	clientLabel string
}

type orderedArtifact struct {
	key      string
	relative string
	staged   string
	final    string
}

type preparedOrderedArtifacts struct {
	artifacts  []orderedArtifact
	transcript []TranscriptItem
	summary    string
	title      string
}

func handleOrderedRecordingResultWrite(params ResultWriteParams, storage *Storage, logger Logger, opts SyncOptions) (ResultWriteResult, error) {
	request, err := validateOrderedWrite(params, opts.ClientLabel)
	if err != nil {
		return ResultWriteResult{}, err
	}
	apply, entry, err := storage.preflightOrderedWrite(request)
	if err != nil {
		return ResultWriteResult{}, err
	}
	prepared := preparedOrderedArtifacts{}
	if apply && request.mode == orderedFull {
		prepared, err = prepareOrderedArtifacts(request, storage)
		if err != nil {
			return ResultWriteResult{}, newWriteError("RESULT_APPLY_FAILED", err.Error(), request.recordingID, &request.revision, nil)
		}
	}
	committed := false
	if apply {
		entry, committed, err = storage.commitOrderedWrite(request, prepared)
		if err != nil {
			storage.cleanupOrderedArtifacts(prepared.artifacts)
			return ResultWriteResult{}, err
		}
		if !committed {
			storage.cleanupOrderedArtifacts(prepared.artifacts)
		}
	}
	entry, err = storage.reconcileOrderedAudio(request.recordingID, request.revision, request.ossURL)
	if err != nil {
		return ResultWriteResult{}, err
	}
	if entry == nil || entry.WriteRevision == nil || *entry.WriteRevision != request.revision {
		var current *int64
		if entry != nil {
			current = entry.WriteRevision
		}
		return ResultWriteResult{}, newWriteError("STALE_WRITE", fmt.Sprintf("writeRevision %d was superseded", request.revision), request.recordingID, &request.revision, current)
	}
	if entry.AudioStatus == AudioStatusFailed {
		return ResultWriteResult{}, newWriteError("AUDIO_DOWNLOAD_FAILED", firstNonEmpty(entry.LastError, "audio download failed"), request.recordingID, &request.revision, entry.WriteRevision)
	}

	if committed {
		storage.cancelSupersededOrderedTask(request.recordingID, request.revision, request.ossURL)
		emitResultStatusAtRevision(request.recordingID, &request.revision, storage, logger, opts.NotifyStatus, prepared.transcript, prepared.summary, prepared.title)
	}
	if entry.AudioStatus == AudioStatusPending || entry.AudioStatus == AudioStatusDownloading {
		queueOrderedAudioDownload(request, storage, logger, opts)
	}
	current, _ := storage.FindByID(request.recordingID)
	return orderedWriteResult(current), nil
}

func validateOrderedWrite(params ResultWriteParams, clientLabel string) (validatedOrderedWrite, error) {
	recordingID := strings.TrimSpace(params.RecordingID)
	if recordingID == "" || !isSafeRecordingID(recordingID) {
		return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "recordingId is required and must be a safe storage id", recordingID, nil, nil)
	}
	validRevision := params.WriteRevision != nil && (params.writeRevisionValid || !params.writeRevisionPresent)
	if !validRevision || *params.WriteRevision < 1 || *params.WriteRevision > maxSafeWriteRevision {
		return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "writeRevision must be a safe integer greater than or equal to 1", recordingID, nil, nil)
	}
	revision := *params.WriteRevision
	ossURL := strings.TrimSpace(params.OssURL)
	if ossURL == "" {
		return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "ossUrl is required for ordered recording writes", recordingID, &revision, nil)
	}
	if params.Recording == nil {
		return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "recording metadata is required for ordered recording writes", recordingID, &revision, nil)
	}
	metadata := *params.Recording
	metadata.Name = strings.TrimSpace(metadata.Name)
	if metadata.Name == "" {
		return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "recording.name is required", recordingID, &revision, nil)
	}
	if strings.TrimSpace(metadata.OssAudioURL) != ossURL {
		return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "recording.oss_audio_url must equal ossUrl", recordingID, &revision, nil)
	}
	if params.writeRevisionPresent && (!params.recordingDurationPresent || !params.recordingDurationValid) {
		return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "recording.duration_sec must be a non-negative finite number", recordingID, &revision, nil)
	}
	if math.IsNaN(metadata.DurationSec) || math.IsInf(metadata.DurationSec, 0) || metadata.DurationSec < 0 {
		return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "recording.duration_sec must be a non-negative finite number", recordingID, &revision, nil)
	}
	metadata.DurationSec = normalizeDurationSeconds(metadata.DurationSec)
	metadata.DurationDisplay = FormatDurationDisplay(metadata.DurationSec)
	metadata.CreatedAt = strings.TrimSpace(metadata.CreatedAt)
	if _, ok := ParseTime(metadata.CreatedAt); !ok {
		return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "recording.created_at must be a valid timestamp", recordingID, &revision, nil)
	}
	markersPresent := params.recordingMarkersPresent || (!params.writeRevisionPresent && metadata.Markers != nil)
	if !markersPresent || (params.writeRevisionPresent && !params.recordingMarkersValid) || len(metadata.Markers) > 100 {
		return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "recording.markers must be an array with at most 100 items", recordingID, &revision, nil)
	}
	for index, marker := range metadata.Markers {
		if marker.Index < 0 || math.IsNaN(marker.TimestampMS) || math.IsInf(marker.TimestampMS, 0) || marker.TimestampMS < 0 {
			return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", fmt.Sprintf("recording.markers[%d] is invalid", index), recordingID, &revision, nil)
		}
	}
	if (params.recordingLocationPresent && metadata.Location == nil) || (metadata.Location != nil && !validLocation(metadata.Location)) {
		return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "recording.location must contain finite latitude and longitude", recordingID, &revision, nil)
	}
	hasTranscript, hasSummary := params.Transcript != nil, params.Summary != nil
	if hasTranscript != hasSummary {
		return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "ordered full results must provide both transcript and summary", recordingID, &revision, nil)
	}
	mode := orderedAudioOnly
	if hasTranscript {
		if !validOrderedTranscript(params.Transcript) {
			return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "transcript must contain an explicit text, markdown, or segments field", recordingID, &revision, nil)
		}
		if !validOrderedSummary(params.Summary) {
			return validatedOrderedWrite{}, newWriteError("INVALID_PARAMS", "summary must contain an explicit markdown or non-null structured field", recordingID, &revision, nil)
		}
		mode = orderedFull
	}
	metadata.OssAudioURL = ossURL
	metadata.Status = "syncing_openclaw"
	request := validatedOrderedWrite{
		recordingID: recordingID, revision: revision, ossURL: ossURL, metadata: metadata,
		mode: mode, transcript: params.Transcript, summary: params.Summary,
		clientLabel: firstNonEmpty(strings.TrimSpace(clientLabel), "default"),
	}
	return request, nil
}

func isSafeRecordingID(value string) bool {
	return fsutil.IsSafeName(value)
}

func validLocation(value any) bool {
	raw, ok := value.(map[string]any)
	if !ok {
		data, err := json.Marshal(value)
		if err != nil || json.Unmarshal(data, &raw) != nil {
			return false
		}
	}
	latitude, latOK := numericValue(raw["latitude"])
	longitude, lonOK := numericValue(raw["longitude"])
	return latOK && lonOK && !math.IsNaN(latitude) && !math.IsInf(latitude, 0) && !math.IsNaN(longitude) && !math.IsInf(longitude, 0)
}

func numericValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func validOrderedTranscript(value *ResultTranscript) bool {
	if value == nil {
		return false
	}
	explicit := value.textPresent || value.markdownPresent || value.segmentsPresent
	if !explicit {
		explicit = value.Text != "" || value.Markdown != "" || value.Segments != nil
	}
	if !explicit {
		return false
	}
	if value.segmentsPresent && !value.segmentsValid {
		return false
	}
	for _, segment := range value.Segments {
		if segment.StartMS != nil && (math.IsNaN(*segment.StartMS) || math.IsInf(*segment.StartMS, 0)) {
			return false
		}
		if segment.EndMS != nil && (math.IsNaN(*segment.EndMS) || math.IsInf(*segment.EndMS, 0)) {
			return false
		}
	}
	return true
}

func validOrderedSummary(value *ResultSummary) bool {
	if value == nil {
		return false
	}
	if value.markdownPresent || value.Markdown != "" {
		return true
	}
	if value.structuredPresent {
		return value.structuredValid
	}
	trimmed := bytes.TrimSpace(value.Structured)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func prepareOrderedArtifacts(request validatedOrderedWrite, storage *Storage) (preparedOrderedArtifacts, error) {
	transcript := *request.transcript
	summary := *request.summary
	title := firstNonEmpty(strings.TrimSpace(transcript.Title), request.metadata.Name, request.recordingID)
	segments := normalizeResultSegments(transcript.Segments)
	textValue := firstNonEmpty(normalizeMultiline(transcript.Text), joinSegmentTexts(segments), normalizeMultiline(transcript.Markdown))
	source := TranscriptSource{Provider: "app", Delivery: "result-write"}
	if transcript.Source != nil {
		source.Provider = firstNonEmpty(strings.TrimSpace(transcript.Source.Provider), "app")
		source.TaskID = strings.TrimSpace(transcript.Source.TaskID)
		source.RequestID = strings.TrimSpace(transcript.Source.RequestID)
		source.Status = strings.TrimSpace(transcript.Source.Status)
	}
	doc := BuildTranscriptDocument(request.recordingID, source, title, strings.TrimSpace(transcript.Category), normalizeMultiline(transcript.Brief), textValue, segments, transcript)
	if generated := strings.TrimSpace(transcript.GeneratedAt); generated != "" {
		doc.GeneratedAt = generated
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return preparedOrderedArtifacts{}, err
	}
	data = append(data, '\n')
	markdown := normalizeMultiline(transcript.Markdown)
	if strings.TrimSpace(markdown) == "" {
		markdown = buildResultTranscriptMarkdown(request.metadata, title, textValue, segments)
	}
	summaryMarkdown := normalizeMultiline(summary.Markdown)
	if strings.TrimSpace(summaryMarkdown) == "" {
		summaryMarkdown = buildStructuredSummaryMarkdown(summary.Structured)
	}
	attempt, err := randomAttemptID()
	if err != nil {
		return preparedOrderedArtifacts{}, err
	}
	base := fmt.Sprintf("%s.r%d.%s", request.recordingID, request.revision, attempt)
	specs := []struct {
		key, dir, name string
		data           []byte
	}{
		{"transcriptDataFile", transcriptDataDirName, base + ".json", data},
		{"transcriptFile", transcriptsDirName, base + ".transcript.md", []byte(ensureTrailingNewline(markdown))},
		{"summaryFile", summariesDirName, base + ".summary.md", []byte(ensureTrailingNewline(summaryMarkdown))},
	}
	prepared := preparedOrderedArtifacts{title: title, transcript: ExtractSourceTextListFromDocument(doc), summary: strings.TrimSpace(summaryMarkdown)}
	for _, spec := range specs {
		final := filepath.Join(storage.dir, spec.dir, spec.name)
		staged := final + ".staged"
		artifact := orderedArtifact{key: spec.key, relative: filepath.ToSlash(filepath.Join(spec.dir, spec.name)), staged: staged, final: final}
		prepared.artifacts = append(prepared.artifacts, artifact)
		file, openErr := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if openErr != nil {
			storage.cleanupOrderedArtifacts(prepared.artifacts)
			return preparedOrderedArtifacts{}, openErr
		}
		_, writeErr := file.Write(spec.data)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			storage.cleanupOrderedArtifacts(prepared.artifacts)
			if writeErr != nil {
				return preparedOrderedArtifacts{}, writeErr
			}
			return preparedOrderedArtifacts{}, closeErr
		}
	}
	return prepared, nil
}

func randomAttemptID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (s *Storage) preflightOrderedWrite(request validatedOrderedWrite) (bool, *Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(request.recordingID)
	if idx < 0 {
		return true, nil, nil
	}
	entry := s.index.Recordings[idx]
	if entry.WriteRevision != nil && *entry.WriteRevision > request.revision {
		return false, nil, newWriteError("STALE_WRITE", fmt.Sprintf("writeRevision %d is older than current revision %d", request.revision, *entry.WriteRevision), request.recordingID, &request.revision, entry.WriteRevision)
	}
	if entry.WriteRevision != nil && *entry.WriteRevision == request.revision {
		if request.mode == orderedAudioOnly || s.hasCompleteOrderedTextLocked(entry) {
			return false, &entry, nil
		}
	}
	return true, &entry, nil
}

func (s *Storage) commitOrderedWrite(request validatedOrderedWrite, prepared preparedOrderedArtifacts) (*Entry, bool, error) {
	s.mu.Lock()
	idx := s.findIndexLocked(request.recordingID)
	var current *Entry
	if idx >= 0 {
		copy := s.index.Recordings[idx]
		current = &copy
	}
	if current != nil && current.WriteRevision != nil && *current.WriteRevision > request.revision {
		s.mu.Unlock()
		return nil, false, newWriteError("STALE_WRITE", fmt.Sprintf("writeRevision %d is older than current revision %d", request.revision, *current.WriteRevision), request.recordingID, &request.revision, current.WriteRevision)
	}
	if current != nil && current.WriteRevision != nil && *current.WriteRevision == request.revision {
		if request.mode == orderedAudioOnly || s.hasCompleteOrderedTextLocked(*current) {
			s.mu.Unlock()
			copy := *current
			return &copy, false, nil
		}
	}
	repairing := current != nil && current.WriteRevision != nil && *current.WriteRevision == request.revision
	audioReusable := current != nil && s.hasReusableAudioLocked(*current)
	nextIndex, err := cloneRecordingIndex(s.index)
	if err != nil {
		s.mu.Unlock()
		return nil, false, newWriteError("RESULT_APPLY_FAILED", err.Error(), request.recordingID, &request.revision, nil)
	}
	now := nowISO()
	next := Entry{ID: request.recordingID, IngestedAt: now}
	if current != nil {
		next = *current
	}
	if !repairing {
		next.Metadata = request.metadata
		next.ClientLabel = request.clientLabel
		next.WriteRevision = cloneInt64(&request.revision)
		next.LastError = ""
	}
	if audioReusable {
		next.AudioStatus = AudioStatusDownloaded
		if info, statErr := os.Stat(filepath.Join(s.dir, next.AudioFile)); statErr == nil {
			next.Metadata.FileSizeDisplay = FormatShortFileSize(info.Size())
		}
	} else if !repairing {
		next.AudioFile = ""
		next.AudioSourceURL = ""
		next.Metadata.FileSizeDisplay = ""
		next.AudioStatus = AudioStatusPending
	} else if next.AudioStatus != AudioStatusFailed {
		next.AudioStatus = AudioStatusPending
	}
	if request.mode == orderedFull {
		next.Status = StatusTranscribed
		next.Metadata.Status = next.Status
	} else if !preserveAudioOnlyBusinessStatus(next.Status) {
		if audioReusable {
			next.Status = StatusSynced
		} else {
			next.Status = "syncing_openclaw"
		}
		next.Metadata.Status = next.Status
	}
	next.UpdatedAt = now

	moved := make([]orderedArtifact, 0, len(prepared.artifacts))
	for _, artifact := range prepared.artifacts {
		if err := os.Rename(artifact.staged, artifact.final); err != nil {
			for _, item := range moved {
				_ = os.Remove(item.final)
			}
			s.mu.Unlock()
			return nil, false, newWriteError("RESULT_APPLY_FAILED", err.Error(), request.recordingID, &request.revision, nil)
		}
		_ = os.Chmod(artifact.final, 0o600)
		moved = append(moved, artifact)
		switch artifact.key {
		case "transcriptDataFile":
			next.TranscriptDataFile = artifact.relative
		case "transcriptFile":
			next.TranscriptFile = artifact.relative
		case "summaryFile":
			next.SummaryFile = artifact.relative
		}
	}
	if request.mode == orderedFull {
		next.Title = firstNonEmpty(strings.TrimSpace(prepared.title), request.recordingID)
	}
	oldText := []string{}
	if current != nil && request.mode == orderedFull {
		oldText = []string{current.TranscriptDataFile, current.TranscriptFile, current.SummaryFile}
	}
	if idx >= 0 {
		nextIndex.Recordings[idx] = next
	} else {
		nextIndex.Recordings = append(nextIndex.Recordings, next)
	}
	if err := fsWriteIndex(s.indexPath, nextIndex); err != nil {
		for _, artifact := range moved {
			_ = os.Remove(artifact.final)
		}
		s.mu.Unlock()
		return nil, false, newWriteError("RESULT_APPLY_FAILED", err.Error(), request.recordingID, &request.revision, nil)
	}
	s.index = nextIndex
	s.mu.Unlock()
	s.removeUnreferencedOrderedPaths(oldText)
	return &next, true, nil
}

func cloneRecordingIndex(input indexWrapper) (indexWrapper, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return indexWrapper{}, err
	}
	var output indexWrapper
	err = json.Unmarshal(data, &output)
	return output, err
}

func fsWriteIndex(path string, index indexWrapper) error {
	return writeJSONAtomic(path, index)
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data)
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".ordered-index-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(temporary)
		}
	}()
	_ = file.Chmod(0o600)
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	committed = true
	return os.Chmod(path, 0o600)
}

func (s *Storage) hasCompleteOrderedTextLocked(entry Entry) bool {
	for _, relative := range []string{entry.TranscriptDataFile, entry.TranscriptFile, entry.SummaryFile} {
		if !s.relativeNonEmptyFileLocked(relative) {
			return false
		}
	}
	return true
}

func (s *Storage) hasReusableAudioLocked(entry Entry) bool {
	return s.relativeNonEmptyFileLocked(entry.AudioFile)
}

func (s *Storage) relativeNonEmptyFileLocked(relative string) bool {
	if relative == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(s.dir, relative))
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func preserveAudioOnlyBusinessStatus(status string) bool {
	switch status {
	case StatusSynced, StatusTranscribing, StatusTranscribeFailed, StatusTranscribed:
		return true
	default:
		return false
	}
}

func (s *Storage) cleanupOrderedArtifacts(artifacts []orderedArtifact) {
	for _, artifact := range artifacts {
		_ = os.Remove(artifact.staged)
		_ = os.Remove(artifact.final)
	}
}

func (s *Storage) removeUnreferencedOrderedPaths(paths []string) {
	s.mu.Lock()
	referenced := map[string]bool{}
	for _, entry := range s.index.Recordings {
		for _, relative := range []string{entry.AudioFile, entry.SrtFile, entry.TranscriptDataFile, entry.TranscriptFile, entry.SummaryFile} {
			if relative != "" {
				referenced[relative] = true
			}
		}
	}
	for _, relative := range paths {
		if relative != "" && !referenced[relative] {
			s.removeRelativeLocked(relative)
		}
	}
	s.mu.Unlock()
}

func (s *Storage) reconcileOrderedAudio(recordingID string, revision int64, ossURL string) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.findIndexLocked(recordingID)
	if idx < 0 {
		return nil, nil
	}
	current := s.index.Recordings[idx]
	if current.WriteRevision == nil || *current.WriteRevision != revision || strings.TrimSpace(current.Metadata.OssAudioURL) != strings.TrimSpace(ossURL) {
		return &current, nil
	}
	if current.AudioStatus != AudioStatusDownloaded || s.hasReusableAudioLocked(current) {
		return &current, nil
	}
	nextIndex, err := cloneRecordingIndex(s.index)
	if err != nil {
		return nil, err
	}
	next := nextIndex.Recordings[idx]
	previous := next.AudioFile
	next.AudioFile = ""
	next.AudioSourceURL = ""
	next.Metadata.FileSizeDisplay = ""
	next.AudioStatus = AudioStatusPending
	next.UpdatedAt = nowISO()
	nextIndex.Recordings[idx] = next
	if err := fsWriteIndex(s.indexPath, nextIndex); err != nil {
		return nil, err
	}
	s.index = nextIndex
	if previous != "" {
		_ = os.Remove(filepath.Join(s.dir, previous))
	}
	return &next, nil
}

func orderedWriteResult(entry Entry) ResultWriteResult {
	return ResultWriteResult{
		OK: true, RecordingID: entry.ID, WriteRevision: cloneInt64(entry.WriteRevision),
		TransferStatus: entry.Status, AudioStatus: entry.AudioStatus,
		AudioFile: entry.AudioFile,
		Stored:    ResultWriteStored{TranscriptDataFile: entry.TranscriptDataFile, TranscriptFile: entry.TranscriptFile, SummaryFile: entry.SummaryFile},
	}
}
