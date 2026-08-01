package recording

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

const (
	defaultDownloadTimeout = 5 * time.Minute
	defaultDownloadRetries = 3
	defaultRetryBackoff    = 2 * time.Second
)

// DownloadOptions 控制 OSS 下载重试与超时。
type DownloadOptions struct {
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

	_ = fsutil.EnsureDir(filepath.Dir(destPath), fsutil.DirMode)
	var lastErr string
	for attempt := 1; attempt <= retries; attempt++ {
		start := time.Now()
		logger.Info(fmt.Sprintf("[downloader] 开始下载 (attempt %d/%d): %s", attempt, retries, rawURL))
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return DownloadResult{OK: false, Error: err.Error()}
		}
		resp, err := client.Do(req)
		var fallbackClient *http.Client
		if err != nil && shouldFallbackDirect(client, req, err) {
			logger.Warn("[downloader] 本机代理不可用，当前下载降级为直连: " + rawURL)
			fallbackClient = directHTTPClient(client)
			resp, err = fallbackClient.Do(req.Clone(ctx))
		}
		if err == nil && resp != nil {
			err = writeDownloadResponse(resp, destPath)
		}
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if fallbackClient != nil {
			fallbackClient.CloseIdleConnections()
		}
		cancel()

		if err == nil {
			info, statErr := os.Stat(destPath)
			size := int64(0)
			if statErr == nil {
				size = info.Size()
			}
			elapsed := time.Since(start)
			logger.Info(fmt.Sprintf("[downloader] 下载完成: %s (%d bytes, %s)", destPath, size, elapsed))
			return DownloadResult{OK: true, SizeBytes: size, Elapsed: elapsed}
		}

		lastErr = err.Error()
		logger.Warn(fmt.Sprintf("[downloader] 下载失败 (attempt %d/%d): %s", attempt, retries, lastErr))
		if !isRetryableDownloadError(err) {
			break
		}
		if attempt < retries {
			time.Sleep(backoff * time.Duration(1<<(attempt-1)))
		}
	}
	return DownloadResult{OK: false, Error: lastErr}
}

func writeDownloadResponse(resp *http.Response, destPath string) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &downloadHTTPStatusError{Code: resp.StatusCode, Status: resp.Status}
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
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
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
	var statusErr *downloadHTTPStatusError
	if errors.As(err, &statusErr) {
		// 408/429 可能是临时限流；其余 4xx 重试不会改变结果。
		return statusErr.Code == http.StatusRequestTimeout ||
			statusErr.Code == http.StatusTooManyRequests ||
			statusErr.Code >= 500
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
