package data

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

// Uploads is a synchronous, resumable Relay transport. No worker or retry loop.
// A separate profile lock serializes sessions; commit then acquires Store's lock.
var uploadLocks sync.Map

type UploadRequest struct {
	Metadata
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}
type UploadStatus struct {
	UploadID  string    `json:"uploadId"`
	State     string    `json:"state"`
	Offset    int64     `json:"offset"`
	SizeBytes int64     `json:"sizeBytes"`
	SHA256    string    `json:"sha256"`
	ObjectID  string    `json:"objectId,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type uploadJournal struct {
	Request  UploadRequest `json:"request"`
	Status   UploadStatus  `json:"status"`
	Label    string        `json:"label"`
	Identity string        `json:"identity"`
}
type Uploads struct {
	store *Store
	dir   string
	mu    *sync.Mutex
}

func NewUploads(s *Store) *Uploads {
	dir := filepath.Join(s.Dir, "uploads")
	mu, _ := uploadLocks.LoadOrStore(dir, &sync.Mutex{})
	return &Uploads{s, dir, mu.(*sync.Mutex)}
}
func uploadID(key, label string) string {
	sum := sha256.Sum256([]byte(label + "\x00" + key))
	return hex.EncodeToString(sum[:])
}
func (s *Uploads) init() error {
	if err := fsutil.EnsureDir(s.dir, 0700); err != nil {
		return err
	}
	return rejectSymlink(s.store.Dir, s.dir)
}
func (s *Uploads) save(j uploadJournal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return writeIndex(filepath.Join(s.dir, j.Status.UploadID+".json"), b)
}
func (s *Uploads) read(id, label, identity string) (uploadJournal, error) {
	var j uploadJournal
	if !hashRE.MatchString(id) {
		return j, Error("NOT_FOUND", "upload not found")
	}
	path := filepath.Join(s.dir, id+".json")
	if err := rejectSymlink(s.store.Dir, path); err != nil {
		if os.IsNotExist(err) {
			return j, Error("NOT_FOUND", "upload not found")
		}
		return j, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return j, Error("NOT_FOUND", "upload not found")
	}
	if err != nil {
		return j, err
	}
	if json.Unmarshal(b, &j) != nil || j.Status.UploadID != id || !hashRE.MatchString(j.Request.SHA256) || j.Request.SizeBytes < 0 || uploadID(j.Request.IdempotencyKey, j.Label) != id {
		return j, Error("UPLOAD_CORRUPT", "invalid upload journal")
	}
	if j.Label != label {
		return uploadJournal{}, Error("NOT_FOUND", "upload not found")
	}
	if identity == "" || j.Identity != identity {
		return uploadJournal{}, Error("UPLOAD_UNAVAILABLE", "client credentials changed")
	}
	return j, nil
}
func (s *Uploads) part(id string) (string, error) {
	path := filepath.Join(s.dir, id+".part")
	if err := rejectSymlink(s.store.Dir, s.dir); err != nil {
		return "", err
	}
	if err := rejectSymlink(s.store.Dir, path); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}
func (s *Uploads) reconcile(j *uploadJournal) error {
	if j.Status.State == "succeeded" || j.Status.State == "aborted" {
		path, err := s.part(j.Status.UploadID)
		if err != nil {
			return err
		}
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if j.Status.State != "uploading" {
		return Error("UPLOAD_CORRUPT", "invalid upload state")
	}
	o, err := s.store.Resolve("upload:"+j.Status.UploadID, j.Label)
	if err != nil {
		return err
	}
	if o != nil {
		if o.SHA256 != j.Request.SHA256 || o.SizeBytes != j.Request.SizeBytes {
			return Error("INTEGRITY_MISMATCH", "committed upload does not match journal")
		}
		if _, err = s.store.Path(*o); err != nil {
			return err
		}
		j.Status.State = "succeeded"
		j.Status.ObjectID = o.ID
		j.Status.Offset = o.SizeBytes
		j.Status.UpdatedAt = time.Now()
		if err = s.save(*j); err != nil {
			return err
		}
		path, err := s.part(j.Status.UploadID)
		if err != nil {
			return err
		}
		return os.RemoveAll(path)
	}
	path, err := s.part(j.Status.UploadID)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > j.Request.SizeBytes {
		return Error("UPLOAD_CORRUPT", "invalid upload staging file")
	}
	j.Status.Offset = info.Size()
	return nil
}
func (s *Uploads) Begin(r UploadRequest, label, identity string) (UploadStatus, error) {
	if err := validate(&r.Metadata); err != nil {
		return UploadStatus{}, err
	}
	if r.IdempotencyKey == "" || !hashRE.MatchString(r.SHA256) || r.SizeBytes < 0 {
		return UploadStatus{}, Error("INVALID_PARAMS", "idempotencyKey, sizeBytes and SHA-256 are required")
	}
	if identity == "" {
		return UploadStatus{}, Error("UPLOAD_UNAVAILABLE", "client credentials unavailable")
	}
	if r.SizeBytes > s.store.Config.MaxImportBytes {
		return UploadStatus{}, Error("TOO_LARGE", "upload exceeds maxImportBytes")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.init(); err != nil {
		return UploadStatus{}, err
	}
	id := uploadID(r.IdempotencyKey, label)
	if _, err := os.Lstat(filepath.Join(s.dir, id+".json")); err == nil {
		j, err := s.read(id, label, identity)
		if err != nil {
			return UploadStatus{}, err
		}
		a, _ := json.Marshal(r)
		b, _ := json.Marshal(j.Request)
		if !bytes.Equal(a, b) {
			return UploadStatus{}, Error("IDEMPOTENCY_CONFLICT", "upload key already used for different request")
		}
		if err = s.reconcile(&j); err != nil {
			return UploadStatus{}, err
		}
		return j.Status, nil
	} else if !os.IsNotExist(err) {
		return UploadStatus{}, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return UploadStatus{}, err
	}
	pending := 0
	var reserved int64
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		if err = rejectSymlink(s.store.Dir, path); err != nil {
			return UploadStatus{}, err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return UploadStatus{}, err
		}
		var old uploadJournal
		if json.Unmarshal(b, &old) != nil || old.Request.SizeBytes < 0 {
			return UploadStatus{}, Error("UPLOAD_CORRUPT", "invalid upload journal")
		}
		if old.Status.State == "uploading" {
			pending++
			reserved += old.Request.SizeBytes
		}
	}
	if pending >= 32 {
		return UploadStatus{}, Error("UPLOAD_BUSY", "at most 32 incomplete uploads")
	}
	usage, err := s.store.Usage()
	if err != nil {
		return UploadStatus{}, err
	}
	if r.SizeBytes > s.store.Config.QuotaBytes-usage.Bytes-reserved {
		return UploadStatus{}, Error("QUOTA_EXCEEDED", "insufficient upload quota")
	}
	if err = fsutil.RequireSpace(s.dir, 2*(reserved+r.SizeBytes)); err != nil {
		return UploadStatus{}, Error("QUOTA_EXCEEDED", "insufficient staging disk space")
	}
	path, err := s.part(id)
	if err != nil {
		return UploadStatus{}, err
	}
	// A part without its journal is an unacknowledged begin and may be recreated.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return UploadStatus{}, err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return UploadStatus{}, err
	}
	if closeErr != nil {
		return UploadStatus{}, closeErr
	}
	j := uploadJournal{r, UploadStatus{UploadID: id, State: "uploading", SizeBytes: r.SizeBytes, SHA256: r.SHA256, UpdatedAt: time.Now()}, label, identity}
	if err = s.save(j); err != nil {
		return UploadStatus{}, err
	}
	return j.Status, nil
}
func (s *Uploads) Status(id, label, identity string) (UploadStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.read(id, label, identity)
	if err != nil {
		return UploadStatus{}, err
	}
	if err = s.reconcile(&j); err != nil {
		return UploadStatus{}, err
	}
	return j.Status, nil
}
func (s *Uploads) Resolve(key, label, identity string) (*UploadStatus, error) {
	if key == "" || len(key) > 256 {
		return nil, Error("INVALID_PARAMS", "idempotencyKey is required")
	}
	id := uploadID(key, label)
	// Absence is not evidence that an in-flight request will not arrive later.
	if _, err := os.Lstat(filepath.Join(s.dir, id+".json")); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	st, err := s.Status(id, label, identity)
	if err != nil {
		return nil, err
	}
	return &st, nil
}
func (s *Uploads) Chunk(id, label, identity string, offset int64, b []byte) (UploadStatus, error) {
	if offset < 0 || len(b) == 0 || len(b) > ChunkBytes {
		return UploadStatus{}, Error("INVALID_PARAMS", "invalid upload chunk")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.read(id, label, identity)
	if err != nil {
		return UploadStatus{}, err
	}
	if err = s.reconcile(&j); err != nil {
		return UploadStatus{}, err
	}
	if j.Status.State != "uploading" {
		return UploadStatus{}, Error("IDEMPOTENCY_CONFLICT", "upload is no longer writable")
	}
	if offset > j.Status.Offset || int64(len(b)) > j.Request.SizeBytes-offset {
		return UploadStatus{}, Error("INVALID_PARAMS", "chunk exceeds expected offset or size")
	}
	path, err := s.part(id)
	if err != nil {
		return UploadStatus{}, err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return UploadStatus{}, err
	}
	defer f.Close()
	overlap := min(int64(len(b)), j.Status.Offset-offset)
	if overlap > 0 {
		old := make([]byte, overlap)
		if _, err = f.ReadAt(old, offset); err != nil {
			return UploadStatus{}, err
		}
		if !bytes.Equal(old, b[:overlap]) {
			return UploadStatus{}, Error("IDEMPOTENCY_CONFLICT", "chunk differs from stored bytes")
		}
	}
	if overlap < int64(len(b)) {
		if err = fsutil.RequireSpace(s.dir, int64(len(b))-overlap); err != nil {
			return UploadStatus{}, Error("QUOTA_EXCEEDED", "insufficient disk space")
		}
		if _, err = f.WriteAt(b[overlap:], offset+overlap); err != nil {
			return UploadStatus{}, err
		}
	}
	if err = f.Sync(); err != nil {
		return UploadStatus{}, err
	}
	j.Status.Offset = max(j.Status.Offset, offset+int64(len(b)))
	j.Status.UpdatedAt = time.Now()
	return j.Status, nil
}
func (s *Uploads) Commit(id, label, identity string) (UploadStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.read(id, label, identity)
	if err != nil {
		return UploadStatus{}, err
	}
	if err = s.reconcile(&j); err != nil {
		return UploadStatus{}, err
	}
	if j.Status.State == "succeeded" {
		return j.Status, nil
	}
	if j.Status.State != "uploading" {
		return UploadStatus{}, Error("IDEMPOTENCY_CONFLICT", "upload aborted")
	}
	if j.Status.Offset != j.Request.SizeBytes {
		return UploadStatus{}, Error("INVALID_PARAMS", "upload incomplete")
	}
	path, err := s.part(id)
	if err != nil {
		return UploadStatus{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return UploadStatus{}, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	f.Close()
	if err != nil {
		return UploadStatus{}, err
	}
	if n != j.Request.SizeBytes || fmt.Sprintf("%x", h.Sum(nil)) != j.Request.SHA256 {
		return UploadStatus{}, Error("INTEGRITY_MISMATCH", "upload size or SHA-256 mismatch")
	}
	m := j.Request.Metadata
	m.IdempotencyKey = "upload:" + id
	result, err := s.store.ImportFile(path, m, label)
	if err != nil {
		return UploadStatus{}, err
	}
	j.Status.State = "succeeded"
	j.Status.ObjectID = result.Data.ID
	j.Status.UpdatedAt = time.Now()
	if err = s.save(j); err != nil {
		return UploadStatus{}, err
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return UploadStatus{}, err
	}
	return j.Status, nil
}
func (s *Uploads) Abort(id, label, identity string) (UploadStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.read(id, label, identity)
	if err != nil {
		return UploadStatus{}, err
	}
	if err = s.reconcile(&j); err != nil {
		return UploadStatus{}, err
	}
	if j.Status.State == "succeeded" {
		return UploadStatus{}, Error("IDEMPOTENCY_CONFLICT", "saved objects cannot be aborted")
	}
	j.Status.State = "aborted"
	j.Status.UpdatedAt = time.Now()
	if err = s.save(j); err != nil {
		return UploadStatus{}, err
	}
	path, err := s.part(id)
	if err != nil {
		return UploadStatus{}, err
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return UploadStatus{}, err
	}
	return j.Status, nil
}
