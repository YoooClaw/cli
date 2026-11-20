package recording

import "testing"

func TestParseCaptureTranscriptFormats(t *testing.T) {
	for name, raw := range map[string]string{
		"proxy":      `{"data":{"recordResult":{"sourceTextList":[{"content":"正文","speakerId":0,"speakerName":"小王","startTime":100,"endTime":2000}]}}}`,
		"normalized": `{"schemaVersion":1,"normalized":{"text":"正文","segments":[{"text":"正文","speakerId":0,"startMs":100,"endMs":2000}]}}`,
		"dashscope":  `{"transcripts":[{"sentences":[{"text":"正文","speaker_id":0,"begin_time":100,"end_time":2000}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			doc, err := parseCaptureTranscript([]byte(raw), Entry{ID: "legacy"})
			if err != nil {
				t.Fatal(err)
			}
			segments := doc.Normalized.Segments
			if len(segments) != 1 || segments[0].Text != "正文" || segments[0].SpeakerID == nil || *segments[0].SpeakerID != 0 || segments[0].StartMS == nil || *segments[0].StartMS != 100 {
				t.Fatalf("doc: %+v", doc)
			}
			if name == "proxy" && segments[0].SpeakerName != "小王" {
				t.Fatalf("speaker lost: %+v", segments[0])
			}
		})
	}
	for _, raw := range []string{`{`, `{"unknown":true}`, `{"success":false,"message":"failed","data":null}`} {
		if _, err := parseCaptureTranscript([]byte(raw), Entry{}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
