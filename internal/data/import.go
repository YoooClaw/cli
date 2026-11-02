package data

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/ossdownload"
)

// ImportRequest references an object uploaded by the existing OSS upload flow.
// Signed URLs are credentials: persisted privately, never returned by Status or events.
type ImportRequest struct {
	Metadata
	URL       string `json:"url"`
	OSSTaskID string `json:"ossTaskId,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}
type ImportStatus struct {
	TaskID    string    `json:"taskId"`
	State     string    `json:"state"`
	SizeBytes int64     `json:"sizeBytes"`
	SHA256    string    `json:"sha256"`
	ObjectID  string    `json:"objectId,omitempty"`
	Deduped   bool      `json:"deduped,omitempty"`
	ErrorCode string    `json:"errorCode,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type importRecord struct {
	Request  ImportRequest `json:"request"`
	Status   ImportStatus  `json:"status"`
	Label    string        `json:"label"`
	Identity string        `json:"identity"`
}
type ImportDependencies struct {
	Download   func(context.Context, string, string, string, string, int64) error
	Refresh    func(context.Context, string, string) (string, error)
	Identity   func(string) string
	Notify     func(string, ImportStatus)
	Timeout    time.Duration
	RetryDelay time.Duration
}
type Imports struct {
	failed bool // journal failure: reject work until daemon restart
	mu     sync.Mutex
	store  *Store
	dir    string
	deps   ImportDependencies
	jobs   map[string]*importRecord
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
}

