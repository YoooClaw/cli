package transfercloud

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
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
	cfg := oss.LoadDefaultConfig().WithLogLevel(oss.LogOff).WithRegion(strings.TrimPrefix(task.Region, "oss-")).WithEndpoint(task.Endpoint).
		WithCredentialsProvider(credentials.NewStaticCredentialsProvider(task.Credentials.AccessKeyID, task.Credentials.AccessKeySecret, task.Credentials.SecurityToken)).WithHttpClient(c.http)
	uploader := oss.NewUploader(oss.NewClient(cfg), func(o *oss.UploaderOptions) {
		o.PartSize = partSize
		o.ParallelNum = 3
		o.EnableCheckpoint = true
		o.CheckpointDir = checkpoint
	})
	var receipt *Receipt
	err = retry(ctx, func() error {
		if expiry, _ := time.Parse(time.RFC3339, task.Credentials.Expiration); !expiry.After(time.Now()) {
			return failure("STS_EXPIRED", "迁移上传凭据已过期，任务 "+task.TaskID)
		}
		_, e := uploader.UploadFile(ctx, &oss.PutObjectRequest{Bucket: oss.Ptr(task.Bucket), Key: oss.Ptr(task.ObjectKey), ContentType: oss.Ptr("application/gzip")}, path)
		if e != nil {
			// The OSS completion response may be lost after the object was committed.
			// Confirm the existing task before attempting the SDK upload again.
			if confirmed, confirmErr := c.Complete(ctx, task.TaskID, task.ObjectKey); confirmErr == nil {
				receipt = confirmed
				return nil
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

// signedDownload adapts a GET-only presigned URL to the SDK downloader. The SDK
// still owns range recovery, concurrent parts, checkpoints and final file rename.
// HEAD is not usable with a GET signature, so metadata comes from GET bytes=0-0.
type signedDownload struct {
	client *http.Client
	url    string
	size   int64
	etag   string
}

func (d *signedDownload) get(ctx context.Context, rng string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return nil, failure("CLOUD_DOWNLOAD_FAILED", "无法创建迁移下载请求")
	}
	req.Header.Set("Range", rng)
	req.Header.Set("Accept-Encoding", "identity")
	if d.etag != "" {
		req.Header.Set("If-Match", d.etag)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, failure("CLOUD_DOWNLOAD_FAILED", "迁移下载网络中断")
	}
	if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Encoding") != "" {
		resp.Body.Close()
		return nil, failure("CLOUD_DOWNLOAD_FAILED", fmt.Sprintf("OSS 范围下载失败（HTTP %d）", resp.StatusCode))
	}
	return resp, nil
}
func parseContentRange(s string) (start, end, total int64, err error) {
	// Strict parsing prevents accepting trailing data or integer overflows.
	parts := strings.Split(strings.TrimPrefix(s, "bytes "), "/")
	if !strings.HasPrefix(s, "bytes ") || len(parts) != 2 {
		return 0, 0, 0, fmt.Errorf("invalid range")
	}
	span := strings.Split(parts[0], "-")
	if len(span) != 2 {
		return 0, 0, 0, fmt.Errorf("invalid range")
	}
	start, e1 := strconv.ParseInt(span[0], 10, 64)
	end, e2 := strconv.ParseInt(span[1], 10, 64)
	total, e3 := strconv.ParseInt(parts[1], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || start < 0 || end < start || total <= end || total > MaxBytes {
		return 0, 0, 0, fmt.Errorf("invalid range")
	}
	return start, end, total, nil
}
func (d *signedDownload) HeadObject(ctx context.Context, _ *oss.HeadObjectRequest, _ ...func(*oss.Options)) (*oss.HeadObjectResult, error) {
	resp, err := d.get(ctx, "bytes=0-0")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	start, end, total, err := parseContentRange(resp.Header.Get("Content-Range"))
	if err != nil || start != 0 || end != 0 || resp.ContentLength != 1 || resp.Header.Get("ETag") == "" {
		return nil, failure("CLOUD_DOWNLOAD_INVALID", "OSS 文件元数据无效或超过 5 GiB")
	}
	if _, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 2)); err != nil {
		return nil, failure("CLOUD_DOWNLOAD_FAILED", "读取 OSS 文件元数据失败")
	}
	d.size = total
	d.etag = resp.Header.Get("ETag")
	headers := resp.Header.Clone()
	headers.Set("Content-Length", strconv.FormatInt(total, 10))
	return &oss.HeadObjectResult{ContentLength: total, ETag: oss.Ptr(d.etag), ResultCommon: oss.ResultCommon{Headers: headers, StatusCode: 200}}, nil
}
func (d *signedDownload) GetObject(ctx context.Context, req *oss.GetObjectRequest, _ ...func(*oss.Options)) (*oss.GetObjectResult, error) {
	if req.Range == nil {
		return nil, failure("CLOUD_DOWNLOAD_INVALID", "下载缺少范围")
	}
	resp, err := d.get(ctx, *req.Range)
	if err != nil {
		return nil, err
	}
	start, end, total, err := parseContentRange(resp.Header.Get("Content-Range"))
	expected := fmt.Sprintf("bytes=%d-%d", start, end)
	if err != nil || total != d.size || expected != *req.Range || resp.ContentLength != end-start+1 || resp.Header.Get("ETag") != d.etag {
		resp.Body.Close()
		return nil, failure("CLOUD_DOWNLOAD_INVALID", "OSS 下载范围或文件版本不匹配")
	}
	return &oss.GetObjectResult{ContentLength: resp.ContentLength, ContentRange: oss.Ptr(resp.Header.Get("Content-Range")), ETag: oss.Ptr(d.etag), Body: resp.Body, ResultCommon: oss.ResultCommon{Headers: resp.Header, StatusCode: 206}}, nil
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
		adapter := &signedDownload{client: c.http, url: rawURL}
		downloader := oss.NewDownloader(adapter, func(o *oss.DownloaderOptions) {
			o.PartSize = partSize
			o.ParallelNum = 3
			o.EnableCheckpoint = true
			o.CheckpointDir = checkpoint
		})
		_, e = downloader.DownloadFile(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr("migration"), Key: oss.Ptr(id)}, downloaded)
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
