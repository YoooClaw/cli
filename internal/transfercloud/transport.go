package transfercloud

import (
	"context"
	"errors"
	"io"
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
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return failure("CANCELLED", "迁移已取消或超时")
		}
		if err = fn(); err == nil {
			return nil
		}
		if attempt < 2 {
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

// Upload uses SDK multipart checkpoints for retries within this invocation.
// The task is created once; confirmation retries never start another upload.
func (c *Client) Upload(ctx context.Context, path string) (*Receipt, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxBytes {
		return nil, failure("CLOUD_FILE_INVALID", "迁移压缩包无效或超过 5 GiB")
	}
	if err = c.CheckAuth(); err != nil {
		return nil, err
	}
	checkpoint, err := os.MkdirTemp("", "yoooclaw-upload-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(checkpoint)
	task, err := c.Create(ctx, filepath.Base(path))
	if err != nil {
		return nil, err
	}
	provider := &uploadCredentials{cloud: c, task: *task}
	cfg := oss.LoadDefaultConfig().WithLogLevel(oss.LogOff).WithRegion(strings.TrimPrefix(task.Region, "oss-")).WithEndpoint(task.Endpoint).
		WithCredentialsProvider(provider).WithHttpClient(c.http)
	uploader := oss.NewUploader(oss.NewClient(cfg), func(o *oss.UploaderOptions) {
		o.PartSize = partSize
		o.ParallelNum = 3
		o.EnableCheckpoint = true
		o.CheckpointDir = checkpoint
	})
	var receipt *Receipt
	err = retry(ctx, func() error {
		if _, e := provider.GetCredentials(ctx); e != nil {
			return e
		}
		_, e := uploader.UploadFile(ctx, &oss.PutObjectRequest{Bucket: oss.Ptr(task.Bucket), Key: oss.Ptr(task.ObjectKey), ContentType: oss.Ptr("application/gzip")}, path)
		if e != nil {
			// The OSS completion response may be lost after the object was committed.
			// Confirm the existing task before attempting the SDK upload again.
			if confirmed, confirmErr := c.Complete(ctx, task.TaskID, task.ObjectKey); confirmErr == nil {
				receipt = confirmed
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
	if receipt == nil {
		err = retry(ctx, func() error { var e error; receipt, e = c.Complete(ctx, task.TaskID, task.ObjectKey); return e })
	}
	if err != nil {
		return nil, failure("CLOUD_COMPLETE_FAILED", "上传已完成，但确认失败；重试：yoooclaw transfer complete --task "+task.TaskID+" --object-key "+task.ObjectKey)
	}
	if int64(receipt.FileSize) != info.Size() {
		return nil, failure("CLOUD_SIZE_MISMATCH", "上传确认的文件大小不匹配，任务 "+task.TaskID)
	}
	return receipt, nil
}

func (c *Client) Download(ctx context.Context, id, path string) error {
	if !ValidTaskID(id) {
		return failure("INVALID_TASK", "迁移 taskId 无效")
	}
	checkpoint, err := os.MkdirTemp("", "yoooclaw-download-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(checkpoint)
	// Work entirely inside a private directory, including the SDK's temporary file.
	downloaded := filepath.Join(checkpoint, "package.tar.gz")
	err = retry(ctx, func() error {
		rawURL, e := c.downloadURL(ctx, id)
		if e != nil {
			return e
		}
		e = ossdownload.Download(ctx, c.http, rawURL, id, downloaded, checkpoint, MaxBytes, false)
		if e != nil {
			return failure("CLOUD_DOWNLOAD_FAILED", "迁移包下载失败，任务 "+id)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Destinations can be on a different filesystem from the checkpoint directory.
	source, err := os.Open(downloaded)
	if err != nil {
		return err
	}
	defer source.Close()
	dest, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dest, source)
	closeErr := dest.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(path)
		return failure("CLOUD_DOWNLOAD_FAILED", "保存迁移压缩包失败")
	}
	return nil
}
