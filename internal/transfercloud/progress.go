package transfercloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

var transferIDRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// ProgressOptions is supplied by deployment, never by an imported archive.
// BaseURL is the agreed fulfillment transfer prefix, on the configured API host.
// TransferID is the fulfillment job ID, NOT the file service task ID.
type ProgressOptions struct {
	TransferID string
	BaseURL    string
	Interval   time.Duration
	// Called only for throttled report attempts, without exposing credentials or URLs.
	ReportLog func(progress int, success bool)
	// Local diagnostic heartbeat; never sends an extra server request.
	DownloadLog func(written, total int64, elapsed, idle time.Duration)
}

// ReportBaseURL uses the file service environment when no deployment override exists.
// Validation remains mandatory before credentials are sent.
func (c *Client) ReportBaseURL(override string) string {
	base := strings.TrimRight(strings.TrimSpace(override), "/")
	api, _ := url.Parse(c.baseURL)
	if base == "" {
		return api.Scheme + "://" + api.Host + "/api/fulfillment/plugin/transfer"
	}
	u, err := url.Parse(base)
	if err == nil && u.Path == "/fulfillment/plugin/transfer" {
		u.Path = "/api" + u.Path
		base = u.String()
	}
	return base
}

func (c *Client) ValidateProgress(o ProgressOptions) error {
	u, err := url.Parse(o.BaseURL)
	api, _ := url.Parse(c.baseURL)
	if !transferIDRE.MatchString(o.TransferID) || err != nil || u.Scheme != "https" || u.Host != api.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
		return failure("PROGRESS_CONFIG_INVALID", "下载进度需要有效 transferId 及同 API 主机的 HTTPS 履约接口前缀")
	}
	if o.Interval != 0 && (o.Interval < 15*time.Second || o.Interval > 24*time.Hour) {
		return failure("PROGRESS_CONFIG_INVALID", "进度上报间隔须为 15 秒至 24 小时")
	}
	return c.CheckAuth()
}

var errProgress = errors.New("下载进度未确认送达履约服务；不代表数据导入失败")

type progressError struct{ permanent bool }

func (e *progressError) Error() string { return errProgress.Error() }

func (c *Client) sendProgress(ctx context.Context, o ProgressOptions, value int) (reportErr error) {
	started := time.Now()
	observation := ReportObservation{Event: "download_progress", Progress: value, Code: "request_failed"}
	defer func() { finishReport(ctx, observation, started, reportErr) }()
	body, _ := json.Marshal(struct {
		Progress int `json:"progress"`
	}{value})
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimRight(o.BaseURL, "/")+"/"+o.TransferID+"/download-progress", bytes.NewReader(body))
	if err != nil {
		return errProgress
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key-Id", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		observation.Code = transportFailure(err)
		return errProgress
	}
	defer resp.Body.Close()
	observation.HTTPStatus = resp.StatusCode
	observation.Code = "http_error"
	permanent := func(code int) bool {
		return code == 400 || code == 401 || code == 403 || code == 404 || code == 409 || code == 410
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &progressError{permanent(resp.StatusCode)}
	}
	var bean struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&bean) != nil {
		observation.Code = "invalid_response"
		return errProgress
	}
	observation.Code = bean.Code
	if !bean.Success || bean.Code != "000000" {
		return &progressError{bean.Code == "400" || bean.Code == "401" || bean.Code == "403" || bean.Code == "404" || bean.Code == "409" || bean.Code == "410"}
	}
	return nil
}

// DownloadWithProgress uses the same downloader as legacy imports. Reporting
// is best effort and separate from downloadErr; callers must surface reportErr.
// A 100% receipt means the archive was downloaded, never that import succeeded.
func (c *Client) DownloadWithProgress(ctx context.Context, id, path string, o ProgressOptions) (downloadErr, reportErr error) {
	if err := c.ValidateProgress(o); err != nil {
		return err, nil
	}
	if !ValidTaskID(id) {
		return failure("INVALID_TASK", "迁移 taskId 无效"), nil
	}
	interval := o.Interval
	if interval == 0 {
		interval = 15 * time.Second
	}
	interval += time.Duration(rand.Int64N(int64(time.Second)))
	attempt := 0
	reporter := newProgressReporter(ctx, interval, func(ctx context.Context, n int) error {
		attempt++
		err := c.sendProgress(context.WithValue(ctx, reportAttemptKey{}, attempt), o, n)
		if o.ReportLog != nil {
			o.ReportLog(n, err == nil)
		}
		return err
	})
	monitor := newDownloadMonitor(o.DownloadLog, time.Minute)
	defer monitor.stop()
	downloadErr = c.download(ctx, id, path, func(_ int64, written int64, total int64) {
		monitor.update(written, total)
		if total > 0 && written >= 0 && written <= total {
			// SDK callbacks report absolute written bytes (including checkpoint bytes),
			// not network traffic. Across retries use a high water mark, never a sum.
			value := int(written * 100 / total)
			if value > 99 {
				value = 99
			} // 100 only after successful final local copy.
			reporter.update(value)
		}
	})
	reportErr = reporter.finish(downloadErr == nil)
	return
}

type progressReporter struct {
	mu     sync.Mutex
	latest int
	wake   chan struct{}
	stop   chan bool
	done   chan error
}

func newProgressReporter(ctx context.Context, interval time.Duration, send func(context.Context, int) error) *progressReporter {
	p := &progressReporter{latest: 0, wake: make(chan struct{}, 1), stop: make(chan bool, 1), done: make(chan error, 1)}
	go p.run(ctx, interval, send)
	return p
}
func (p *progressReporter) update(value int) {
	if value < 0 || value > 99 {
		return
	}
	p.mu.Lock()
	if value > p.latest {
		p.latest = value
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *progressReporter) value() int                { p.mu.Lock(); defer p.mu.Unlock(); return p.latest }
func (p *progressReporter) finish(success bool) error { p.stop <- success; return <-p.done }
func (p *progressReporter) run(ctx context.Context, interval time.Duration, send func(context.Context, int) error) {
	var lastErr error
	sent, failures := -1, 0
	blocked := false
	attempt := func(n int) {
		lastErr = send(ctx, n)
		if lastErr == nil {
			sent = n
			failures = 0
		} else {
			failures++
			var e *progressError
			blocked = errors.As(lastErr, &e) && e.permanent
		}
	}
	attempt(0)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	ready := false
	for {
		select {
		case <-ctx.Done():
			p.done <- errProgress
			return
		case success := <-p.stop:
			if success && !blocked {
				attempt(100)
			}
			p.done <- lastErr
			return
		case <-timer.C:
			ready = true
		case <-p.wake:
		}
		n := p.value()
		if ready && !blocked && n > sent {
			attempt(n)
			delay := interval
			ceiling := max(interval, 2*time.Minute)
			for i := 0; i < failures && delay < ceiling; i++ {
				if delay > ceiling/2 {
					delay = ceiling
					break
				}
				delay *= 2
			}
			timer.Reset(delay)
			ready = false
		}
	}
}
