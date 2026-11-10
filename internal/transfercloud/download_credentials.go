package transfercloud

import (
	"context"
	"net/http"
	"time"

	"github.com/YoooClaw/cli/internal/ossdownload"
)

// renewingDownloadClient keeps the signed URL current for one migration
// download. The shared refresher collapses concurrent range refreshes into one
// call and caches a failed refresh for the current outer attempt.
type renewingDownloadClient struct {
	refresher *ossdownload.LinkRefresher
	next      ossdownload.HTTPClient
}

func newRenewingDownloadClient(cloud *Client, taskID string, link downloadLink, now func() time.Time) *renewingDownloadClient {
	return &renewingDownloadClient{
		refresher: ossdownload.NewLinkRefresher(
			ossdownload.Link{URL: link.SignedURL, ExpiresAt: time.UnixMilli(int64(link.ExpiresAt))},
			func(ctx context.Context) (ossdownload.Link, error) {
				next, err := cloud.downloadLink(ctx, taskID)
				if err != nil {
					return ossdownload.Link{}, err
				}
				return ossdownload.Link{URL: next.SignedURL, ExpiresAt: time.UnixMilli(int64(next.ExpiresAt))}, nil
			},
			ossdownload.LinkRefresherOptions{Window: refreshWindow, Timeout: 30 * time.Second, Now: now},
		),
		next: cloud.http,
	}
}

func (c *renewingDownloadClient) currentURL(ctx context.Context) (string, error) {
	link, err := c.refresher.Current(ctx)
	return link.URL, err
}

func (c *renewingDownloadClient) Do(req *http.Request) (*http.Response, error) {
	return (&ossdownload.RenewingClient{Refresher: c.refresher, Next: c.next}).Do(req)
}
