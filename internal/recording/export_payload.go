package recording

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxExportTextBytes int64 = 64 << 20

var (
	ErrExportSourceNotFound = errors.New("export source not found")
	ErrExportSourceChanged  = errors.New("export source changed")
)

// ExportSourceKind 标识导出失败时对应的源文件类别。它只说明"是哪一类文件"，
// 用于客户端提示和日志定位；本地相对路径只写进 daemon 日志。
type ExportSourceKind string

const (
	ExportSourceRecording      ExportSourceKind = "recording"
	ExportSourceAudio          ExportSourceKind = "audio"
	ExportSourceTranscriptData ExportSourceKind = "transcriptData"
	ExportSourceTranscript     ExportSourceKind = "transcript"
	ExportSourceSummary        ExportSourceKind = "summary"
	ExportSourceSrt            ExportSourceKind = "srt"
)

// exportSourceError 在源文件错误上补充类别和相对路径。Error() 只在本地日志
// 里输出路径，返回给客户端的 message 由 sourceFailureMessage 单独生成。
type exportSourceError struct {
	kind ExportSourceKind
	path string
	err  error
}

func (e *exportSourceError) Error() string {
	if e.path == "" {
		return fmt.Sprintf("%s (%s)", e.err, e.kind)
	}
	return fmt.Sprintf("%s (%s: %s)", e.err, e.kind, e.path)
}

func (e *exportSourceError) Unwrap() error { return e.err }

func wrapExportSource(kind ExportSourceKind, path string, err error) error {
	if err == nil {
		return nil
	}
	return &exportSourceError{kind: kind, path: path, err: err}
}

func exportSourceKindOf(err error) ExportSourceKind {
	var sourceErr *exportSourceError
	if errors.As(err, &sourceErr) {
		return sourceErr.kind
	}
	return ""
}

// ExportRecording is the metadata projection allowed in writePayload. It
// deliberately omits the old OSS URL and local paths.
type ExportRecording struct {
	Name            string   `json:"name"`
	CreatedAt       string   `json:"created_at"`
	DurationSec     float64  `json:"duration_sec"`
	DurationDisplay string   `json:"duration_display"`
	RecordingType   string   `json:"recording_type"`
	Location        any      `json:"location,omitempty"`
	Markers         []Marker `json:"markers"`
}

// ExportPayload mirrors recordings.result.write params.
type ExportPayload struct {
	RecordingID    string            `json:"recordingId"`
	SourceType     string            `json:"sourceType"`
	OssTaskID      string            `json:"ossTaskId"`
	OssURL         string            `json:"ossUrl"`
	DurationMillis *float64          `json:"durationMillis,omitempty"`
	Recording      ExportRecording   `json:"recording"`
	Transcript     *ResultTranscript `json:"transcript,omitempty"`
	Summary        *ResultSummary    `json:"summary,omitempty"`
}

type ExportContentPresent struct {
	Transcript bool `json:"transcript"`
	Summary    bool `json:"summary"`
}

type exportSource struct {
	entry          Entry
	audioPath      string
	fileName       string
	mimeType       string
	sizeBytes      int64
	sha256         string
	sourceVersion  string
	identity       ExportAudioIdentity
	payload        ExportPayload
	contentPresent ExportContentPresent
}

type exportVersionMaterial struct {
	RecordingID           string              `json:"recordingId"`
	Name                  string              `json:"name"`
	Title                 string              `json:"title,omitempty"`
	CreatedAt             string              `json:"createdAt"`
	DurationSec           float64             `json:"durationSec"`
	RecordingType         string              `json:"recordingType"`
	Location              any                 `json:"location,omitempty"`
	Markers               []Marker            `json:"markers,omitempty"`
	TranscriptDataSHA256  string              `json:"transcriptDataSha256,omitempty"`
	TranscriptMarkdownSHA string              `json:"transcriptMarkdownSha256,omitempty"`
	SummaryMarkdownSHA    string              `json:"summaryMarkdownSha256,omitempty"`
	SrtSHA256             string              `json:"srtSha256,omitempty"`
	Audio                 ExportAudioIdentity `json:"audio"`
}

