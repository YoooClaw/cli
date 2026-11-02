package data

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/YoooClaw/cli/internal/ossdownload"
)

var ossHost = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.oss-[a-z0-9-]+\.aliyuncs\.com$`)

// ValidateImportURL restricts imports to HTTPS bucket URLs, not arbitrary fetches.
func ValidateImportURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || len(raw) > 8192 || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" || !ossHost.MatchString(u.Hostname()) || u.Path == "" || u.Path == "/" {
		return Error("INVALID_PARAMS", "a valid HTTPS OSS bucket object URL is required")
	}
	return nil
}

// NewImportHTTPClient checks the actual dial address and disables redirects/proxies.
// This also protects against DNS answers pointing an allowed hostname at a local service.
func NewImportHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.DisableCompression = true
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, Error("DOWNLOAD_FAILED", "invalid destination")
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil || len(ips) == 0 {
			return nil, Error("DOWNLOAD_FAILED", "OSS DNS lookup failed")
		}
		for _, ip := range ips {
			if !publicIP(ip.IP) {
				return nil, Error("DOWNLOAD_FAILED", "OSS address is not public")
			}
		}
		dialer := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			e = err
		}
		return nil, e
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func publicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	// Carrier-grade NAT and transition ranges can expose non-public destinations.
	for _, s := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "2001::/32", "2002::/16", "2001:db8::/32"} {
		_, n, _ := net.ParseCIDR(s)
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// DownloadImport reuses the SDK's range downloader and its durable checkpoints.
// A source without Range support is copied with a strict size bound.
func DownloadImport(ctx context.Context, client *http.Client, rawURL, key, dest, checkpoint string, size int64) error {
	if e := ValidateImportURL(rawURL); e != nil {
		return e
	}
	if client == nil {
		client = NewImportHTTPClient()
	}
	// Even an injected client must not follow a redirect to a new origin.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	e := ossdownload.Download(ctx, &copyClient, rawURL, key, dest, checkpoint, size, true)
	if e == nil {
		return os.Chmod(dest, 0600)
	}
	if !errors.Is(e, ossdownload.ErrNoRange) {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if e != nil {
		return Error("DOWNLOAD_FAILED", "invalid request")
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, e := copyClient.Do(req)
	if e != nil {
		return Error("DOWNLOAD_FAILED", "OSS request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &ossdownload.HTTPError{Code: resp.StatusCode}
	}
	if resp.ContentLength > size || resp.Header.Get("Content-Encoding") != "" {
		return Error("INTEGRITY_MISMATCH", "OSS size or encoding mismatch")
	}
	f, e := os.CreateTemp(filepath.Dir(dest), "stream-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	n, e := io.Copy(f, io.LimitReader(resp.Body, size+1))
	if e != nil {
		return e
	}
	if n != size {
		return Error("INTEGRITY_MISMATCH", "OSS size mismatch")
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return replaceIndex(f.Name(), dest)
}
