package transfercloud

import (
	"context"
	"net/http"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// recordingMIMEs are the audio extensions the CLI can upload as RECORDING.
// The file service still owns the authoritative extension list; an unknown
// extension fails before a task is created.
var recordingMIMEs = map[string]string{
	".m4a":  "audio/mp4",
	".mp4":  "audio/mp4",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".aac":  "audio/aac",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".opus": "audio/opus",
	".amr":  "audio/amr",
	".flac": "audio/flac",
	".3gp":  "audio/3gpp",
}

var recordingDateRE = regexp.MustCompile(`^[0-9]{8}$`)

// RecordingReceipt is C-004's completed recording object plus the initial
// signed download link. The signed URL is kept in memory and is not part of
// the persisted export record.
type RecordingReceipt struct {
	TaskID    string
	ObjectKey string
	Status    string
	FileSize  int64
	SignedURL string
	ExpiresAt time.Time
}

func recordingContentType(name string) (string, bool) {
	base := filepath.Base(name)
	if base != name || base == "." || base == "" || len(base) > 255 || strings.ContainsAny(base, "/\\\r\n\x00") {
		return "", false
	}
	ext := strings.ToLower(filepath.Ext(base))
	if strings.TrimSuffix(base, ext) == "" {
		return "", false
	}
	mime, ok := recordingMIMEs[ext]
	return mime, ok
}

// validRecordingTaskID deliberately does not reuse migration's 32-hex syntax:
// recording task IDs are opaque to this client.
func validRecordingTaskID(id string) bool {
	return id != "" && len(id) <= 256 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "/\\\r\n\x00")
}

func validRecordingObjectKey(key string) bool {
	if key == "" || len(key) > 512 || strings.ContainsAny(key, "\\\r\n\x00") {
		return false
	}
	if pathpkg.IsAbs(key) || pathpkg.Clean(key) != key {
		return false
	}
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "users" || parts[1] == "" || !recordingDateRE.MatchString(parts[2]) {
		return false
	}
	if _, ok := recordingContentType(parts[3]); !ok {
		return false
	}
	ext := filepath.Ext(parts[3])
	return len(strings.TrimSuffix(parts[3], ext)) >= 16
}

func validateRecordingUploadTask(task *UploadTask, expectedName string) error {
	if task == nil || task.FileType != "RECORDING" || !validRecordingTaskID(task.TaskID) || !validRecordingObjectKey(task.ObjectKey) ||
		(expectedName != "" && task.FileName != expectedName) {
		return failure("CLOUD_RESPONSE_INVALID", "录音上传任务信息无效")
	}
	u, err := validateOSSURL(task.Endpoint)
	exp, expErr := time.Parse(time.RFC3339, task.Credentials.Expiration)
	if err != nil || u.RawQuery != "" || (u.Path != "" && u.Path != "/") || !bucketRE.MatchString(task.Bucket) ||
		!regionRE.MatchString(task.Region) || !endpointRE.MatchString(task.Endpoint) ||
		task.Credentials.AccessKeyID == "" || task.Credentials.AccessKeySecret == "" || task.Credentials.SecurityToken == "" ||
		expErr != nil || !exp.After(time.Now()) {
		return failure("CLOUD_RESPONSE_INVALID", "录音上传凭据无效或已过期")
	}
	return nil
}

// CreateRecording requests a RECORDING task. Like migration Create, this must
// never be retried automatically because it consumes quota.
func (c *Client) CreateRecording(ctx context.Context, name string) (*UploadTask, error) {
	if _, ok := recordingContentType(name); !ok {
		return nil, failure("CLOUD_FILE_INVALID", "录音文件名或格式不受支持")
	}
	var task UploadTask
	if err := c.call(ctx, http.MethodPost, "/create", map[string]string{"fileType": "RECORDING", "fileName": name}, &task); err != nil {
		return nil, err
	}
	if err := validateRecordingUploadTask(&task, name); err != nil {
		return nil, err
	}
	return &task, nil
}

