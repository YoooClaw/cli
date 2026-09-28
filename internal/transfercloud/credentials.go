package transfercloud

import (
	"context"
	"sync"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

const refreshWindow = 5 * time.Minute

// The SDK calls this provider before signing each part. Serialize refreshes so
// concurrent parts share one credential generation and never create a new task.
type uploadCredentials struct {
	mu         sync.Mutex
	cloud      *Client
	task       UploadTask
	force      bool
	refreshErr error
}

func (p *uploadCredentials) invalidate() { p.mu.Lock(); defer p.mu.Unlock(); p.force = true }
func (p *uploadCredentials) GetCredentials(ctx context.Context) (credentials.Credentials, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refreshErr != nil {
		return credentials.Credentials{}, p.refreshErr
	}
	expiry, _ := time.Parse(time.RFC3339, p.task.Credentials.Expiration)
	if p.force || !expiry.After(time.Now().Add(refreshWindow)) {
		refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		next, err := p.cloud.Refresh(refreshCtx, &p.task)
		cancel()
		if err != nil {
			p.refreshErr = err
			return credentials.Credentials{}, err
		}
		expiry, _ = time.Parse(time.RFC3339, next.Credentials.Expiration)
		// Reject unusably short credentials rather than refreshing every part.
		if !expiry.After(time.Now().Add(refreshWindow)) {
			p.refreshErr = failure("CLOUD_RESPONSE_INVALID", "迁移上传凭据有效期不足")
			return credentials.Credentials{}, p.refreshErr
		}
		p.task = *next
		p.force = false
	}
	c := p.task.Credentials
	return credentials.Credentials{AccessKeyID: c.AccessKeyID, AccessKeySecret: c.AccessKeySecret, SecurityToken: c.SecurityToken, Expires: &expiry}, nil
}
