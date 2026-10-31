package transfercloud

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Each range request checks the link's expiry, as multipart uploads check STS.
// Refreshing does not restart the SDK downloader or consume an outer retry.
// This wrapper is used only for OSS GET requests, never file-service requests.
type renewingDownloadClient struct {
	mu         sync.Mutex
	cloud      *Client
	taskID     string
	link       downloadLink
	now        func() time.Time
	refreshErr error
}

func (c *renewingDownloadClient) currentURL(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.refreshErr != nil {
		return "", c.refreshErr
	}
	if int64(c.link.ExpiresAt) <= c.now().Add(refreshWindow).UnixMilli() {
		refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		next, err := c.cloud.downloadLink(refreshCtx, c.taskID)
		if err == nil && int64(next.ExpiresAt) <= c.now().Add(refreshWindow).UnixMilli() {
			err = failure("CLOUD_RESPONSE_INVALID", "迁移下载链接有效期不足")
		}
		if err != nil {
			// Concurrent parts share the error; the existing outer retry owns recovery.
			c.refreshErr = err
			return "", err
		}
		c.link = next
	}
	return c.link.SignedURL, nil
}
func (c *renewingDownloadClient) Do(req *http.Request) (*http.Response, error) {
	rawURL, err := c.currentURL(req.Context())
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, failure("CLOUD_RESPONSE_INVALID", "迁移下载链接无效")
	}
	clone := req.Clone(req.Context())
	clone.URL = u
	clone.Host = ""
	return c.cloud.http.Do(clone)
}
