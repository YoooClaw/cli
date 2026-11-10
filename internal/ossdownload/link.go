package ossdownload

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultLinkRefreshWindow  = 5 * time.Minute
	defaultLinkRefreshTimeout = 30 * time.Second
)

// Link is one signed OSS URL and the time it stops being usable. A zero
// ExpiresAt means the caller does not know the expiry.
type Link struct {
	URL       string
	ExpiresAt time.Time
}

// LinkRefresher keeps a signed URL current for one download. Every range
// request shares it, so concurrent parts collapse into one refresh call.
// A refresh failure stays cached until ResetError, which the caller does per
// outer attempt; this prevents a failing file service from being hammered.
type LinkRefresher struct {
	mu         sync.Mutex
	link       Link
	refresh    func(context.Context) (Link, error)
	now        func() time.Time
	window     time.Duration
	timeout    time.Duration
	zeroValid  bool
	refreshed  bool
	refreshErr error
}

// LinkRefresherOptions tunes when a link is considered stale.
type LinkRefresherOptions struct {
	// Window refreshes links that expire within this duration. Zero uses 5m.
	Window time.Duration
	// Timeout bounds a single refresh call. Zero uses 30s.
	Timeout time.Duration
	// Now is overridable for tests.
	Now func() time.Time
	// ZeroExpiryValid treats a zero ExpiresAt as unknown rather than expired.
	// Recording links may not carry an expiry; migration links always do.
	ZeroExpiryValid bool
}

func NewLinkRefresher(initial Link, refresh func(context.Context) (Link, error), opts LinkRefresherOptions) *LinkRefresher {
	r := &LinkRefresher{link: initial, refresh: refresh, now: opts.Now, window: opts.Window, timeout: opts.Timeout, zeroValid: opts.ZeroExpiryValid}
	if r.now == nil {
		r.now = time.Now
	}
	if r.window <= 0 {
		r.window = defaultLinkRefreshWindow
	}
	if r.timeout <= 0 {
		r.timeout = defaultLinkRefreshTimeout
	}
	return r
}

// Current returns a usable link, refreshing first when it is missing or inside
// the expiry window.
func (r *LinkRefresher) Current(ctx context.Context) (Link, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Link{}, err
	}
	if r.refreshErr != nil {
		return Link{}, r.refreshErr
	}
	if r.link.URL == "" {
		// 例如导出 ready 后进程重启：初始链接缓存已丢失，必须重新签名。
		if r.refresh == nil {
			return Link{}, errors.New("download link unavailable")
		}
		return r.refreshLocked(ctx)
	}
	if r.link.ExpiresAt.IsZero() {
		if r.zeroValid || r.refresh == nil {
			return r.link, nil
		}
		return r.refreshLocked(ctx)
	}
	if r.link.ExpiresAt.After(r.now().Add(r.window)) || r.refresh == nil {
		return r.link, nil
	}
	return r.refreshLocked(ctx)
}

// ForceRefresh discards a cached refresh error and gets a new link. It backs
// the HTTP 403 fallback once per download.
func (r *LinkRefresher) ForceRefresh(ctx context.Context) (Link, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshErr = nil
	return r.refreshLocked(ctx)
}

// Set replaces the cached link without calling the refresh callback. Callers
// use it when they already obtained a new signed URL out of band.
func (r *LinkRefresher) Set(link Link) {
	r.mu.Lock()
	r.link = link
	r.refreshErr = nil
	r.mu.Unlock()
}

// ResetError clears a cached refresh failure for a new outer attempt.
func (r *LinkRefresher) ResetError() {
	r.mu.Lock()
	r.refreshErr = nil
	r.mu.Unlock()
}

// CanRefresh reports whether a refresh callback exists.
func (r *LinkRefresher) CanRefresh() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refresh != nil
}

// HasRefreshed reports whether a refresh succeeded at least once.
func (r *LinkRefresher) HasRefreshed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refreshed
}

func (r *LinkRefresher) refreshLocked(ctx context.Context) (Link, error) {
	if r.refresh == nil {
		return r.link, nil
	}
	refreshCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	next, err := r.refresh(refreshCtx)
	if err != nil {
		r.refreshErr = err
		return Link{}, err
	}
	if strings.TrimSpace(next.URL) == "" {
		err = errors.New("download link refresh returned empty URL")
		r.refreshErr = err
		return Link{}, err
	}
	if next.ExpiresAt.IsZero() {
		if !r.zeroValid {
			err = errors.New("download link refresh returned no expiry")
			r.refreshErr = err
			return Link{}, err
		}
	} else if !next.ExpiresAt.After(r.now().Add(r.window)) {
		err = errors.New("download link refresh returned insufficient expiry")
		r.refreshErr = err
		return Link{}, err
	}
	r.link = next
	r.refreshed = true
	r.refreshErr = nil
	return next, nil
}

// RenewingClient rewrites every OSS GET to the current signed URL while the
// SDK downloader keeps its range state and checkpoints.
type RenewingClient struct {
	Refresher *LinkRefresher
	Next      HTTPClient
}

func (c *RenewingClient) Do(req *http.Request) (*http.Response, error) {
	link, err := c.Refresher.Current(req.Context())
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(link.URL)
	if err != nil {
		return nil, errors.New("invalid download link")
	}
	clone := req.Clone(req.Context())
	clone.URL = u
	clone.Host = ""
	clone.RequestURI = ""
	return c.Next.Do(clone)
}