// prepareExportSource reads one consistent snapshot of the audio and text
// files, builds writePayload, and derives a sourceVersion. hashAudio is true
// for the initial export; status checks use the cheap file identity instead.
func prepareExportSource(storage *Storage, recordingID string, hashAudio bool) (exportSource, error) {
	entry, ok := storage.FindByID(recordingID)
	if !ok {
		return exportSource{}, wrapExportSource(ExportSourceRecording, "", ErrExportSourceNotFound)
	}
	if entry.AudioFile == "" || entry.AudioStatus != AudioStatusDownloaded {
		return exportSource{}, wrapExportSource(ExportSourceAudio, entry.AudioFile, ErrExportSourceNotFound)
	}
	root, err := os.OpenRoot(storage.dir)
	if err != nil {
		return exportSource{}, err
	}
	defer root.Close()

	audio, err := root.Open(entry.AudioFile)
	if err != nil {
		if os.IsNotExist(err) {
			return exportSource{}, wrapExportSource(ExportSourceAudio, entry.AudioFile, ErrExportSourceNotFound)
		}
		return exportSource{}, wrapExportSource(ExportSourceAudio, entry.AudioFile, err)
	}
	defer audio.Close()
	info, err := audio.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxAudioExportBytes {
		return exportSource{}, wrapExportSource(ExportSourceAudio, entry.AudioFile, ErrExportSourceNotFound)
	}
	device, inode := exportFileIdentity(info)
	identity := ExportAudioIdentity{Device: device, Inode: inode, Size: info.Size(), ModTime: info.ModTime().UnixNano()}
	digest := ""
	if hashAudio {
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(audio, MaxAudioExportBytes+1))
		if err != nil || n != info.Size() {
			return exportSource{}, wrapExportSource(ExportSourceAudio, entry.AudioFile, ErrExportSourceChanged)
		}
		digest = hex.EncodeToString(h.Sum(nil))
	}
	fileName := filepath.Base(entry.AudioFile)
	mimeType, ok := exportAudioMIME(fileName)
	if !ok {
		return exportSource{}, fmt.Errorf("unsupported recording audio extension: %s", filepath.Ext(fileName))
	}

	dataRaw, _, err := readExportText(root, entry.TranscriptDataFile)
	if err != nil {
		return exportSource{}, wrapExportSource(ExportSourceTranscriptData, entry.TranscriptDataFile, err)
	}
	transcriptMD, _, err := readExportText(root, entry.TranscriptFile)
	if err != nil {
		return exportSource{}, wrapExportSource(ExportSourceTranscript, entry.TranscriptFile, err)
	}
	summaryMD, _, err := readExportText(root, entry.SummaryFile)
	if err != nil {
		return exportSource{}, wrapExportSource(ExportSourceSummary, entry.SummaryFile, err)
	}
	srtRaw, _, err := readExportText(root, entry.SrtFile)
	if err != nil {
		return exportSource{}, wrapExportSource(ExportSourceSrt, entry.SrtFile, err)
	}

	var doc *TranscriptDocument
	if len(dataRaw) > 0 {
		var parsed TranscriptDocument
		if err := json.Unmarshal(dataRaw, &parsed); err != nil {
			return exportSource{}, wrapExportSource(ExportSourceTranscriptData, entry.TranscriptDataFile, fmt.Errorf("invalid transcript data: %w", err))
		}
		doc = &parsed
	}
	transcript, transcriptPresent := buildExportTranscript(doc, string(transcriptMD), string(srtRaw))
	summary, summaryPresent := buildExportSummary(string(summaryMD))
	if transcript == nil && summary == nil {
		// Audio-only compatibility: old DTOs carry empty objects.
		transcript = &ResultTranscript{Segments: []ResultTranscriptSegment{}}
		summary = &ResultSummary{Format: "markdown", Markdown: ""}
	}
	markers := entry.Metadata.Markers
	if markers == nil {
		markers = []Marker{}
	}
	payload := ExportPayload{
		RecordingID: entry.ID,
		// 接收方按 sourceType 把导出的录音写回对应来源；旧索引条目回退 mobile_app。
		SourceType: entry.RecordingSource(),
		Recording: ExportRecording{
			Name: entry.Metadata.Name, CreatedAt: entry.Metadata.CreatedAt,
			DurationSec: entry.Metadata.DurationSec, DurationDisplay: entry.Metadata.DurationDisplay,
			RecordingType: NormalizeRecordingType(entry.Metadata.RecordingType),
			Location:      entry.Metadata.Location, Markers: markers,
		},
		Transcript: transcript,
		Summary:    summary,
	}
	if entry.Metadata.DurationSec > 0 {
		millis := entry.Metadata.DurationSec * 1000
		payload.DurationMillis = &millis
	}
	material := exportVersionMaterial{
		RecordingID: entry.ID, Name: entry.Metadata.Name, Title: entry.Title, CreatedAt: entry.Metadata.CreatedAt,
		DurationSec: entry.Metadata.DurationSec, RecordingType: NormalizeRecordingType(entry.Metadata.RecordingType),
		Location: entry.Metadata.Location, Markers: entry.Metadata.Markers,
		TranscriptDataSHA256:  hashBytes(dataRaw),
		TranscriptMarkdownSHA: hashBytes(transcriptMD),
		SummaryMarkdownSHA:    hashBytes(summaryMD),
		SrtSHA256:             hashBytes(srtRaw),
		Audio:                 identity,
	}
	versionRaw, err := json.Marshal(material)
	if err != nil {
		return exportSource{}, err
	}
	return exportSource{
		entry: entry, audioPath: filepath.Join(storage.dir, filepath.FromSlash(entry.AudioFile)),
		fileName: fileName, mimeType: mimeType, sizeBytes: info.Size(), sha256: digest,
		sourceVersion: hashBytes(versionRaw), identity: identity, payload: payload,
		contentPresent: ExportContentPresent{Transcript: transcriptPresent, Summary: summaryPresent},
	}, nil
}

