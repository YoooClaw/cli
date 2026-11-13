package recording

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpeakerNameWriteExportRoundTrip(t *testing.T) {
	var transcript ResultTranscript
	if err := json.Unmarshal([]byte(`{"segments":[{"text":"第一句","speakerId":0,"speakerName":" 双蛋 "},{"text":"第二句","speakerId":0,"speakerName":"朱"},{"text":"旧分段","speakerId":1},{"text":"匿名","speakerName":null}]}`), &transcript); err != nil {
		t.Fatal(err)
	}
	storage, entry := seedExportPayloadRecording(t, &transcript, nil)
	raw, err := os.ReadFile(filepath.Join(storage.dir, entry.TranscriptDataFile))
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := ParseTranscriptDocument(raw)
	if !ok {
		t.Fatal("cannot read persisted document")
	}
	items := ExtractSourceTextListFromDocument(doc)
	if len(items) != 4 || items[0].SpeakerName != "双蛋" || items[1].SpeakerName != "朱" || items[2].SpeakerName != "" || items[3].SpeakerName != "" {
		t.Fatalf("items=%+v", items)
	}
	source, err := prepareExportSource(storage, entry.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	tr := source.payload.Transcript
	if tr.Segments[0].SpeakerName != "双蛋" || tr.Segments[1].SpeakerName != "朱" || *tr.Segments[0].SpeakerID != 0 {
		t.Fatalf("transcript=%+v", tr)
	}
	if !strings.Contains(tr.Markdown, "双蛋：第一句") || !strings.Contains(tr.Markdown, "朱：第二句") {
		t.Fatalf("markdown=%s", tr.Markdown)
	}
	// The exported JSON must be accepted by result.write without losing per-segment names.
	wire, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	var imported ResultTranscript
	if err := json.Unmarshal(wire, &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Segments[1].SpeakerName != "朱" {
		t.Fatalf("round trip=%s", wire)
	}
	legacy, _ := json.Marshal(imported.Segments[2])
	if strings.Contains(string(legacy), "speakerName") {
		t.Fatalf("legacy shape changed: %s", legacy)
	}
	// A second sync updates the name even when the speaker ID is unchanged.
	imported.Segments[0].SpeakerName = "新名字"
	imported.Markdown = ""
	if _, err := HandleRecordingResultWrite(ResultWriteParams{RecordingID: entry.ID, Transcript: &imported}, storage, testLogger{t}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	updated, err := prepareExportSource(storage, entry.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.payload.Transcript.Segments[0].SpeakerName != "新名字" || updated.sourceVersion == source.sourceVersion {
		t.Fatal("rename was not reflected in export and source version")
	}
}

func TestSpeakerNameMarkdownCompatibility(t *testing.T) {
	id := 1
	for _, tc := range []struct{ name, want string }{{" 张三 ", "张三：正文"}, {"", "说话人1：正文"}, {"  ", "说话人1：正文"}} {
		got := formatTranscriptSegmentText(TranscriptSegment{Text: "正文", SpeakerID: &id, SpeakerName: tc.name})
		if got != tc.want {
			t.Fatalf("name=%q got=%q want=%q", tc.name, got, tc.want)
		}
	}
}

// Supplied Markdown remains authoritative: callers must regenerate it after
// editing segments, or omit it to request CLI-generated Markdown.
func TestSpeakerNameSuppliedMarkdown(t *testing.T) {
	for _, markdown := range []string{"# 自定义格式\n\n旧名字：正文\n", "# 自定义格式\n\n新名字：正文\n"} {
		storage, entry := seedExportPayloadRecording(t, &ResultTranscript{
			Segments: []ResultTranscriptSegment{{Text: "正文", SpeakerName: "新名字"}}, Markdown: markdown,
		}, nil)
		source, err := prepareExportSource(storage, entry.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if source.payload.Transcript.Markdown != markdown || source.payload.Transcript.Segments[0].SpeakerName != "新名字" {
			t.Fatalf("payload=%+v", source.payload.Transcript)
		}
	}
}

func TestASRSpeakerNameAndSafeMarkdown(t *testing.T) {
	var result recordResult
	if err := json.Unmarshal([]byte(`{"sourceTextList":[{"content":"正文","speakerId":0,"speakerName":" 李四\n---\n# 标题\u0000[*](x) "}]}`), &result); err != nil {
		t.Fatal(err)
	}
	_, segments := extractTextAndSegments(&result, 1000)
	if len(segments) != 1 || segments[0].SpeakerName != "李四\n---\n# 标题\x00[*](x)" {
		t.Fatalf("segments=%+v", segments)
	}
	want := `李四 \-\-\- \# 标题 \[\*\]\(x\)：`
	if got := transcriptSpeakerPrefix(segments[0], true); got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
	if got := transcriptSpeakerPrefix(segments[0], false); got != want {
		t.Fatalf("result prefix=%q", got)
	}
	id := 2
	if got := transcriptSpeakerPrefix(TranscriptSegment{SpeakerID: &id}, false); got != "" {
		t.Fatalf("legacy result.write changed: %q", got)
	}
	doc := BuildTranscriptDocument("rec", TranscriptSource{Provider: "model-proxy"}, "", "", "", "", segments, nil)
	doc.Normalized.Segments[0].SpeakerName = " 张三 "
	exported, _ := buildExportTranscript(&doc, "", "")
	if exported.Segments[0].SpeakerName != "张三" {
		t.Fatal("export did not trim historical name")
	}
}
