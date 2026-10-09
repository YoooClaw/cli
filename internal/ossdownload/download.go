// Package ossdownload adapts GET-only URLs to the OSS SDK range downloader.
package ossdownload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

var ErrNoRange = errors.New("source does not support validated range downloads")

type HTTPError struct{ Code int }

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d", e.Code) }
func NewAdapter(client HTTPClient, rawURL string, maxBytes int64, allowFallback bool) *Adapter {
	return &Adapter{client: client, url: rawURL, maxBytes: maxBytes, allowFallback: allowFallback, attempts: make(map[string]int)}
}

// Download reuses the caller's private checkpoint directory across attempts.
// Key and destination remain stable even when a signed URL changes.
func Download(ctx context.Context, client HTTPClient, rawURL, key, dest, checkpoint string, maxBytes int64, allowFallback bool) error {
	downloader := oss.NewDownloader(NewAdapter(client, rawURL, maxBytes, allowFallback), func(o *oss.DownloaderOptions) {
		o.PartSize = 8 << 20
		o.ParallelNum = 3
		o.EnableCheckpoint = true
		o.CheckpointDir = checkpoint
	})
	_, err := downloader.DownloadFile(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr("download"), Key: oss.Ptr(key)}, dest)
	return err
}

// Adapter adapts a GET-only presigned URL to the SDK downloader. The SDK
// still owns range recovery, concurrent parts, checkpoints and final file rename.
// HEAD is not usable with a GET signature, so metadata comes from GET bytes=0-0.
type Adapter struct {
	mu            sync.Mutex
	attempts      map[string]int
	client        HTTPClient
	url           string
	size          int64
	etag          string
	maxBytes      int64
	allowFallback bool
}

func (d *Adapter) get(ctx context.Context, rng string) (*http.Response, error) {
	// The SDK retries interrupted bodies itself. Bound attempts at an unchanged
	// offset so a source repeatedly closing without data cannot spin indefinitely.
	if d.allowFallback {
		d.mu.Lock()
		d.attempts[rng]++
		count := d.attempts[rng]
		d.mu.Unlock()
		if count > 3 {
			return nil, errors.New("range download made no progress")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return nil, errors.New("invalid OSS download response")
	}
	req.Header.Set("Range", rng)
	req.Header.Set("Accept-Encoding", "identity")
	if d.etag != "" {
		req.Header.Set("If-Match", d.etag)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OSS transport: %w", err)
	}
	if resp.StatusCode == 200 && d.allowFallback {
		resp.Body.Close()
		return nil, ErrNoRange
	}
	if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Encoding") != "" {
		resp.Body.Close()
		return nil, &HTTPError{Code: resp.StatusCode}
	}
	return resp, nil
}
func parseContentRange(s string, maxBytes int64) (start, end, total int64, err error) {
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
	if e1 != nil || e2 != nil || e3 != nil || start < 0 || end < start || total <= end || total > maxBytes {
		return 0, 0, 0, fmt.Errorf("invalid range")
	}
	return start, end, total, nil
}
func (d *Adapter) HeadObject(ctx context.Context, _ *oss.HeadObjectRequest, _ ...func(*oss.Options)) (*oss.HeadObjectResult, error) {
	resp, err := d.get(ctx, "bytes=0-0")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	start, end, total, err := parseContentRange(resp.Header.Get("Content-Range"), d.maxBytes)
	if err != nil || start != 0 || end != 0 || resp.ContentLength != 1 {
		return nil, errors.New("invalid OSS download response")
	}
	if n, e := io.Copy(io.Discard, io.LimitReader(resp.Body, 2)); e != nil || n != 1 {
		return nil, errors.New("invalid OSS download response")
	}
	if resp.Header.Get("ETag") == "" || strings.HasPrefix(resp.Header.Get("ETag"), "W/") {
		if d.allowFallback {
			return nil, ErrNoRange
		}
		return nil, errors.New("OSS ETag missing")
	}
	d.size = total
	d.etag = resp.Header.Get("ETag")
	headers := resp.Header.Clone()
	headers.Set("Content-Length", strconv.FormatInt(total, 10))
	return &oss.HeadObjectResult{ContentLength: total, ETag: oss.Ptr(d.etag), ResultCommon: oss.ResultCommon{Headers: headers, StatusCode: 200}}, nil
}
func (d *Adapter) GetObject(ctx context.Context, req *oss.GetObjectRequest, _ ...func(*oss.Options)) (*oss.GetObjectResult, error) {
	if req.Range == nil {
		return nil, errors.New("invalid OSS download response")
	}
	resp, err := d.get(ctx, *req.Range)
	if err != nil {
		return nil, err
	}
	start, end, total, err := parseContentRange(resp.Header.Get("Content-Range"), d.maxBytes)
	expected := fmt.Sprintf("bytes=%d-%d", start, end)
	if err != nil || total != d.size || expected != *req.Range || resp.ContentLength != end-start+1 || resp.Header.Get("ETag") != d.etag {
		resp.Body.Close()
		return nil, errors.New("invalid OSS download response")
	}
	return &oss.GetObjectResult{ContentLength: resp.ContentLength, ContentRange: oss.Ptr(resp.Header.Get("Content-Range")), ETag: oss.Ptr(d.etag), Body: resp.Body, ResultCommon: oss.ResultCommon{Headers: resp.Header, StatusCode: 206}}, nil
}