func readExportText(root *os.Root, rel string) ([]byte, bool, error) {
	if strings.TrimSpace(rel) == "" {
		return nil, false, nil
	}
	f, err := root.Open(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, ErrExportSourceNotFound
		}
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxExportTextBytes {
		return nil, false, ErrExportSourceChanged
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxExportTextBytes+1))
	if err != nil || int64(len(raw)) > maxExportTextBytes {
		return nil, false, ErrExportSourceChanged
	}
	return raw, true, nil
}

func buildExportTranscript(doc *TranscriptDocument, markdown, srt string) (*ResultTranscript, bool) {
	if doc != nil {
		segments := make([]ResultTranscriptSegment, 0, len(doc.Normalized.Segments))
		for _, segment := range doc.Normalized.Segments {
			segments = append(segments, ResultTranscriptSegment{
				Text: segment.Text, StartMS: segment.StartMS, EndMS: segment.EndMS, SpeakerID: segment.SpeakerID, SpeakerName: strings.TrimSpace(segment.SpeakerName),
			})
		}
		out := &ResultTranscript{
			GeneratedAt: doc.GeneratedAt, Title: doc.Normalized.Title, Category: doc.Normalized.Category,
			Brief: doc.Normalized.Summary, Text: doc.Normalized.Text, Segments: segments, Markdown: markdown,
		}
		if doc.Source.Provider != "" || doc.Source.TaskID != "" || doc.Source.RequestID != "" || doc.Source.Status != "" {
			out.Source = &ResultTranscriptSource{
				Provider: doc.Source.Provider, TaskID: doc.Source.TaskID,
				RequestID: doc.Source.RequestID, Status: doc.Source.Status,
			}
		}
		return out, hasTranscriptContent(out)
	}
	if strings.TrimSpace(markdown) != "" {
		out := &ResultTranscript{Markdown: markdown}
		return out, hasRealMarkdown(markdown)
	}
	if parsed, ok := parseSRT(srt); ok {
		return parsed, hasTranscriptContent(parsed)
	}
	return nil, false
}

