package recording

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

// RecordingDownloadResult 是录音音频 + SRT 的下载结果。
type RecordingDownloadResult struct {
	Audio DownloadResult  `json:"audio"`
	SRT   *DownloadResult `json:"srt,omitempty"`
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
		if err == nil && resp != nil {
			err = writeDownloadResponse(resp, destPath)
		}
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
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
		_ = os.Remove(destPath)
		logger.Warn(fmt.Sprintf("[downloader] 下载失败 (attempt %d/%d): %s", attempt, retries, lastErr))
		if attempt < retries {
			time.Sleep(backoff * time.Duration(1<<(attempt-1)))
		}
	}
	return DownloadResult{OK: false, Error: lastErr}
}

func writeDownloadResponse(resp *http.Response, destPath string) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
	}
	if resp.Body == nil {
		return fmt.Errorf("响应体为空")
	}
	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// DownloadRecordingFiles 下载录音音频与可选 SRT。
func DownloadRecordingFiles(audioURL, srtURL, audioDestPath, srtDestPath string, logger Logger, opts DownloadOptions) RecordingDownloadResult {
	audio := DownloadFile(audioURL, audioDestPath, logger, opts)
	var srt *DownloadResult
	if srtURL != "" {
		res := DownloadFile(srtURL, srtDestPath, logger, opts)
		if !res.OK {
			logger.Warn("[downloader] 打点文件下载失败（非致命）: " + res.Error)
		}
		srt = &res
	}
	return RecordingDownloadResult{Audio: audio, SRT: srt}
}
