package recording

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/ossdownload"
)

const (
	defaultDownloadTimeout = 30 * time.Minute
	defaultDownloadRetries = 8
	defaultRetryBackoff    = 2 * time.Second
)

// DownloadOptions 控制 OSS 下载重试与超时。
type DownloadOptions struct {
	OverallTimeout time.Duration
	HeaderTimeout  time.Duration
	IdleTimeout    time.Duration
	RefreshURL     func(context.Context) (string, error)

	Timeout      time.Duration
	MaxRetries   int
	RetryBackoff time.Duration
	Client       *http.Client
}

// DownloadResult 是单个文件下载结果。
type DownloadResult struct {
	OK        bool          `json:"ok"`
	SizeBytes int64         `json:"sizeBytes,omitempty"`
	Elapsed   time.Duration `json:"-"`
	Error     string        `json:"error,omitempty"`
}

type downloadHTTPStatusError struct {
	Code   int
	Status string
}

func (e *downloadHTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d %s", e.Code, e.Status)
}

// DownloadFile 从 URL 下载文件到 destPath。
func DownloadFile(rawURL, destPath string, logger Logger, opts DownloadOptions) DownloadResult {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultDownloadTimeout
	}
	retries := opts.MaxRetries
	if retries <= 0 {
		retries = defaultDownloadRetries
	}
	backoff := opts.RetryBackoff
	if backoff <= 0 {
		backoff = defaultRetryBackoff
	}
	client := opts.Client
	if client == nil {
		client = http.DefaultClient
	}

	start := time.Now()
	overall := opts.OverallTimeout
	if overall <= 0 {
		overall = time.Hour
	}
	parent, stop := context.WithTimeout(context.Background(), overall)
	defer stop()
	if err := fsutil.EnsureDir(filepath.Dir(destPath), fsutil.DirMode); err != nil {
		return DownloadResult{Error: err.Error()}
	}
	checkpoint, err := os.MkdirTemp(filepath.Dir(destPath), ".audio-checkpoint-")
	if err != nil {
		return DownloadResult{Error: err.Error()}
	}
	defer os.RemoveAll(checkpoint)
	staged := filepath.Join(checkpoint, "audio")
	var lastErr error
	refreshed := false
	for attempt := 1; attempt <= retries; attempt++ {
		logger.Info(fmt.Sprintf("[downloader] 开始下载 %s (attempt %d/%d)", filepath.Base(destPath), attempt, retries))
		ctx, cancel := context.WithTimeout(parent, timeout)
		transport := &downloadClient{client: client, header: opts.HeaderTimeout, idle: opts.IdleTimeout, strict: refreshed}
		err = ossdownload.Download(ctx, transport, rawURL, "audio", staged, checkpoint, maxAudioBytes, true)
		if errors.Is(err, ossdownload.ErrNoRange) {
			logger.Info("[downloader] 源站不支持可靠分段下载，转为单流下载")
			var req *http.Request
			req, err = http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
			if err == nil {
				req.Header.Set("Accept-Encoding", "identity")
				var resp *http.Response
				resp, err = transport.Do(req)
				if err == nil {
					err = writeDownloadResponse(resp, staged)
					resp.Body.Close()
				}
			}
		}
		cancel()
		if err == nil {
			err = os.Chmod(staged, 0600)
			if err == nil {
				err = os.Rename(staged, destPath)
			}
		}
		if err == nil {
			info, e := os.Stat(destPath)
			if e != nil {
				return DownloadResult{Error: e.Error()}
			}
			logger.Info(fmt.Sprintf("[downloader] 下载完成 %s (%d bytes, %s)", filepath.Base(destPath), info.Size(), time.Since(start)))
			return DownloadResult{OK: true, SizeBytes: info.Size(), Elapsed: time.Since(start)}
		}
		lastErr = err
		logger.Warn(fmt.Sprintf("[downloader] 下载失败 (attempt %d/%d): %s", attempt, retries, safeDownloadError(err)))
		if downloadStatus(err) == 403 && !refreshed && opts.RefreshURL != nil && parent.Err() == nil {
			refreshed = true
			logger.Info("[downloader] HTTP 403，开始刷新签名链接（本轮最多一次）")
			refreshCtx, done := context.WithTimeout(parent, 30*time.Second)
			next, e := opts.RefreshURL(refreshCtx)
			done()
			if e != nil {
				lastErr = e
				logger.Warn("[downloader] 签名链接刷新失败: " + safeDownloadError(e))
				break
			}
			rawURL = next
			logger.Info("[downloader] 签名链接已刷新，继续下载")
			// The refresh retry is available even when the attempt budget was exhausted.
			if attempt == retries {
				retries++
			}
			continue
		}
		if !isRetryableDownloadError(err) || parent.Err() != nil {
			break
		}
		if attempt < retries {
			delay := backoff
			for i := 1; i < attempt && delay < 30*time.Second; i++ {
				delay *= 2
			}
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-parent.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
	return DownloadResult{Error: safeDownloadError(lastErr), Elapsed: time.Since(start)}
}

const maxAudioBytes int64 = 5 << 30

func downloadStatus(err error) int {
	var a *ossdownload.HTTPError
	if errors.As(err, &a) {
		return a.Code
	}
	var b *downloadHTTPStatusError
	if errors.As(err, &b) {
		return b.Code
	}
	return 0
}
func safeDownloadError(err error) string {
	if err == nil {
		return "download failed"
	}
	if code := downloadStatus(err); code != 0 {
		return fmt.Sprintf("HTTP %d", code)
	}
	var u *url.Error
	if errors.As(err, &u) {
		return "download transport failed"
	}
	return err.Error()
}

// Each range has its own header and inactivity deadline.
type downloadClient struct {
	client       *http.Client
	header, idle time.Duration
	strict       bool
}

func (d *downloadClient) Do(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	req = req.Clone(ctx)
	header := d.header
	if header <= 0 {
		header = 20 * time.Second
	}
	idle := d.idle
	if idle <= 0 {
		idle = 30 * time.Second
	}
	deadline := &headerDeadline{cancel: cancel}
	timer := time.AfterFunc(header, deadline.expire)
	client := *d.client
	if d.strict {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	resp, err := client.Do(req)
	var direct *http.Client
	if err != nil && shouldFallbackDirect(&client, req, err) {
		direct = directHTTPClient(&client)
		resp, err = direct.Do(req)
	}
	timedOut := deadline.finish()
	timer.Stop()
	if timedOut {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		err = context.DeadlineExceeded
	}
	if err != nil {
		cancel()
		if direct != nil {
			direct.CloseIdleConnections()
		}
		return nil, err
	}
	body := &idleBody{ReadCloser: resp.Body, cancel: cancel, idle: idle, direct: direct}
	body.timer = time.AfterFunc(idle, cancel)
	resp.Body = body
	return resp, nil
}

// Serialize completion with the timer callback. Stop alone cannot prevent an
// already queued callback from cancelling a body after Do has returned it.
// If expiry wins, reject the response here instead of handing out a cancelled body.
type headerDeadline struct {
	mu             sync.Mutex
	done, timedOut bool
	cancel         context.CancelFunc
}

func (d *headerDeadline) expire() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.done {
		d.done = true
		d.timedOut = true
		d.cancel()
	}
}
func (d *headerDeadline) finish() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.done = true
	return d.timedOut
}

type idleBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	idle   time.Duration
	timer  *time.Timer
	mu     sync.Mutex
	closed bool
	direct *http.Client
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	b.mu.Lock()
	if n > 0 && !b.closed {
		b.timer.Reset(b.idle)
	}
	b.mu.Unlock()
	return n, e
}
func (b *idleBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.timer.Stop()
	b.mu.Unlock()
	b.cancel()
	e := b.ReadCloser.Close()
	if b.direct != nil {
		b.direct.CloseIdleConnections()
	}
	return e
}

func writeDownloadResponse(resp *http.Response, destPath string) error {
	if resp.StatusCode != http.StatusOK {
		return &downloadHTTPStatusError{Code: resp.StatusCode, Status: resp.Status}
	}
	if resp.ContentLength > maxAudioBytes || resp.Header.Get("Content-Encoding") != "" {
		return fmt.Errorf("invalid audio response")
	}
	if resp.Body == nil {
		return fmt.Errorf("响应体为空")
	}
	dir := filepath.Dir(destPath)
	if err := fsutil.EnsureDir(dir, fsutil.DirMode); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".audio-*.part")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	committed := false
	defer func() {
		_ = f.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	_ = f.Chmod(0o600)
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxAudioBytes+1))
	if err != nil {
		return err
	}
	if n <= 0 || n > maxAudioBytes || (resp.ContentLength >= 0 && n != resp.ContentLength) {
		return fmt.Errorf("invalid audio size")
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return err
	}
	committed = true
	_ = os.Chmod(destPath, 0o600)
	return nil
}

func isRetryableDownloadError(err error) bool {
	if code := downloadStatus(err); code != 0 {
		return code == 408 || code == 429 || code >= 500
	}
	return true
}

func shouldFallbackDirect(client *http.Client, req *http.Request, err error) bool {
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return false
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpTransport, ok := transport.(*http.Transport)
	if !ok || httpTransport.Proxy == nil {
		return false
	}
	proxyURL, proxyErr := httpTransport.Proxy(req)
	if proxyErr != nil || proxyURL == nil {
		return false
	}
	host := strings.TrimSpace(proxyURL.Hostname())
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func directHTTPClient(source *http.Client) *http.Client {
	transport := source.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	var directTransport http.RoundTripper
	if base, ok := transport.(*http.Transport); ok {
		clone := base.Clone()
		clone.Proxy = nil
		directTransport = clone
	} else {
		directTransport = &http.Transport{Proxy: nil}
	}
	return &http.Client{
		Transport:     directTransport,
		CheckRedirect: source.CheckRedirect,
		Jar:           source.Jar,
	}
}
