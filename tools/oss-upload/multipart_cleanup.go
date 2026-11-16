package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

// Keep SDK checkpoints during retries, then abort only this invocation's unfinished uploads.
type trackedUploadClient struct {
	oss.UploadAPIClient
	mu      sync.Mutex
	pending map[string]bool
}

func (c *trackedUploadClient) InitiateMultipartUpload(ctx context.Context, req *oss.InitiateMultipartUploadRequest, opts ...func(*oss.Options)) (*oss.InitiateMultipartUploadResult, error) {
	result, err := c.UploadAPIClient.InitiateMultipartUpload(ctx, req, opts...)
	if err == nil {
		c.mu.Lock()
		c.pending[oss.ToString(result.UploadId)] = true
		c.mu.Unlock()
	}
	return result, err
}

func (c *trackedUploadClient) CompleteMultipartUpload(ctx context.Context, req *oss.CompleteMultipartUploadRequest, opts ...func(*oss.Options)) (*oss.CompleteMultipartUploadResult, error) {
	result, err := c.UploadAPIClient.CompleteMultipartUpload(ctx, req, opts...)
	if err == nil {
		c.mu.Lock()
		delete(c.pending, oss.ToString(req.UploadId))
		c.mu.Unlock()
	}
	return result, err
}

func (c *trackedUploadClient) cleanup(req *oss.PutObjectRequest, label string, log func(string, ...any)) error {
	c.mu.Lock()
	ids := make([]string, 0, len(c.pending))
	for id := range c.pending {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	// The upload context may already be cancelled. Bound all cleanup requests together.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var failures []error
	for _, id := range ids {
		_, err := c.UploadAPIClient.AbortMultipartUpload(ctx, &oss.AbortMultipartUploadRequest{
			Bucket: req.Bucket, Key: req.Key, UploadId: oss.Ptr(id),
		})
		var service *oss.ServiceError
		if err == nil || (errors.As(err, &service) && service.Code == "NoSuchUpload") {
			log("%s: 未完成分片已清理", label)
			continue
		}
		reason := "网络或 SDK 错误"
		if errors.As(err, &service) {
			reason = fmt.Sprintf("HTTP %d", service.StatusCode)
		}
		// Never include raw SDK errors or signed request details.
		failure := fmt.Errorf("%s: 分片清理失败（%s），需要桶生命周期规则兜底", label, reason)
		log("%s", failure)
		failures = append(failures, failure)
	}
	return errors.Join(failures...)
}