func hasTranscriptContent(t *ResultTranscript) bool {
	if t == nil {
		return false
	}
	if strings.TrimSpace(t.Text) != "" {
		return true
	}
	for _, segment := range t.Segments {
		if strings.TrimSpace(segment.Text) != "" {
			return true
		}
	}
	return false
}

func buildExportSummary(markdown string) (*ResultSummary, bool) {
	if strings.TrimSpace(markdown) == "" {
		return nil, false
	}
	return &ResultSummary{Format: "markdown", Markdown: markdown}, hasRealMarkdown(markdown)
}

func hasRealMarkdown(markdown string) bool {
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return true
	}
	return false
}

func hashBytes(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

var exportAudioMIMEs = map[string]string{
	".m4a": "audio/mp4", ".mp4": "audio/mp4", ".mp3": "audio/mpeg", ".wav": "audio/wav",
	".aac": "audio/aac", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/opus",
	".amr": "audio/amr", ".flac": "audio/flac", ".3gp": "audio/3gpp",
}

func exportAudioMIME(name string) (string, bool) {
	mime, ok := exportAudioMIMEs[strings.ToLower(filepath.Ext(name))]
	return mime, ok
}

// parseSRT keeps text and timings from legacy SRT-only recordings.
func parseSRT(raw string) (*ResultTranscript, bool) {
	normalized := strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	blocks := strings.Split(normalized, "\n\n")
	segments := make([]ResultTranscriptSegment, 0, len(blocks))
	var texts []string
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 {
			continue
		}
		timeLine := ""
		timeIndex := -1
		for i, line := range lines {
			if strings.Contains(line, "-->") {
				timeLine, timeIndex = line, i
				break
			}
		}
		if timeIndex < 0 {
			continue
		}
		start, end, ok := parseSRTRange(timeLine)
		if !ok {
			continue
		}
		text := strings.TrimSpace(strings.Join(lines[timeIndex+1:], "\n"))
		if text == "" {
			continue
		}
		startMS, endMS := float64(start), float64(end)
		segments = append(segments, ResultTranscriptSegment{Text: text, StartMS: &startMS, EndMS: &endMS})
		texts = append(texts, text)
	}
	if len(segments) == 0 {
		return nil, false
	}
	return &ResultTranscript{Text: strings.Join(texts, "\n"), Segments: segments}, true
}

func parseSRTRange(line string) (int64, int64, bool) {
	parts := strings.Split(line, "-->")
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, ok := parseSRTTime(parts[0])
	if !ok {
		return 0, 0, false
	}
	end, ok := parseSRTTime(parts[1])
	return start, end, ok
}

func parseSRTTime(raw string) (int64, bool) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return 0, false
	}
	value := strings.Replace(fields[0], ",", ".", 1)
	dot := strings.IndexByte(value, '.')
	millis := int64(0)
	if dot >= 0 {
		frac := value[dot+1:]
		value = value[:dot]
		if len(frac) > 3 {
			frac = frac[:3]
		}
		frac += strings.Repeat("0", 3-len(frac))
		parsed, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, false
		}
		millis = parsed
	}
	clock, err := time.Parse("15:04:05", value)
	if err != nil {
		return 0, false
	}
	return int64(clock.Hour())*3600000 + int64(clock.Minute())*60000 + int64(clock.Second())*1000 + millis, true
}

// MaxAudioExportBytes mirrors the download/upload guard without introducing a
// recording-specific transfer limit.
var MaxAudioExportBytes int64 = 5 << 30
