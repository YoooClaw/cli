package data

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/ossdownload"
)

const testOSSURL = "https://test-bucket.oss-cn-hangzhou.aliyuncs.com/object?Signature=secret"

func importReq(b []byte) ImportRequest {
	h := sha256.Sum256(b)
	return ImportRequest{Metadata: Metadata{Name: "audio.bin", Tags: []string{"large"}, IdempotencyKey: "once"}, URL: testOSSURL, SizeBytes: int64(len(b)), SHA256: hex.EncodeToString(h[:])}
}
func awaitImport(t *testing.T, s *Imports, id, state string) ImportStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		v, e := s.Status(id, "")
		if e != nil {
			t.Fatal(e)
		}
		if v.State == state {
			return v
		}
		if v.State == "failed" && state != "failed" {
			t.Fatalf("import failed: %+v", v)
		}
		time.Sleep(5 * time.Millisecond)
	}
	v, _ := s.Status(id, "")
	t.Fatalf("waiting for %s: %+v", state, v)
	return v
}
func fakeImportDownload(b []byte) func(context.Context, string, string, string, string, int64) error {
	return func(ctx context.Context, u, key, dest, cp string, size int64) error {
		return os.WriteFile(dest, b, 0600)
	}
}
func TestImportsRoundTripScopeIdempotency(t *testing.T) {
	raw := []byte{0, 255, 1, 2, 3}
	store := New(t.TempDir(), config.DataSection{})
	events := make(chan ImportStatus, 2)
	s, e := NewImports(store, ImportDependencies{Download: fakeImportDownload(raw), Notify: func(label string, v ImportStatus) {
		if label != "a" {
			t.Errorf("wrong event label: %s", label)
		}
		events <- v
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	r := importReq(raw)
	a, e := s.Accept(r, "a")
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Accept(r, "a")
	if e != nil || b.TaskID != a.TaskID {
		t.Fatal(b, e)
	}
	r.SHA256 = strings.Repeat("0", 64)
	if _, e = s.Accept(r, "a"); e == nil {
		t.Fatal("key conflict accepted")
	}
	v := awaitImport(t, s, a.TaskID, "succeeded")
	o, e := store.Show(v.ObjectID, "a")
	if e != nil {
		t.Fatal(e)
	}
	p, e := store.Path(o)
	if e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(raw, got) {
		t.Fatal("bytes changed")
	}
	for _, scope := range []string{"b", "default"} {
		if _, e = s.Status(a.TaskID, scope); e == nil {
			t.Fatal("status leak")
		}
		if _, e = s.Retry(a.TaskID, scope, ""); e == nil {
			t.Fatal("retry leak")
		}
	}
	statusJSON, _ := json.Marshal(v)
	if strings.Contains(string(statusJSON), "secret") || strings.Contains(string(statusJSON), "https") {
		t.Fatal("URL exposed")
	}
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("event missing")
	}
	journal := filepath.Join(store.Dir, "imports", a.TaskID+".json")
	bts, _ := os.ReadFile(journal)
	if strings.Contains(string(bts), "Signature") {
		t.Fatal("successful journal retained URL")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(journal)
		if info.Mode().Perm() != 0600 {
			t.Fatal(info.Mode())
		}
	}
}
func TestImportIntegrityFailureRetryAndQuota(t *testing.T) {
	raw := []byte("expected")
	store := New(t.TempDir(), config.DataSection{MaxImportBytes: 8, QuotaBytes: 8})
	var good atomic.Bool
	dl := func(ctx context.Context, u, key, dest, cp string, n int64) error {
		b := []byte("tampered")
		if good.Load() {
			b = raw
		}
		return os.WriteFile(dest, b, 0600)
	}
	s, e := NewImports(store, ImportDependencies{Download: dl})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	r := importReq(raw)
	a, e := s.Accept(r, "a")
	if e != nil {
		t.Fatal(e)
	}
	v := awaitImport(t, s, a.TaskID, "failed")
	if v.ErrorCode != "YOOOCLAW_DATA_INTEGRITY_MISMATCH" {
		t.Fatal(v)
	}
	u, _ := store.Usage()
	if u.Objects != 0 || u.Bytes != 0 {
		t.Fatal("failed object committed", u)
	}
	good.Store(true)
	if _, e = s.Retry(a.TaskID, "a", testOSSURL+"2"); e != nil {
		t.Fatal(e)
	}
	awaitImport(t, s, a.TaskID, "succeeded")
	r.SizeBytes = 9
	if _, e = s.Accept(r, "a"); e == nil {
		t.Fatal("limit accepted")
	}
	put(t, store, raw, Metadata{}, "a")
	u, _ = store.Usage()
	if u.Bytes != 8 {
		t.Fatal("dedup quota", u)
	}
	r = importReq([]byte("different"))
	r.SizeBytes = 8
	r.IdempotencyKey = "other"
	if _, e = store.Put(strings.NewReader("other"), Metadata{}, "a"); e == nil {
		t.Fatal("quota overrun")
	}
}
func TestImportRestartAndCommitWindow(t *testing.T) {
	raw := []byte("resume")
	store := New(t.TempDir(), config.DataSection{})
	started := make(chan struct{})
	s, e := NewImports(store, ImportDependencies{Download: func(ctx context.Context, u, key, dest, cp string, n int64) error {
		os.WriteFile(filepath.Join(cp, "checkpoint-marker"), []byte("saved"), 0600)
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}})
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.Accept(importReq(raw), "a")
	if e != nil {
		t.Fatal(e)
	}
	<-started
	s.Stop()
	var resumed atomic.Bool
	s, e = NewImports(store, ImportDependencies{Download: func(ctx context.Context, u, key, dest, cp string, n int64) error {
		if _, e := os.Stat(filepath.Join(cp, "checkpoint-marker")); e != nil {
			return e
		}
		resumed.Store(true)
		return os.WriteFile(dest, raw, 0600)
	}})
	if e != nil {
		t.Fatal(e)
	}
	v := awaitImport(t, s, a.TaskID, "succeeded")
	s.Stop()
	if !resumed.Load() {
		t.Fatal("checkpoint not reused")
	}
	// Emulate a process dying after index commit but before updating its job journal.
	journal := filepath.Join(store.Dir, "imports", a.TaskID+".json")
	b, _ := os.ReadFile(journal)
	var j importRecord
	json.Unmarshal(b, &j)
	j.Status.State = "downloading"
	j.Status.ObjectID = ""
	j.Request.URL = ""
	b, _ = json.Marshal(j)
	writeIndex(journal, b)
	s, e = NewImports(store, ImportDependencies{Download: func(context.Context, string, string, string, string, int64) error {
		t.Error("committed object downloaded again")
		return fmt.Errorf("unexpected download")
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	recovered := awaitImport(t, s, a.TaskID, "succeeded")
	u, _ := store.Usage()
	if recovered.ObjectID != v.ObjectID || u.Objects != 1 {
		t.Fatal(recovered, u)
	}
}
func TestImportRefreshAndDoesNotHoldStoreLock(t *testing.T) {
	raw := []byte("test")
	store := New(t.TempDir(), config.DataSection{})
	started := make(chan struct{})
	release := make(chan struct{})
	var refreshes atomic.Int32
	s, e := NewImports(store, ImportDependencies{Refresh: func(ctx context.Context, label, id string) (string, error) {
		refreshes.Add(1)
		return testOSSURL + "renewed", nil
	}, Download: func(ctx context.Context, u, key, dest, cp string, n int64) error {
		if !strings.HasSuffix(u, "renewed") {
			return fmt.Errorf("stale URL")
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return os.WriteFile(dest, raw, 0600)
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	r := importReq(raw)
	r.OSSTaskID = "uploaded-task"
	r.URL = ""
	a, e := s.Accept(r, "a")
	if e != nil {
		t.Fatal(e)
	}
	<-started
	done := make(chan error, 1)
	go func() { _, e := store.Put(strings.NewReader("inline"), Metadata{}, "local"); done <- e }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("network held store lock")
	}
	if _, e = store.GC(false); e != nil {
		t.Fatal(e)
	}
	close(release)
	awaitImport(t, s, a.TaskID, "succeeded")
	if refreshes.Load() != 1 {
		t.Fatal("refresh missing")
	}
}
func TestImportCredentialChange(t *testing.T) {
	var changed atomic.Bool
	raw := []byte("private")
	started := make(chan struct{})
	release := make(chan struct{})
	s, e := NewImports(New(t.TempDir(), config.DataSection{}), ImportDependencies{Identity: func(label string) string {
		if changed.Load() {
			return "new-key"
		}
		return "old-key"
	}, Download: func(ctx context.Context, u, key, dest, cp string, n int64) error {
		close(started)
		<-release
		return os.WriteFile(dest, raw, 0600)
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	a, e := s.Accept(importReq(raw), "a")
	if e != nil {
		t.Fatal(e)
	}
	<-started
	changed.Store(true)
	close(release)
	v := awaitImport(t, s, a.TaskID, "failed")
	if v.ErrorCode != "YOOOCLAW_DATA_IMPORT_UNAVAILABLE" {
		t.Fatal(v)
	}
	if _, e = s.Retry(a.TaskID, "a", ""); e == nil {
		t.Fatal("retry under changed account")
	}
}

type rewriteTransport struct {
	base      *url.URL
	transport http.RoundTripper
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = r.base.Scheme
	u.Host = r.base.Host
	copy.URL = &u
	copy.Host = ""
	return r.transport.RoundTrip(copy)
}
func ossFixtureClient(t *testing.T, h http.HandlerFunc) *http.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return &http.Client{Transport: rewriteTransport{u, http.DefaultTransport}}
}
func TestDownloadImportRangeAboveHTTPBodyLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("large binary roundtrip")
	}
	raw := bytes.Repeat([]byte{0, 255, 2, 3}, (65<<20)/4)
	var ranges atomic.Int32
	client := ossFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		if _, e := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); e != nil || start < 0 || end >= int64(len(raw)) {
			t.Errorf("invalid range %s", r.Header.Get("Range"))
			w.WriteHeader(400)
			return
		}
		ranges.Add(1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(raw)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.Header().Set("ETag", `"stable"`)
		w.WriteHeader(206)
		w.Write(raw[start : end+1])
	})
	store := New(t.TempDir(), config.DataSection{MaxObjectBytes: 1, MaxImportBytes: 70 << 20})
	s, e := NewImports(store, ImportDependencies{Download: func(ctx context.Context, u, key, dest, cp string, n int64) error {
		return DownloadImport(ctx, client, u, key, dest, cp, n)
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	a, e := s.Accept(importReq(raw), "a")
	if e != nil {
		t.Fatal(e)
	}
	v := awaitImport(t, s, a.TaskID, "succeeded")
	o, e := store.Show(v.ObjectID, "")
	if e != nil {
		t.Fatal(e)
	}
	if o.SizeBytes != int64(len(raw)) || o.SHA256 != importReq(raw).SHA256 || ranges.Load() < 3 {
		t.Fatal(o, ranges.Load())
	}
	p, _ := store.Path(o)
	f, _ := os.Open(p)
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	if hex.EncodeToString(h.Sum(nil)) != o.SHA256 {
		t.Fatal("binary hash mismatch")
	}
}
func TestDownloadImportFallbackBoundsAndRedirect(t *testing.T) {
	for _, mode := range []string{"valid", "oversize", "redirect", "bad-etag"} {
		t.Run(mode, func(t *testing.T) {
			client := ossFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					w.Header().Set("Location", "http://127.0.0.1/private")
					w.WriteHeader(302)
					return
				}
				if mode == "bad-etag" {
					w.Header().Set("Content-Length", "1")
					w.Header().Set("Content-Range", "bytes 0-0/4")
					w.Header().Set("ETag", `"mismatch"`)
					w.WriteHeader(206)
					w.Write([]byte("x"))
					return
				}
				b := "test"
				if mode == "oversize" {
					b = "too long"
				}
				w.Write([]byte(b))
			})
			dir := t.TempDir()
			e := DownloadImport(context.Background(), client, testOSSURL, "key", filepath.Join(dir, "out"), dir, 4)
			if mode == "valid" && e != nil {
				t.Fatal(e)
			}
			if mode != "valid" && e == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}
func TestImportURLAndDialRestrictions(t *testing.T) {
	for _, u := range []string{"http://test-bucket.oss-cn-hangzhou.aliyuncs.com/x", "https://localhost/x", "https://127.0.0.1/x", "https://test-bucket.oss-cn-hangzhou.aliyuncs.com.evil.test/x", "https://user@test-bucket.oss-cn-hangzhou.aliyuncs.com/x", "https://test-bucket.oss-cn-hangzhou.aliyuncs.com:443/x", "https://test-bucket.oss-cn-hangzhou.aliyuncs.com/x#frag"} {
		if ValidateImportURL(u) == nil {
			t.Fatal(u)
		}
	}
	if e := ValidateImportURL(testOSSURL); e != nil {
		t.Fatal(e)
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "64:ff9b::7f00:1"} {
		if publicIP(net.ParseIP(ip)) {
			t.Fatal(ip)
		}
	}
	client := NewImportHTTPClient()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tr := client.Transport.(*http.Transport)
	if c, e := tr.DialContext(ctx, "tcp", "127.0.0.1:80"); e == nil {
		if c != nil {
			c.Close()
		}
		t.Fatal("private dial accepted")
	}
}

func TestImportExpiredURLRefreshAndBoundedRetries(t *testing.T) {
	raw := []byte("renew")
	var attempts, refreshes atomic.Int32
	s, e := NewImports(New(t.TempDir(), config.DataSection{}), ImportDependencies{RetryDelay: time.Millisecond, Refresh: func(context.Context, string, string) (string, error) {
		refreshes.Add(1)
		return testOSSURL + "new", nil
	}, Download: func(ctx context.Context, u, key, dest, cp string, n int64) error {
		attempts.Add(1)
		if !strings.HasSuffix(u, "new") {
			return &ossdownload.HTTPError{Code: 403}
		}
		return os.WriteFile(dest, raw, 0600)
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	r := importReq(raw)
	r.OSSTaskID = "task"
	a, e := s.Accept(r, "a")
	if e != nil {
		t.Fatal(e)
	}
	awaitImport(t, s, a.TaskID, "succeeded")
	if attempts.Load() != 2 || refreshes.Load() != 1 {
		t.Fatal(attempts.Load(), refreshes.Load())
	}
	r.OSSTaskID = ""
	r.IdempotencyKey = "expired"
	a, e = s.Accept(r, "a")
	if e != nil {
		t.Fatal(e)
	}
	v := awaitImport(t, s, a.TaskID, "failed")
	if v.ErrorCode != "YOOOCLAW_DATA_URL_EXPIRED" || attempts.Load() != 5 {
		t.Fatal(v, attempts.Load())
	}
}

func TestImportQueueReservationAndCorruptJournal(t *testing.T) {
	raw := []byte("12345678")
	store := New(t.TempDir(), config.DataSection{QuotaBytes: 10})
	s, e := NewImports(store, ImportDependencies{Download: func(ctx context.Context, _ string, _ string, _ string, _ string, _ int64) error {
		<-ctx.Done()
		return ctx.Err()
	}})
	if e != nil {
		t.Fatal(e)
	}
	r := importReq(raw)
	a, e := s.Accept(r, "a")
	if e != nil {
		t.Fatal(e)
	}
	r.IdempotencyKey = "second"
	if _, e = s.Accept(r, "b"); e == nil {
		t.Fatal("oversubscribed staging queue")
	}
	s.Stop()
	journal := filepath.Join(store.Dir, "imports", a.TaskID+".json")
	os.WriteFile(journal, []byte(`{"bad":true}`), 0600)
	if service, e := NewImports(store, ImportDependencies{Download: fakeImportDownload(raw)}); e == nil {
		service.Stop()
		t.Fatal("corrupt journal accepted")
	}
}

func TestImportJournalFailureIsVisible(t *testing.T) {
	raw := []byte("payload")
	started := make(chan struct{})
	release := make(chan struct{})
	store := New(t.TempDir(), config.DataSection{})
	s, e := NewImports(store, ImportDependencies{Download: func(ctx context.Context, u, key, dest, cp string, n int64) error {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return os.WriteFile(dest, raw, 0600)
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	a, e := s.Accept(importReq(raw), "a")
	if e != nil {
		t.Fatal(e)
	}
	<-started
	journal := filepath.Join(store.Dir, "imports", a.TaskID+".json")
	if e = os.Remove(journal); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(journal, 0700); e != nil {
		t.Fatal(e)
	}
	close(release)
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop on journal failure")
	}
	if _, e = s.Status(a.TaskID, "a"); e == nil {
		t.Fatal("journal failure hidden")
	}
	if _, e = s.Accept(importReq(raw), "a"); e == nil {
		t.Fatal("accepted work after journal failure")
	}
}