// RefreshRecording renews the STS credentials for the same recording task.
func (c *Client) RefreshRecording(ctx context.Context, original *UploadTask) (*UploadTask, error) {
	if original == nil || !validRecordingTaskID(original.TaskID) {
		return nil, failure("INVALID_TASK", "录音 taskId 无效")
	}
	var next UploadTask
	if err := c.call(ctx, http.MethodPost, "/refresh", map[string]string{"taskId": original.TaskID}, &next); err != nil {
		return nil, err
	}
	if err := validateRecordingUploadTask(&next, original.FileName); err != nil {
		return nil, err
	}
	if next.TaskID != original.TaskID || next.ObjectKey != original.ObjectKey || next.Bucket != original.Bucket ||
		next.Endpoint != original.Endpoint || next.Region != original.Region || next.FileType != original.FileType || next.FileName != original.FileName {
		return nil, failure("CLOUD_RESPONSE_INVALID", "录音上传任务信息不匹配")
	}
	return &next, nil
}

// CompleteRecording confirms the object and returns C-004's signed link.
// Repeated calls reuse the same task and never re-upload.
func (c *Client) CompleteRecording(ctx context.Context, id, key string) (*RecordingReceipt, error) {
	if !validRecordingTaskID(id) || !validRecordingObjectKey(key) {
		return nil, failure("INVALID_TASK", "需要有效的录音 taskId 与 objectKey")
	}
	var result struct {
		TaskID    string `json:"taskId"`
		ObjectKey string `json:"objectKey"`
		Status    string `json:"status"`
		FileSize  Number `json:"fileSize"`
		SignedURL string `json:"signedUrl"`
		ExpiresAt Number `json:"expiresAt"`
	}
	if err := c.call(ctx, http.MethodPost, "/complete", map[string]string{"taskId": id, "objectKey": key}, &result); err != nil {
		return nil, err
	}
	if result.TaskID != id || result.ObjectKey != key || result.Status != "COMPLETED" || result.FileSize <= 0 {
		return nil, failure("CLOUD_RESPONSE_INVALID", "录音上传确认数据无效")
	}
	u, err := validateOSSURL(result.SignedURL)
	if err != nil || u.Port() != "" || !strings.Contains(u.Hostname(), ".oss-") {
		return nil, failure("CLOUD_URL_INVALID", "录音下载地址无效")
	}
	expiresAt := time.UnixMilli(int64(result.ExpiresAt))
	if !expiresAt.After(time.Now()) {
		return nil, failure("CLOUD_RESPONSE_INVALID", "录音下载链接已过期")
	}
	return &RecordingReceipt{TaskID: result.TaskID, ObjectKey: result.ObjectKey, Status: result.Status, FileSize: int64(result.FileSize), SignedURL: result.SignedURL, ExpiresAt: expiresAt}, nil
}

// UploadRecording uploads an audio file as a RECORDING task and returns the
// completed object plus its initial signed download link.
func (c *Client) UploadRecording(ctx context.Context, path string) (*RecordingReceipt, error) {
	return c.UploadRecordingWithTask(ctx, path, nil)
}

// UploadRecordingWithTask additionally reports the created task before bytes
// are uploaded, so callers can persist taskId/objectKey for crash recovery.
func (c *Client) UploadRecordingWithTask(ctx context.Context, path string, onTask func(taskID, objectKey string) error) (*RecordingReceipt, error) {
	contentType, ok := recordingContentType(filepath.Base(path))
	if !ok {
		return nil, failure("CLOUD_FILE_INVALID", "录音文件格式不受支持")
	}
	done, err := c.uploadFile(ctx, path, uploadFilePlan{
		create:  c.CreateRecording,
		refresh: c.RefreshRecording,
		complete: func(ctx context.Context, id, key string) (*uploadCompletion, error) {
			receipt, err := c.CompleteRecording(ctx, id, key)
			if err != nil {
				return nil, err
			}
			return &uploadCompletion{taskID: receipt.TaskID, objectKey: receipt.ObjectKey, status: receipt.Status, fileSize: receipt.FileSize, recording: receipt}, nil
		},
		contentType:         contentType,
		kind:                "录音",
		invalidFileMessage:  "录音文件必须是非空普通文件",
		sizeMismatchMessage: "录音上传确认的文件大小不匹配",
		completeHint: func(task *UploadTask) string {
			return "录音上传已完成，但确认失败；任务 " + task.TaskID
		},
		taskCreated: func(task *UploadTask) error {
			if onTask != nil {
				return onTask(task.TaskID, task.ObjectKey)
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	if done.recording == nil {
		return nil, failure("CLOUD_RESPONSE_INVALID", "录音上传确认数据无效")
	}
	return done.recording, nil
}
