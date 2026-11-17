package recording

import (
	"encoding/json"
	"testing"
)

func TestMetadataExtraRetryUnchanged(t *testing.T) {
	current := Metadata{DurationDisplay: FormatDurationDisplay(0), Extra: map[string]json.RawMessage{"future": json.RawMessage(`{"items":[2,1]}`)}}
	params := ResultWriteParams{Recording: &Metadata{Extra: map[string]json.RawMessage{"future": json.RawMessage(`{ "items": [2,1] }`)}}}
	_, _, changed := mergeResultMetadata(current, params)
	if changed {
		t.Fatal("identical extra marked changed")
	}
}
func TestNewSummaryClearsOldStructuredData(t *testing.T) {
	old := &ResultSummary{Structured: json.RawMessage(`{"old":true}`), Format: "old", GeneratedAt: "old"}
	if got := mergeSummaryData(old, &ResultSummary{Markdown: "new"}); got != nil {
		t.Fatalf("stale summary retained: %+v", got)
	}
}

func TestResetAndDeleteClearResultExtra(t *testing.T) {
	for _, reset := range []bool{true, false} {
		t.Run(map[bool]string{true: "reset", false: "delete"}[reset], func(t *testing.T) {
			s, _ := seedExportPayloadRecording(t, nil, nil)
			if err := s.updateEntry("rec_payload", func(e *Entry) {
				e.ResultExtra = map[string]json.RawMessage{"old": json.RawMessage(`true`)}
				e.SummaryData = &ResultSummary{Format: "old"}
			}); err != nil {
				t.Fatal(err)
			}
			if reset {
				if _, err := s.Ingest("rec_payload", Metadata{Name: "new", OssAudioURL: "https://example.test/new"}, ""); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.Delete("rec_payload", true); err != nil {
					t.Fatal(err)
				}
			}
			entry, found := s.FindByID("rec_payload")
			if !found {
				t.Fatal("recording missing")
			}
			if len(entry.ResultExtra) != 0 || entry.SummaryData != nil {
				t.Fatal("stale result metadata retained")
			}
		})
	}
}

func TestMetadataOnlySummaryKeepsAudioOnlyDTO(t *testing.T) {
	s, _ := seedExportPayloadRecording(t, nil, nil)
	if err := s.updateEntry("rec_payload", func(e *Entry) {
		e.TranscriptFile = ""
		e.TranscriptDataFile = ""
		e.SummaryFile = ""
		e.SummaryData = &ResultSummary{Format: "markdown"}
	}); err != nil {
		t.Fatal(err)
	}
	source, err := prepareExportSource(s, "rec_payload", false)
	if err != nil {
		t.Fatal(err)
	}
	if source.payload.Transcript == nil || source.payload.Summary == nil {
		t.Fatal("audio-only DTO lost empty objects")
	}
}
func TestStructuredReadPreservesEmptySegments(t *testing.T) {
	doc := TranscriptDocument{}
	doc.Normalized.Segments = []TranscriptSegment{{Text: "a"}, {Text: "", Extra: map[string]json.RawMessage{"future": json.RawMessage(`[2,1]`)}}, {Text: "b"}}
	items := ExtractSourceTextListFromDocument(doc)
	if len(items) != 3 || string(items[1].Extra["future"]) != `[2,1]` {
		t.Fatal("empty segment or ordered extras lost")
	}
}

func TestReplacementSummaryFormat(t *testing.T) {
	for _, format := range []string{"", "app-custom"} {
		t.Run(format, func(t *testing.T) {
			s, _ := seedExportPayloadRecording(t, nil, nil)
			if err := s.updateEntry("rec_payload", func(e *Entry) {
				e.SummaryData = &ResultSummary{Format: "old-format", Structured: json.RawMessage(`{"old":true}`)}
			}); err != nil {
				t.Fatal(err)
			}
			_, err := HandleRecordingResultWrite(ResultWriteParams{RecordingID: "rec_payload", Summary: &ResultSummary{Markdown: "replacement", Format: format}}, s, testLogger{t}, SyncOptions{})
			if err != nil {
				t.Fatal(err)
			}
			source, err := prepareExportSource(s, "rec_payload", false)
			if err != nil {
				t.Fatal(err)
			}
			expected := format
			if expected == "" {
				expected = "markdown"
			}
			if source.payload.Summary.Format != expected || source.payload.Summary.Structured != nil {
				t.Fatalf("replacement summary: %+v", source.payload.Summary)
			}
		})
	}
}
