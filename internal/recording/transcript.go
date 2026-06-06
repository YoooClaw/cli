package recording

import (
	"encoding/json"
	"fmt"
	"strings"
)

const transcriptDocumentSchemaVersion = 1

// TranscriptSource 是 transcript-data 的来源信息。
type TranscriptSource struct {
	Provider  string `json:"provider"`
	TaskID    string `json:"taskId,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	Status    string `json:"status,omitempty"`
}

// TranscriptSegment 是 transcript-data normalized.segments 的单段。
type TranscriptSegment struct {
	Text      string   `json:"text"`
	StartMS   *float64 `json:"startMs,omitempty"`
	EndMS     *float64 `json:"endMs,omitempty"`
	SpeakerID *int     `json:"speakerId,omitempty"`
}

// TranscriptItem 是对客户端展示的 sourceTextList 形状。
type TranscriptItem struct {
	Content   string   `json:"content"`
	SpeakerID *int     `json:"speakerId,omitempty"`
	StartTime *float64 `json:"startTime,omitempty"`
	EndTime   *float64 `json:"endTime,omitempty"`
}

// TranscriptDocument 是转写结构化主存储。
type TranscriptDocument struct {
	SchemaVersion int              `json:"schemaVersion"`
	RecordingID   string           `json:"recordingId"`
	GeneratedAt   string           `json:"generatedAt"`
	Source        TranscriptSource `json:"source"`
	Normalized    struct {
		Title    string              `json:"title,omitempty"`
		Category string              `json:"category,omitempty"`
		Summary  string              `json:"summary"`
		Text     string              `json:"text"`
		Segments []TranscriptSegment `json:"segments"`
	} `json:"normalized"`
	Raw any `json:"raw,omitempty"`
}

// BuildTranscriptDocument 构造 transcript-data 文档。
func BuildTranscriptDocument(recordingID string, source TranscriptSource, title, category, summary, text string, segments []TranscriptSegment, raw any) TranscriptDocument {
	if text == "" && len(segments) > 0 {
		parts := make([]string, 0, len(segments))
		for _, s := range segments {
			if strings.TrimSpace(s.Text) != "" {
				parts = append(parts, strings.TrimSpace(s.Text))
			}
		}
		text = strings.Join(parts, "\n\n")
	}
	doc := TranscriptDocument{
		SchemaVersion: transcriptDocumentSchemaVersion,
		RecordingID:   recordingID,
		GeneratedAt:   nowISO(),
		Source:        source,
		Raw:           raw,
	}
	doc.Normalized.Title = strings.TrimSpace(title)
	doc.Normalized.Category = strings.TrimSpace(category)
	doc.Normalized.Summary = strings.TrimSpace(summary)
	doc.Normalized.Text = strings.TrimSpace(text)
	doc.Normalized.Segments = segments
	return doc
}

// ExtractSourceTextListFromDocument 生成客户端 transcript[] 形状。
func ExtractSourceTextListFromDocument(doc TranscriptDocument) []TranscriptItem {
	if len(doc.Normalized.Segments) > 0 {
		out := make([]TranscriptItem, 0, len(doc.Normalized.Segments))
		for _, seg := range doc.Normalized.Segments {
			if strings.TrimSpace(seg.Text) == "" {
				continue
			}
			out = append(out, TranscriptItem{
				Content:   seg.Text,
				SpeakerID: seg.SpeakerID,
				StartTime: seg.StartMS,
				EndTime:   seg.EndMS,
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	if strings.TrimSpace(doc.Normalized.Text) != "" {
		return []TranscriptItem{{Content: doc.Normalized.Text}}
	}
	return nil
}

// ParseTranscriptDocument 解析 transcript-data 文档。
func ParseTranscriptDocument(raw []byte) (TranscriptDocument, bool) {
	var doc TranscriptDocument
	if json.Unmarshal(raw, &doc) != nil {
		return TranscriptDocument{}, false
	}
	if doc.RecordingID == "" || doc.GeneratedAt == "" || doc.Source.Provider == "" {
		return TranscriptDocument{}, false
	}
	return doc, true
}

// BuildTranscriptMarkdown 生成转写 Markdown。
func BuildTranscriptMarkdown(result TranscriptionResult, markers []Marker, recordingName string, durationSec float64, createdAt string) string {
	title := strings.TrimSpace(result.Summary)
	if title == "" {
		title = strings.TrimSpace(recordingName)
	}
	if title == "" {
		title = "录音转写"
	}
	if len([]rune(title)) > 10 {
		title = string([]rune(title)[:10])
	}

	var lines []string
	lines = append(lines, "# "+title, "")
	lines = append(lines, "> 录音名称："+recordingName)
	lines = append(lines, "> 时长："+formatDuration(durationSec))
	lines = append(lines, "> 创建时间："+createdAt)
	if len(markers) > 0 {
		lines = append(lines, fmt.Sprintf("> 关键点数：%d", len(markers)))
	}
	lines = append(lines, "", "---", "")

	if len(result.Segments) > 0 {
		sorted := append([]Marker(nil), markers...)
		sortMarkers(sorted)
		markerIdx := 0
		for _, seg := range result.Segments {
			for markerIdx < len(sorted) && sorted[markerIdx].TimestampMS <= valueOrZero(seg.StartMS) {
				lines = append(lines, "**[关键点 "+formatTimestamp(sorted[markerIdx].TimestampMS)+"]**", "")
				markerIdx++
			}
			if text := formatTranscriptSegmentText(seg); text != "" {
				lines = append(lines, text, "")
			}
		}
		for markerIdx < len(sorted) {
			lines = append(lines, "**[关键点 "+formatTimestamp(sorted[markerIdx].TimestampMS)+"]**", "")
			markerIdx++
		}
	} else if strings.TrimSpace(result.Text) != "" {
		if len(markers) > 0 {
			lines = append(lines, "### 关键点", "")
			for _, marker := range markers {
				lines = append(lines, "- **[关键点 "+formatTimestamp(marker.TimestampMS)+"]**")
			}
			lines = append(lines, "", "---", "")
		}
		lines = append(lines, result.Text, "")
	}

	return strings.Join(lines, "\n")
}

func sortMarkers(markers []Marker) {
	for i := 1; i < len(markers); i++ {
		for j := i; j > 0 && markers[j-1].TimestampMS > markers[j].TimestampMS; j-- {
			markers[j-1], markers[j] = markers[j], markers[j-1]
		}
	}
}

func valueOrZero(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func formatTranscriptSegmentText(seg TranscriptSegment) string {
	text := strings.TrimSpace(seg.Text)
	if text == "" {
		return ""
	}
	prefix := ""
	if seg.StartMS != nil {
		prefix = "[" + formatTimestamp(*seg.StartMS) + "] "
	}
	if seg.SpeakerID != nil {
		prefix += fmt.Sprintf("说话人%d：", *seg.SpeakerID)
	}
	return prefix + text
}

func formatTimestamp(ms float64) string {
	total := int(ms / 1000)
	min := total / 60
	sec := total % 60
	return fmt.Sprintf("%02d:%02d", min, sec)
}

func formatDuration(seconds float64) string {
	total := int(seconds)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func extractSummary(text string) string {
	for _, part := range strings.FieldsFunc(text, func(r rune) bool {
		return r == '。' || r == '！' || r == '？' || r == '\n'
	}) {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		runes := []rune(trimmed)
		if len(runes) <= 10 {
			return trimmed
		}
		return string(runes[:10])
	}
	return "录音转写"
}
