package recording

import "testing"

func TestResultWriteUsesClientLabel(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)

	if _, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_new",
		Transcript:  &ResultTranscript{Text: "你好"},
	}, storage, testLogger{t}, SyncOptions{ClientLabel: "phone-a"}); err != nil {
		t.Fatal(err)
	}
	entry, ok := storage.FindByID("rec_new")
	if !ok || entry.ClientLabel != "phone-a" {
		t.Fatalf("new recording clientLabel = %q, want phone-a", entry.ClientLabel)
	}
}

// 老数据（或早期版本写入的）label 是 default，再次收到结果时按来源纠正。
func TestResultWriteBackfillsClientLabel(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_old", Metadata{Name: "会议"}, ""); err != nil {
		t.Fatal(err)
	}
	if entry, _ := storage.FindByID("rec_old"); entry.ClientLabel != "default" {
		t.Fatalf("precondition: clientLabel = %q", entry.ClientLabel)
	}

	if _, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_old",
		Summary:     &ResultSummary{Markdown: "# 总结"},
	}, storage, testLogger{t}, SyncOptions{ClientLabel: "phone-b"}); err != nil {
		t.Fatal(err)
	}
	entry, _ := storage.FindByID("rec_old")
	if entry.ClientLabel != "phone-b" {
		t.Fatalf("clientLabel = %q, want phone-b", entry.ClientLabel)
	}
}

// 没有来源信息时不应把已有 label 抹成 default。
func TestResultWriteKeepsLabelWhenUnknown(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)
	if _, err := storage.Ingest("rec_keep", Metadata{Name: "会议"}, "phone-c"); err != nil {
		t.Fatal(err)
	}
	if _, err := HandleRecordingResultWrite(ResultWriteParams{
		RecordingID: "rec_keep",
		Summary:     &ResultSummary{Markdown: "# 总结"},
	}, storage, testLogger{t}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	entry, _ := storage.FindByID("rec_keep")
	if entry.ClientLabel != "phone-c" {
		t.Fatalf("clientLabel = %q, want phone-c", entry.ClientLabel)
	}
}
