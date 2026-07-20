package recording

import (
	"testing"
)

// 回归：recordingId 会拼进 transcript-data/transcripts/summaries 文件名，
// 穿越形式必须在写入前被拒。
func TestResultWriteRejectsTraversalRecordingID(t *testing.T) {
	t.Parallel()
	storage := newResultStorage(t)
	for _, id := range []string{"../evil", "..", "a/b", `a\b`} {
		_, err := HandleRecordingResultWrite(ResultWriteParams{
			RecordingID: id,
			Summary:     &ResultSummary{Markdown: "# x"},
		}, storage, testLogger{t}, SyncOptions{})
		if err == nil || err.Error() != "recordingId is invalid" {
			t.Errorf("HandleRecordingResultWrite(id=%q) err = %v, want invalid", id, err)
		}
	}
}
