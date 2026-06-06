package image

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testLogger struct{ t *testing.T }

func (l testLogger) Info(msg string) { l.t.Log("[INFO] " + msg) }
func (l testLogger) Warn(msg string) { l.t.Log("[WARN] " + msg) }

func TestIngestDownloadsImage(t *testing.T) {
	t.Parallel()
	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/img.png" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-bytes"))
	}))
	defer oss.Close()

	dir := filepath.Join(t.TempDir(), "images")
	result, err := Ingest(dir, SyncPayload{
		ImageID: "img_1",
		Image: Metadata{
			OssImageURL: oss.URL + "/img.png",
			CreatedAt:   "2026-06-04T17:16:50+08:00",
			MimeType:    "image/png",
			SourceApp:   "camera",
		},
	}, "phone-a", 1024, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Status != "syncing" {
		t.Fatalf("unexpected ingest result: %+v", result)
	}

	waitFor(t, time.Second, func() bool {
		entries := ReadIndex(dir)
		return len(entries) == 1 && entries[0].Status == "synced" && entries[0].LocalFile != nil
	})
	entry := ReadIndex(dir)[0]
	if entry.ClientLabel != "phone-a" {
		t.Fatalf("client label mismatch: %q", entry.ClientLabel)
	}
	data, err := os.ReadFile(filepath.Join(dir, *entry.LocalFile))
	if err != nil || string(data) != "png-bytes" {
		t.Fatalf("image file mismatch: %q err=%v", string(data), err)
	}

	dedup, err := Ingest(dir, SyncPayload{ImageID: "img_1", Image: entry.Metadata}, "phone-a", 1024, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	if !dedup.Deduped || dedup.Status != "synced" {
		t.Fatalf("expected dedupe: %+v", dedup)
	}
}

func waitFor(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
