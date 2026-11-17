package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"syscall"
	"testing"

	"github.com/YoooClaw/cli/internal/recording"
)

func decodeGatewayError(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var body struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if body.OK {
		t.Fatalf("expected error response, got %s", rec.Body.String())
	}
	return body.Error.Code, body.Error.Message
}

// 导出受理阶段的索引写盘失败必须映射为契约里的 STORAGE_FULL，App 才会
// 停止整批同步，而不是把 INTERNAL_ERROR 当作可重试错误继续刷。
func TestWriteRecordingExportErrorMapsStorageFull(t *testing.T) {
	for name, err := range map[string]error{
		"wrapped enospc": fmt.Errorf("write export index: %w", syscall.ENOSPC),
		"text enospc":    errors.New("write /x/.abc.tmp: no space left on device"),
		"quotas":         errors.New("disk quota exceeded"),
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeRecordingExportError(rec, err)
			code, message := decodeGatewayError(t, rec)
			if code != "STORAGE_FULL" || message != recording.StorageFullMessage {
				t.Fatalf("code=%q message=%q", code, message)
			}
			// 对外文案固定，不能把带本地路径的底层错误透出。
			if message != "Not enough disk space" {
				t.Fatalf("message=%q", message)
			}
		})
	}
	for name, err := range map[string]error{
		"storage unavailable": recording.ErrStorageUnavailable,
		"permission":          fmt.Errorf("write export index: %w", syscall.EACCES),
	} {
		t.Run("not storage full/"+name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeRecordingExportError(rec, err)
			if code, _ := decodeGatewayError(t, rec); code == "STORAGE_FULL" {
				t.Fatalf("%v misclassified as STORAGE_FULL", err)
			}
		})
	}
}

// rename/delete 只改索引，空间不足同样必须是独立的 STORAGE_FULL。
func TestRecordingIndexWriteErrMapsStorageFull(t *testing.T) {
	rec := httptest.NewRecorder()
	recordingIndexWriteErr(rec, fmt.Errorf("rename: %w", syscall.ENOSPC))
	code, message := decodeGatewayError(t, rec)
	if code != "STORAGE_FULL" || message != recording.StorageFullMessage {
		t.Fatalf("code=%q message=%q", code, message)
	}

	rec = httptest.NewRecorder()
	recordingIndexWriteErr(rec, syscall.EACCES)
	code, message = decodeGatewayError(t, rec)
	if code != "INTERNAL_ERROR" || message != syscall.EACCES.Error() {
		t.Fatalf("code=%q message=%q", code, message)
	}
}
