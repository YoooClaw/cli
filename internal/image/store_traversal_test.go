package image

import (
	"strings"
	"testing"
)

// 回归：imageId 会拼进 files/<id>.<ext> 落盘路径，穿越形式必须在入口被拒。
func TestIngestRejectsTraversalImageID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, id := range []string{"../evil", "..", "a/b", `a\b`, "c:evil"} {
		_, err := Ingest(dir, SyncPayload{
			ImageID: id,
			Image:   Metadata{OssImageURL: "https://oss/x.png", CreatedAt: "2026-06-04T17:16:50+08:00"},
		}, "phone-a", 1024, testLogger{t})
		if err == nil || !strings.Contains(err.Error(), "imageId") {
			t.Errorf("Ingest(imageId=%q) err = %v, want imageId 非法错误", id, err)
		}
	}
}
