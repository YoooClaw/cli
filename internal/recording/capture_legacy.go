package recording

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/YoooClaw/cli/internal/capturerecording"
	"github.com/YoooClaw/cli/internal/fsutil"
)

// ReadSourceSnapshot overlays unmigrated Capture history without writing on reads.
// Current IDs shadow history before callers apply date or visibility filters.
func (s *Storage) ReadSourceSnapshot(ctx context.Context, query capturerecording.Query) ([]Entry, error) {
	entries, err := s.ReadIndexSnapshot(ctx)
	if err != nil || !s.capture {
		return entries, err
	}
	return MergeCaptureHistory(ctx, filepath.Dir(s.dir), entries, query)
}

func MergeCaptureHistory(ctx context.Context, voice string, entries []Entry, query capturerecording.Query) ([]Entry, error) {
	ids := map[string]bool{}
	for _, e := range entries {
		ids[e.ID] = true
	}
	visible := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if !entry.LegacyCaptureDeleted {
			visible = append(visible, entry)
		}
	}
	entries = visible
	if voice == "" {
		return entries, nil
	}
	query.MaxIndexBytes = SummaryIndexLimit
	legacy, err := capturerecording.List(ctx, voice, query)
	if err != nil {
		return nil, err
	}
	root := capturerecording.RecordingsPath(voice)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	relative := func(path *string) string {
		if path == nil {
			return ""
		}
		rel, err := filepath.Rel(root, *path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return ""
		}
		return filepath.ToSlash(rel)
	}
	for _, old := range legacy {
		if ids[old.ID] {
			continue
		}
		e := Entry{ID: old.ID, SourceType: SourceCaptureApp, ClientLabel: "default", Title: old.Title,
			Metadata: Metadata{Name: old.Title, CreatedAt: old.RecordedAt, DurationSec: float64(old.DurationMS) / 1000},
			Status:   StatusSynced, AudioFile: relative(old.AudioPath), LegacyCaptureTranscript: relative(old.TranscriptPath), SummaryFile: relative(old.SummaryPath),
			LegacyCapture: true, IngestedAt: old.RecordedAt, UpdatedAt: old.RecordedAt}
		if e.AudioFile != "" {
			e.AudioStatus = AudioStatusDownloaded
		}
		if e.LegacyCaptureTranscript != "" {
			e.Status = StatusTranscribed
		}
		switch old.Status {
		case "recording", "finalizing", "uploading", "transcribing":
			e.Status = StatusTranscribing
		case "failed":
			e.Status = StatusTranscribeFailed
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (s *Storage) LookupSourceByID(ctx context.Context, id string) (Entry, bool, error) {
	entry, found, err := s.LookupByID(id)
	if err != nil || found || !s.capture {
		return entry, found, err
	}
	entries, err := s.ReadSourceSnapshot(ctx, capturerecording.Query{})
	if err != nil {
		return Entry{}, false, err
	}
	for _, e := range entries {
		if e.ID == id {
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// AdoptCaptureHistory persists only the selected entry when export needs task
// storage. The audio and historical text stay in place; an existing row wins.
func (s *Storage) AdoptCaptureHistory(entry Entry) (Entry, error) {
	if !s.capture || !entry.LegacyCapture || !fsutil.IsSafeName(entry.ID) {
		return Entry{}, errors.New("invalid capture history entry")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.prepareWriteLocked(); err != nil {
		return Entry{}, err
	}
	if idx := s.findIndexLocked(entry.ID); idx >= 0 {
		return s.index.Recordings[idx], nil
	}
	s.index.Recordings = append(s.index.Recordings, entry)
	if err := s.saveIndexLocked(); err != nil {
		s.index.Recordings = s.index.Recordings[:len(s.index.Recordings)-1]
		return Entry{}, err
	}
	return entry, nil
}

// Capture stored either normalized data, a model-proxy response or DashScope JSON.
func parseCaptureTranscript(raw []byte, entry Entry) (*TranscriptDocument, error) {
	var doc TranscriptDocument
	if json.Unmarshal(raw, &doc) == nil && doc.SchemaVersion > 0 {
		return &doc, nil
	}
	var dash struct {
		Transcripts []struct {
			Sentences []struct {
				Text    string   `json:"text"`
				Start   *float64 `json:"begin_time"`
				End     *float64 `json:"end_time"`
				Speaker *int     `json:"speaker_id"`
			} `json:"sentences"`
		} `json:"transcripts"`
	}
	if json.Unmarshal(raw, &dash) == nil && dash.Transcripts != nil {
		var segments []TranscriptSegment
		for _, transcript := range dash.Transcripts {
			for _, sentence := range transcript.Sentences {
				segments = append(segments, TranscriptSegment{Text: sentence.Text, StartMS: sentence.Start, EndMS: sentence.End, SpeakerID: sentence.Speaker})
			}
		}
		doc = BuildTranscriptDocument(entry.ID, TranscriptSource{Provider: "dashscope"}, entry.Title, "", "", "", segments, nil)
		doc.GeneratedAt = entry.UpdatedAt
		return &doc, nil
	}
	response, message := unwrapStatusResponse(raw)
	if message != "" || response.RecordResult == nil {
		return nil, errors.New("unsupported capture transcript format")
	}
	text, segments := extractTextAndSegments(response.RecordResult, entry.Metadata.DurationSec*1000)
	if text == "" {
		text = response.RecordResult.SourceText
	}
	doc = BuildTranscriptDocument(entry.ID, TranscriptSource{Provider: "model-proxy", TaskID: response.TaskID, RequestID: response.RequestID, Status: response.Status}, response.RecordResult.Title, response.RecordResult.Category, "", text, segments, nil)
	doc.GeneratedAt = entry.UpdatedAt
	return &doc, nil
}
