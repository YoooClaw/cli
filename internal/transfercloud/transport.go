package transfercloud

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/ossdownload"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

const partSize int64 = 8 << 20

func retry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if ctx.Err() != nil {
			return failure("CANCELLED", "迁移已取消或超时")
		}
		if err = fn(); err == nil {
			return nil
		}
		if attempt < 3 {
			timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return failure("CANCELLED", "迁移已取消或超时")
			case <-timer.C:
			}
		}
	}
	return err
}

// uploadCompletion normalizes the two completion receipts (migration and
// recording) for the shared upload pipeline.
type uploadCompletion struct {
	taskID, objectKey, status string
	fileSize                  int64
	recording                 *RecordingReceipt
}

type uploadFilePlan struct {
	create              func(context.Context, string) (*UploadTask, error)
	refresh             func(context.Context, *UploadTask) (*UploadTask, error)
	complete            func(context.Context, string, string) (*uploadCompletion, error)
	contentType         string
	kind                string
	invalidFileMessage  string
	sizeMismatchMessage string
	completeHint        func(*UploadTask) string
	taskCreated         func(*UploadTask) error
}

// Upload uses SDK multipart checkpoints for retries within this invocation.
// The task is created once; confirmation retries never start another upload.
func (c *Client) Upload(ctx context.Context, path string) (*Receipt, error) {
	done, err := c.uploadFile(ctx, path, uploadFilePlan{
		create:  c.Create,
		refresh: c.Refresh,
		complete: func(ctx context.Context, id, key string) (*uploadCompletion, error) {
			receipt, err := c.Complete(ctx, id, key)
			if err != nil {
				return nil, err
			}
			return &uploadCompletion{taskID: receipt.TaskID, objectKey: receipt.ObjectKey, status: receipt.Status, fileSize: int64(receipt.FileSize)}, nil
		},
		contentType:         "application/gzip",
		kind:                "迁移",
		invalidFileMessage:  "迁移压缩包无效或超过 5 GiB",
		sizeMismatchMessage: "上传确认的文件大小不匹配",
		completeHint: func(task *UploadTask) string {
			return "上传已完成，但确认失败；重试：yoooclaw transfer complete --task " + task.TaskID + " --object-key " + task.ObjectKey
		},
	})
	if err != nil {
		return nil, err
	}
	return &Receipt{TaskID: done.taskID, ObjectKey: done.objectKey, Status: done.status, FileSize: Number(done.fileSize)}, nil
}

func (c *Client) uploadFile(ctx context.Context, path string, plan uploadFilePlan) (*uploadCompletion, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxBytes {
		return nil, failure("CLOUD_FILE_INVALID", plan.invalidFileMessage)
	}
	if err = c.CheckAuth(); err != nil {
		return nil, err
	}
	checkpoint, err := os.MkdirTemp("", "yoooclaw-upload-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(checkpoint)
	task, err := plan.create(ctx, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if plan.taskCreated != nil {
		if err := plan.taskCreated(task); err != nil {
			return nil, err
		}
	}
	provider := &uploadCredentials{cloud: c, task: *task, refresh: plan.refresh, kind: plan.kind}
	cfg := oss.LoadDefaultConfig().WithLogLevel(oss.LogOff).WithRegion(strings.TrimPrefix(task.Region, "oss-")).WithEndpoint(task.Endpoint).
		WithCredentialsProvider(provider).WithHttpClient(c.http)
	uploader := oss.NewUploader(oss.NewClient(cfg), func(o *oss.UploaderOptions) {
		o.PartSize = partSize
		o.ParallelNum = 3
		o.EnableCheckpoint = true
		o.CheckpointDir = checkpoint
	})
	var completion *uploadCompletion
	err = retry(ctx, func() error {
		if _, e := provider.GetCredentials(ctx); e != nil {
			return e
		}
		_, e := uploader.UploadFile(ctx, &oss.PutObjectRequest{Bucket: oss.Ptr(task.Bucket), Key: oss.Ptr(task.ObjectKey), ContentType: oss.Ptr(plan.contentType)}, path)
		if e != nil {
			// The OSS completion response may be lost after the object was committed.
			// Confirm the existing task before attempting the SDK upload again.
			if confirmed, confirmErr := plan.complete(ctx, task.TaskID, task.ObjectKey); confirmErr == nil {
				completion = confirmed
				return nil
			}
			var serviceErr *oss.ServiceError
			if errors.As(e, &serviceErr) && (serviceErr.Code == "SecurityTokenExpired" || serviceErr.Code == "InvalidSecurityToken") {
				provider.invalidate()
			}
			return failure("CLOUD_UPLOAD_FAILED", "OSS 上传未完成，任务 "+task.TaskID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if completion == nil {
		err = retry(ctx, func() error {
			var e error
			completion, e = plan.complete(ctx, task.TaskID, task.ObjectKey)
			return e
		})
	}
	if err != nil {
		return nil, failure("CLOUD_COMPLETE_FAILED", plan.completeHint(task))
	}
	if completion.fileSize != info.Size() {
		return nil, failure("CLOUD_SIZE_MISMATCH", plan.sizeMismatchMessage+"，任务 "+task.TaskID)
	}
	return completion, nil
}

func (c *Client) Download(ctx context.Context, id, path string) error {
	return c.download(ctx, id, path, nil)
}

func (c *Client) download(ctx context.Context, id, path string, progress func(int64, int64, int64)) error {
	if !ValidTaskID(id) {
		return failure("INVALID_TASK", "迁移 taskId 无效")
	}
	checkpoint := path + ".download"
	if err := os.MkdirAll(checkpoint, 0700); err != nil {
		return err
	}
	var err error
	// Work entirely inside a private directory, including the SDK's temporary file.
	downloaded := filepath.Join(checkpoint, "package.tar.gz")
	err = retry(ctx, func() error {
		link, e := c.downloadLink(ctx, id)
		if e != nil {
			return e
		}
		client := &renewingDownloadClient{cloud: c, taskID: id, link: link, now: time.Now}
		e = ossdownload.Download(ctx, client, link.SignedURL, id, downloaded, checkpoint, 0, false, progress)
		if e != nil {
			return failure("CLOUD_DOWNLOAD_FAILED", "迁移包下载失败，任务 "+id)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Checkpoint and destination share a filesystem. Commit without a second copy.
	if err := os.Link(downloaded, path); err != nil {
		return err
	}
	return os.RemoveAll(checkpoint)
}
