package transfercloud

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestUploadAboveFormerLimitReachesCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate((5 << 30) + 1)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.URL.Path != apiPath+"/create" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		// Stop at the mock service, without uploading the sparse test file.
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := c.Upload(context.Background(), path); err == nil {
		t.Fatal("expected mock rejection")
	}
	if called != 1 {
		t.Fatalf("create calls=%d; large file was blocked locally", called)
	}
}

func TestCompleteAboveFormerLimitAndNonpositiveSize(t *testing.T) {
	for _, size := range []int64{(5 << 30) + 1, 25 << 30, 0, -1} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]any{"taskId": testID, "objectKey": testKey, "status": "COMPLETED", "fileSize": size})
		})
		receipt, err := c.Complete(context.Background(), testID, testKey)
		if size <= 0 {
			if err == nil {
				t.Fatalf("accepted size %d", size)
			}
		} else if err != nil || int64(receipt.FileSize) != size {
			t.Fatalf("size=%d receipt=%v err=%v", size, receipt, err)
		}
	}
}