func NewImports(store *Store, deps ImportDependencies) (*Imports, error) {
	if deps.Download == nil {
		return nil, Error("IMPORT_UNAVAILABLE", "OSS downloader unavailable")
	}
	if deps.Timeout <= 0 {
		deps.Timeout = time.Hour
	}
	if deps.RetryDelay <= 0 {
		deps.RetryDelay = time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Imports{store: store, dir: filepath.Join(store.Dir, "imports"), deps: deps, jobs: map[string]*importRecord{}, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{})}
	if e := fsutil.EnsureDir(s.dir, 0700); e != nil {
		cancel()
		return nil, e
	}
	if e := rejectSymlink(store.Dir, s.dir); e != nil {
		cancel()
		return nil, e
	}
	entries, e := os.ReadDir(s.dir)
	if e != nil {
		cancel()
		return nil, e
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, e := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if e != nil {
			cancel()
			return nil, e
		}
		var j importRecord
		if json.Unmarshal(raw, &j) != nil || !uuidRE.MatchString(j.Status.TaskID) || entry.Name() != j.Status.TaskID+".json" || !hashRE.MatchString(j.Request.SHA256) || j.Request.SizeBytes <= 0 || j.Label == "" {
			cancel()
			return nil, Error("IMPORT_CORRUPT", "invalid import journal")
		}
		switch j.Status.State {
		case "queued", "downloading":
			j.Status.State = "queued"
		case "succeeded", "failed":
			_ = os.RemoveAll(filepath.Join(s.dir, j.Status.TaskID))
		default:
			cancel()
			return nil, Error("IMPORT_CORRUPT", "invalid import state")
		}
		s.jobs[j.Status.TaskID] = &j
	}
	go s.loop()
	s.signal()
	return s, nil
}
func (s *Imports) Cancel() { s.cancel() }
func (s *Imports) Stop()   { s.cancel(); <-s.done }
func (s *Imports) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Imports) identity(label string) string {
	if s.deps.Identity == nil {
		return label
	}
	return s.deps.Identity(label)
}
func (s *Imports) save(j *importRecord) error {
	raw, e := json.Marshal(j)
	if e != nil {
		return e
	}
	return writeIndex(filepath.Join(s.dir, j.Status.TaskID+".json"), raw)
}
func importFingerprint(r ImportRequest) string {
	r.URL = ""
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *Imports) validate(r *ImportRequest) error {
	if e := validate(&r.Metadata); e != nil {
		return e
	}
	if r.URL == "" && r.OSSTaskID == "" {
		return Error("INVALID_PARAMS", "url or ossTaskId is required")
	}
	if r.URL != "" {
		if e := ValidateImportURL(r.URL); e != nil {
			return e
		}
	}
	if len(r.OSSTaskID) > 256 || strings.ContainsAny(r.OSSTaskID, "\r\n\x00") {
		return Error("INVALID_PARAMS", "invalid ossTaskId")
	}
	if !hashRE.MatchString(r.SHA256) || r.SizeBytes <= 0 {
		return Error("INVALID_PARAMS", "positive sizeBytes and lowercase SHA-256 are required")
	}
	if r.SizeBytes > s.store.Config.MaxImportBytes {
		return Error("TOO_LARGE", "object exceeds maxImportBytes")
	}
	return nil
}
func (s *Imports) Accept(r ImportRequest, label string) (ImportStatus, error) {
	if label == "" {
		label = "local"
	}
	if e := s.validate(&r); e != nil {
		return ImportStatus{}, e
	}
	identity := s.identity(label)
	if identity == "" {
		return ImportStatus{}, Error("IMPORT_UNAVAILABLE", "client credentials unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || s.failed {
		return ImportStatus{}, Error("IMPORT_UNAVAILABLE", "import worker unavailable; restart daemon")
	}
	for _, old := range s.jobs {
		if old.Label == label && r.IdempotencyKey != "" && old.Request.IdempotencyKey == r.IdempotencyKey {
			if old.Identity != identity || importFingerprint(old.Request) != importFingerprint(r) {
				return ImportStatus{}, Error("IDEMPOTENCY_CONFLICT", "import key already used for different request")
			}
			return old.Status, nil
		}
	}
	pending := 0
	var reserved int64
	for _, j := range s.jobs {
		if j.Status.State == "queued" || j.Status.State == "downloading" {
			pending++
			reserved += j.Request.SizeBytes
		}
	}
	if pending >= 32 {
		return ImportStatus{}, Error("IMPORT_BUSY", "import queue is full")
	}
	if reserved > s.store.Config.QuotaBytes-r.SizeBytes {
		return ImportStatus{}, Error("QUOTA_EXCEEDED", "pending imports exceed profile quota")
	}
	if e := fsutil.RequireSpace(s.dir, 2*r.SizeBytes); e != nil {
		return ImportStatus{}, Error("QUOTA_EXCEEDED", "insufficient staging disk space")
	}
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return ImportStatus{}, e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	id := fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
	now := time.Now()
	j := &importRecord{r, ImportStatus{TaskID: id, State: "queued", SizeBytes: r.SizeBytes, SHA256: r.SHA256, CreatedAt: now, UpdatedAt: now}, label, identity}
	if e := s.save(j); e != nil {
		return ImportStatus{}, e
	}
	s.jobs[id] = j
	s.signal()
	return j.Status, nil
}

// Job status is private even for historical/shared labels: it is not legacy content.
func (s *Imports) Status(id, scope string) (ImportStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil || (scope != "" && j.Label != scope) {
		return ImportStatus{}, Error("NOT_FOUND", "import task not found")
	}
	if s.failed && (j.Status.State == "queued" || j.Status.State == "downloading") {
		return ImportStatus{}, Error("WRITE_FAILED", "import journal could not be saved; restart daemon")
	}
	return j.Status, nil
}
func (s *Imports) Retry(id, scope, rawURL string) (ImportStatus, error) {
	if rawURL != "" {
		if e := ValidateImportURL(rawURL); e != nil {
			return ImportStatus{}, e
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil || (scope != "" && j.Label != scope) {
		return ImportStatus{}, Error("NOT_FOUND", "import task not found")
	}
	if s.ctx.Err() != nil || s.failed {
		return ImportStatus{}, Error("IMPORT_UNAVAILABLE", "import worker unavailable; restart daemon")
	}
	if j.Identity != s.identity(j.Label) {
		return ImportStatus{}, Error("IMPORT_UNAVAILABLE", "client credentials changed")
	}
	if j.Status.State != "failed" {
		return j.Status, nil
	}
	// Bound requeued jobs just like newly accepted requests.
	pending := 0
	reserved := j.Request.SizeBytes
	for _, other := range s.jobs {
		if other.Status.State == "queued" || other.Status.State == "downloading" {
			pending++
			reserved += other.Request.SizeBytes
		}
	}
	if pending >= 32 || reserved > s.store.Config.QuotaBytes {
		return ImportStatus{}, Error("IMPORT_BUSY", "import queue is full")
	}
	next := *j
	if rawURL != "" {
		next.Request.URL = rawURL
	}
	next.Status.State = "queued"
	next.Status.ErrorCode = ""
	next.Status.UpdatedAt = time.Now()
	if e := s.save(&next); e != nil {
		return ImportStatus{}, e
	}
	*j = next
	s.signal()
	return j.Status, nil
}
func (s *Imports) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.Status.State == "queued" || j.Status.State == "downloading" {
			return true
		}
	}
	return false
}
func (s *Imports) loop() {
	defer close(s.done)
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		}
		for {
			s.mu.Lock()
			ids := []string{}
			for id, j := range s.jobs {
				if j.Status.State == "queued" {
					ids = append(ids, id)
				}
			}
			sort.Strings(ids)
			if len(ids) == 0 {
				s.mu.Unlock()
				break
			}
			j := s.jobs[ids[0]]
			next := *j
			next.Status.State = "downloading"
			next.Status.UpdatedAt = time.Now()
			if e := s.save(&next); e != nil {
				s.failed = true
				s.mu.Unlock()
				return
			}
			*j = next
			copyJob := next
			s.mu.Unlock()
			result, e := s.run(copyJob)
			s.mu.Lock()
			next = *j
			if s.ctx.Err() != nil {
				next.Status.State = "queued"
			} else if e != nil {
				next.Status.State = "failed"
				next.Status.ErrorCode = importErrorCode(e)
			} else {
				next.Status.State = "succeeded"
				next.Status.ObjectID = result.Data.ID
				next.Status.Deduped = result.Deduped
				next.Status.ErrorCode = ""
				next.Request.URL = ""
			}
			next.Status.UpdatedAt = time.Now()
			saveErr := s.save(&next)
			if saveErr == nil {
				*j = next
			} else {
				s.failed = true
			}
			s.mu.Unlock()
			if saveErr != nil {
				return
			}
			if s.ctx.Err() != nil {
				return
			}
			_ = os.RemoveAll(filepath.Join(s.dir, next.Status.TaskID))
			// Status is durable; events are best-effort and never broadcast to other labels.
			if s.deps.Notify != nil {
				s.deps.Notify(next.Label, next.Status)
			}
		}
	}
}
func importErrorCode(e error) string {
	var status *ossdownload.HTTPError
	if errors.As(e, &status) && (status.Code == 401 || status.Code == 403) {
		return "YOOOCLAW_DATA_URL_EXPIRED"
	}
	var d *errs.Error
	if errors.As(e, &d) && strings.HasPrefix(d.Code, "YOOOCLAW_DATA_") {
		return d.Code
	}
	return "YOOOCLAW_DATA_DOWNLOAD_FAILED"
}
func (s *Imports) run(j importRecord) (PutResult, error) {
	var result PutResult
	r := j.Request
	if j.Identity != s.identity(j.Label) {
		return result, Error("IMPORT_UNAVAILABLE", "client credentials changed")
	}
	if r.SizeBytes > s.store.Config.MaxImportBytes {
		return result, Error("TOO_LARGE", "object exceeds maxImportBytes")
	}
	if prior, found, e := s.store.committedImport(j.Status.TaskID, j.Label, r.SHA256, r.SizeBytes); e != nil || found {
		return prior, e
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.deps.Timeout)
	defer cancel()
	dir := filepath.Join(s.dir, j.Status.TaskID)
	if e := fsutil.EnsureDir(dir, 0700); e != nil {
		return result, e
	}
	if e := rejectSymlink(s.store.Dir, dir); e != nil {
		return result, e
	}
	// Interrupted jobs retain private checkpoints; terminal jobs release staging.
	defer func() {
		if s.ctx.Err() == nil {
			_ = os.RemoveAll(dir)
		}
	}()
	if e := fsutil.RequireSpace(dir, 2*r.SizeBytes); e != nil {
		return result, Error("QUOTA_EXCEEDED", "insufficient staging disk space")
	}
	dest := filepath.Join(dir, "content")
	rawURL := r.URL
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if j.Identity != s.identity(j.Label) {
			return result, Error("IMPORT_UNAVAILABLE", "client credentials changed")
		}
		if r.OSSTaskID != "" && s.deps.Refresh != nil && (rawURL == "" || attempt > 0) {
			rawURL, err = s.deps.Refresh(ctx, j.Label, r.OSSTaskID)
		} else {
			err = nil
		}
		if err == nil {
			err = s.deps.Download(ctx, rawURL, j.Status.TaskID, dest, dir, r.SizeBytes)
		}
		if err == nil {
			break
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * s.deps.RetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if err != nil {
		return result, err
	}
	f, e := os.Open(dest)
	if e != nil {
		return result, e
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, r.SizeBytes+1))
	f.Close()
	if e != nil {
		return result, e
	}
	if n != r.SizeBytes || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return result, Error("INTEGRITY_MISMATCH", "download size or SHA-256 mismatch")
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if j.Identity != s.identity(j.Label) {
		return result, Error("IMPORT_UNAVAILABLE", "client credentials changed")
	}
	m := r.Metadata
	m.IdempotencyKey = "oss-import:" + j.Status.TaskID
	return s.store.ImportFile(dest, m, j.Label)
}
