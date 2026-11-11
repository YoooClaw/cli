package transfercloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

const recordingTaskID = "recording-task-1"
const recordingObjectKey = "users/audio/20260924/0123456789abcdef0123456789abcdef.m4a"

func recordingTask() map[string]any {
	return map[string]any{
		"taskId":    recordingTaskID,
		"fileType":  "RECORDING",
		"fileName":  "call.m4a",
		"objectKey": recordingObjectKey,
		"bucket":    "bucket",
		"region":    "cn-hangzhou",
		"endpoint":  "https://oss-cn-hangzhou.aliyuncs.com",
		"credentials": map[string]string{
			"accessKeyId":     "temporary-id",
			"accessKeySecret": "temporary-secret",
			"securityToken":   "temporary-token",
			"expiration":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

func TestRecordingCreateAndCompleteUseRECORDINGTask(t *testing.T) {
	var paths []string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case apiPath + "/create":
			if body["fileType"] != "RECORDING" || body["fileName"] != "call.m4a" {
				t.Errorf("create body=%v", body)
			}
			reply(w, recordingTask())
		case apiPath + "/complete":
			if body["taskId"] != recordingTaskID || body["objectKey"] != recordingObjectKey {
				t.Errorf("complete body=%v", body)
			}
			reply(w, map[string]any{
				"taskId": recordingTaskID, "objectKey": recordingObjectKey, "status": "COMPLETED",
				"fileSize": "250035", "signedUrl": signedURL, "expiresAt": time.Now().Add(time.Hour).UnixMilli(),
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	ctx := context.Background()
	task, err := c.CreateRecording(ctx, "call.m4a")
	if err != nil {
		t.Fatal(err)
	}
	if task.TaskID != recordingTaskID || task.FileType != "RECORDING" {
		t.Fatalf("task=%+v", task)
	}
	receipt, err := c.CompleteRecording(ctx, recordingTaskID, recordingObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.FileSize != 250035 || receipt.SignedURL != signedURL || !receipt.ExpiresAt.After(time.Now()) {
		t.Fatalf("receipt=%+v", receipt)
	}
	if len(paths) != 2 {
		t.Fatalf("paths=%v", paths)
	}
}

func TestRecordingCreateRejectsNonAudioNames(t *testing.T) {
	called := false
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	for _, name := range []string{"migration.tar.gz", "../call.m4a", "dir/call.m4a", "", ".m4a", "call.exe", strings.Repeat("a", 256) + ".m4a"} {
		if _, err := c.CreateRecording(context.Background(), name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	if called {
		t.Fatal("invalid name reached the service")
	}
}

func TestRecordingCreateRejectsWrongTaskResponse(t *testing.T) {
	for _, field := range []string{"fileType", "fileName", "taskId", "objectKey", "bucket", "endpoint", "region", "credentials"} {
		t.Run(field, func(t *testing.T) {
			task := recordingTask()
			switch field {
			case "credentials":
				task[field].(map[string]string)["expiration"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
			case "fileType":
				task[field] = "DATA_TRANSFER"
			case "objectKey":
				task[field] = "users/audio/20260924/not-audio.txt"
			case "taskId":
				task[field] = ""
			case "bucket":
				task[field] = "Bad_Bucket"
			case "region":
				task[field] = "bad region"
			default:
				task[field] = "changed"
			}
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, task) })
			if _, err := c.CreateRecording(context.Background(), "call.m4a"); err == nil {
				t.Fatal("accepted invalid recording task")
			}
		})
	}
}

func TestRecordingRefreshKeepsTaskIdentity(t *testing.T) {
	for _, field := range []string{"taskId", "objectKey", "bucket", "endpoint", "region", "fileType", "fileName", "credentials"} {
		t.Run(field, func(t *testing.T) {
			next := recordingTask()
			switch field {
			case "credentials":
				next[field].(map[string]string)["expiration"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
			case "bucket":
				next[field] = "other-bucket"
			case "endpoint":
				next[field] = "https://oss-cn-beijing.aliyuncs.com"
			case "region":
				next[field] = "cn-beijing"
			case "fileName":
				next[field] = "other.m4a"
			case "fileType":
				next[field] = "DATA_TRANSFER"
			default:
				next[field] = "changed"
			}
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]string
				json.NewDecoder(r.Body).Decode(&body)
				if body["taskId"] != recordingTaskID {
					t.Errorf("refresh body=%v", body)
				}
				reply(w, next)
			})
			original := recordingTask()
			var task UploadTask
			raw, _ := json.Marshal(original)
			_ = json.Unmarshal(raw, &task)
			if _, err := c.RefreshRecording(context.Background(), &task); err == nil {
				t.Fatal("accepted changed task or invalid credentials")
			}
		})
	}
}

func TestRecordingCompleteRejectsInvalidReceipts(t *testing.T) {
	for _, scenario := range []string{"expired", "zero-size", "wrong-object", "not-completed", "http-url", "port-url", "missing-url"} {
		t.Run(scenario, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				data := map[string]any{
					"taskId": recordingTaskID, "objectKey": recordingObjectKey, "status": "COMPLETED",
					"fileSize": 100, "signedUrl": signedURL, "expiresAt": time.Now().Add(time.Hour).UnixMilli(),
				}
				switch scenario {
				case "expired":
					data["expiresAt"] = time.Now().Add(-time.Minute).UnixMilli()
				case "zero-size":
					data["fileSize"] = 0
				case "wrong-object":
					data["objectKey"] = strings.Replace(recordingObjectKey, "0123456789abcdef", "fedcba9876543210", 1)
				case "not-completed":
					data["status"] = "UPLOADING"
				case "http-url":
					data["signedUrl"] = "http://bucket.oss-cn-hangzhou.aliyuncs.com/a.m4a"
				case "port-url":
					data["signedUrl"] = "https://bucket.oss-cn-hangzhou.aliyuncs.com:443/a.m4a"
				case "missing-url":
					data["signedUrl"] = ""
				}
				reply(w, data)
			})
			if _, err := c.CompleteRecording(context.Background(), recordingTaskID, recordingObjectKey); err == nil {
				t.Fatal("accepted invalid receipt")
			}
		})
	}
}

func TestRecordingObjectKeyValidation(t *testing.T) {
	valid := []string{
		recordingObjectKey,
		"users/recording/20260924/0123456789abcdef0123456789abcdef.ogg",
	}
	for _, key := range valid {
		if !validRecordingObjectKey(key) {
			t.Errorf("rejected %q", key)
		}
	}
	invalid := []string{
		"", "users/audio/20260924/x.m4a", "users/audio/20260924/../../etc/passwd",
		"users/audio/2026-09-24/" + strings.Repeat("a", 32) + ".m4a",
		"users/audio/20260924/" + strings.Repeat("a", 32) + ".tar.gz",
		"/users/audio/20260924/" + strings.Repeat("a", 32) + ".m4a",
		"users/audio/20260924/sub/" + strings.Repeat("a", 32) + ".m4a",
	}
	for _, key := range invalid {
		if validRecordingObjectKey(key) {
			t.Errorf("accepted %q", key)
		}
	}
}

func TestRecordingUploadRejectsUnsupportedExtensionBeforeCreate(t *testing.T) {
	called := false
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	if _, err := c.UploadRecording(context.Background(), "/tmp/call.txt"); err == nil {
		t.Fatal("accepted unsupported file")
	}
	if called {
		t.Fatal("unsupported file reached the service")
	}
}

func TestRecordingContentType(t *testing.T) {
	for name, want := range map[string]string{
		"call.m4a": "audio/mp4", "call.MP3": "audio/mpeg", "call.wav": "audio/wav", "call.ogg": "audio/ogg",
	} {
		got, ok := recordingContentType(name)
		if !ok || got != want {
			t.Errorf("%s -> %q, %v", name, got, ok)
		}
	}
	for _, name := range []string{"call", "call.", ".m4a", "call.tar.gz"} {
		if _, ok := recordingContentType(name); ok {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestRecordingCompleteAcceptsSizeAboveFormerLimit(t *testing.T) {
	const size = (5 << 30) + 1
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{
			"taskId": recordingTaskID, "objectKey": recordingObjectKey, "status": "COMPLETED",
			"fileSize": fmt.Sprint(size), "signedUrl": signedURL, "expiresAt": time.Now().Add(time.Hour).UnixMilli(),
		})
	})
	receipt, err := c.CompleteRecording(context.Background(), recordingTaskID, recordingObjectKey)
	if err != nil || int64(receipt.FileSize) != size {
		t.Fatalf("large receipt = %+v, err %v", receipt, err)
	}
}

func TestRecordingUploadRetriesCompleteWithoutReupload(t *testing.T) {
	var creates, puts, completes int
	var reported string
	c := mockOSSClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiPath + "/create":
			if creates++; creates > 1 {
				t.Errorf("create retried %d times", creates)
			}
			reply(w, recordingTask())
		case apiPath + "/complete":
			completes++
			if completes == 1 {
				w.WriteHeader(503)
				return
			}
			reply(w, map[string]any{
				"taskId": recordingTaskID, "objectKey": recordingObjectKey, "status": "COMPLETED",
				"fileSize": "9", "signedUrl": signedURL, "expiresAt": time.Now().Add(time.Hour).UnixMilli(),
			})
		default:
			if r.Method != "PUT" {
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				w.WriteHeader(400)
				return
			}
			b, _ := io.ReadAll(r.Body)
			if string(b) != "recording" {
				t.Errorf("upload data=%q", b)
			}
			puts++
			crc := oss.NewCRC64(0)
			crc.Write(b)
			w.Header().Set("x-oss-hash-crc64ecma", fmt.Sprint(crc.Sum64()))
			w.Header().Set("ETag", `"test"`)
			w.WriteHeader(200)
		}
	})
	file := filepath.Join(t.TempDir(), "call.m4a")
	if err := os.WriteFile(file, []byte("recording"), 0o600); err != nil {
		t.Fatal(err)
	}
	receipt, err := c.UploadRecordingWithTask(context.Background(), file, func(taskID, objectKey string) error {
		reported = taskID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TaskID != recordingTaskID || receipt.FileSize != 9 || receipt.SignedURL != signedURL {
		t.Fatalf("receipt=%+v", receipt)
	}
	if creates != 1 || puts != 1 || completes != 2 || reported != recordingTaskID {
		t.Fatalf("create=%d put=%d complete=%d reported=%q", creates, puts, completes, reported)
	}
}
