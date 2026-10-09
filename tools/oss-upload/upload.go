package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

const uploadPartSize = 1024 * 1024

func (u *uploader) upload(key string, body []byte, file, contentType, label string) error {
	ctx, cancel := context.WithTimeout(context.Background(), putTimeout)
	defer cancel()
	req := &oss.PutObjectRequest{Bucket: oss.Ptr(u.bucket), Key: oss.Ptr(key), ContentType: oss.Ptr(contentType), CacheControl: oss.Ptr(u.cacheControl)}
	if u.acl != "" {
		req.Acl = oss.ObjectACLType(u.acl)
	}
	return uploadWithProgress(ctx, u.client, req, body, file, label, logf)
}

// Use the SDK's file checkpoint and multipart scheduler; never rebuild parts ourselves.
func uploadWithProgress(ctx context.Context, client oss.UploadAPIClient, req *oss.PutObjectRequest, body []byte, file, label string, log func(string, ...any)) error {
	size := int64(len(body))
	if file != "" {
		info, err := os.Stat(file)
		if err != nil {
			return err
		}
		size = info.Size()
	}
	checkpoint, err := os.MkdirTemp("", "yoooclaw-release-upload-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(checkpoint)
	sdk := oss.NewUploader(client, func(o *oss.UploaderOptions) {
		o.PartSize = uploadPartSize
		o.ParallelNum = 3
		o.EnableCheckpoint = true
		o.CheckpointDir = checkpoint
		o.LeavePartsOnError = true
	})
	started := time.Now()
	var transferred, attempt atomic.Int64
	report := func() {
		seconds := time.Since(started).Seconds()
		if seconds < 0.001 {
			seconds = 0.001
		}
		n := transferred.Load()
		percent := float64(0)
		if size > 0 {
			percent = float64(n) * 100 / float64(size)
		}
		log("%s: %.0f%% 已传输 %.2f/%.2f MiB，平均 %.1f KiB/s，耗时 %.1fs，第 %d/%d 次", label, percent, float64(n)/(1024*1024), float64(size)/(1024*1024), float64(n)/1024/seconds, seconds, attempt.Load(), maxAttempts)
	}
	req.ProgressFn = func(_ int64, done int64, _ int64) { transferred.Store(done); report() }
	mode := "普通 PUT"
	if size >= uploadPartSize {
		mode = "SDK 分片（1 MiB/片，并发 3）"
	}
	log("开始上传 %s：%.2f MiB，%s", label, float64(size)/(1024*1024), mode)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				report()
			}
		}
	}()
	defer func() { close(stop); <-stopped }()
	for n := 1; n <= maxAttempts; n++ {
		attempt.Store(int64(n))
		transferred.Store(0)
		report()
		if file != "" {
			_, err = sdk.UploadFile(ctx, req, file)
		} else {
			_, err = sdk.UploadFrom(ctx, req, bytes.NewReader(body))
		}
		if err == nil {
			transferred.Store(size)
			report()
			log("%s: 上传完成（已收到 OSS 完成响应）", label)
			return nil
		}
		reason := "网络或 SDK 错误"
		retry := true
		var service *oss.ServiceError
		if errors.As(err, &service) {
			reason = fmt.Sprintf("HTTP %d", service.StatusCode)
			retry = service.StatusCode < 400 || service.StatusCode >= 500 || service.StatusCode == 408 || service.StatusCode == 429
		}
		log("%s: 第 %d/%d 次失败（%s），耗时 %.1fs", label, n, maxAttempts, reason, time.Since(started).Seconds())
		if ctx.Err() != nil {
			return fmt.Errorf("%s: 上传停止: %w", label, ctx.Err())
		}
		if !retry || n == maxAttempts {
			return fmt.Errorf("%s: 上传失败（%s）", label, reason)
		}
		log("%s: %ds 后重试，文件上传复用 SDK checkpoint", label, 1<<(n-1))
		timer := time.NewTimer(time.Duration(1<<(n-1)) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}
