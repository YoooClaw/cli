package data

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/config"
)

func uploadRequest(raw []byte, key string) UploadRequest {
	h := sha256.Sum256(raw)
	return UploadRequest{Metadata: Metadata{Name: "sample.bin", Source: "records", IdempotencyKey: key, Meta: json.RawMessage(`{"kind":"file"}`)}, SizeBytes: int64(len(raw)), SHA256: hex.EncodeToString(h[:])}
}
func uploadStore(t *testing.T) *Store {
	t.Helper()
	s := New(t.TempDir(), config.DataSection{})
	if e := s.Init(); e != nil {
		t.Fatal(e)
	}
	return s
}
func TestUploadRestartRepeatChunksAndCommitReceipt(t *testing.T) {
	s := uploadStore(t)
	u := NewUploads(s)
	raw := bytes.Repeat([]byte{0, 255, 10, 128, 42}, ChunkBytes/2)
	r := uploadRequest(raw, "once")
	st, e := u.Begin(r, "a", "identity")
	if e != nil {
		t.Fatal(e)
	}
	again, e := u.Begin(r, "a", "identity")
	if e != nil || again.UploadID != st.UploadID {
		t.Fatal(again, e)
	}
	if _, e = u.Commit(st.UploadID, "a", "identity"); e == nil {
		t.Fatal("incomplete commit")
	}
	first := raw[:ChunkBytes]
	st, e = u.Chunk(st.UploadID, "a", "identity", 0, first)
	if e != nil {
		t.Fatal(e)
	}
	if st.Offset != int64(len(first)) {
		t.Fatal(st)
	}
	// A new service instance simulates a daemon restart. Persisted bytes determine offset.
	u = NewUploads(New(s.Dir, s.Config))
	st, e = u.Status(st.UploadID, "a", "identity")
	if e != nil || st.Offset != int64(len(first)) {
		t.Fatal(st, e)
	}
	st, e = u.Chunk(st.UploadID, "a", "identity", 0, first)
	if e != nil || st.Offset != int64(len(first)) {
		t.Fatal(st, e)
	}
	if _, e = u.Chunk(st.UploadID, "a", "identity", 0, []byte("changed")); e == nil {
		t.Fatal("conflicting replay accepted")
	}
	// Replay the last part of an acknowledged chunk together with new bytes.
	start := int64(len(first) - 10)
	for start < int64(len(raw)) {
		end := min(start+ChunkBytes, int64(len(raw)))
		st, e = u.Chunk(st.UploadID, "a", "identity", start, raw[start:end])
		if e != nil {
			t.Fatal(e)
		}
		start = st.Offset
	}
	st, e = u.Commit(st.UploadID, "a", "identity")
	if e != nil || st.State != "succeeded" || st.ObjectID == "" {
		t.Fatal(st, e)
	}
	saved := st
	st, e = u.Commit(st.UploadID, "a", "identity")
	if e != nil || st.ObjectID != saved.ObjectID {
		t.Fatal(st, e)
	}
	resolved, e := u.Resolve("once", "a", "identity")
	if e != nil || resolved.ObjectID != saved.ObjectID {
		t.Fatal(resolved, e)
	}
	var got []byte
	for offset := int64(0); ; {
		part, e := s.Content(saved.ObjectID, "a", offset, ChunkBytes)
		if e != nil {
			t.Fatal(e)
		}
		b, e := base64.StdEncoding.DecodeString(part.ContentBase64)
		if e != nil {
			t.Fatal(e)
		}
		got = append(got, b...)
		if part.EOF {
			break
		}
		offset = part.NextOffset
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("binary roundtrip changed bytes")
	}
	page, e := s.List(Filter{Scope: "a"})
	if e != nil || len(page.Items) != 1 {
		t.Fatal(page, e)
	}
	// A deliberate second send produces a second record, sharing the physical blob.
	r.IdempotencyKey = "twice"
	second, e := u.Begin(r, "a", "identity")
	if e != nil {
		t.Fatal(e)
	}
	for offset := 0; offset < len(raw); offset += ChunkBytes {
		end := min(offset+ChunkBytes, len(raw))
		if _, e = u.Chunk(second.UploadID, "a", "identity", int64(offset), raw[offset:end]); e != nil {
			t.Fatal(e)
		}
	}
	second, e = u.Commit(second.UploadID, "a", "identity")
	if e != nil || second.ObjectID == saved.ObjectID {
		t.Fatal(second, e)
	}
}
func TestUploadIsolationIntegrityAndAbort(t *testing.T) {
	s := uploadStore(t)
	u := NewUploads(s)
	r := uploadRequest([]byte("abc"), "same")
	st, e := u.Begin(r, "a", "identity")
	if e != nil {
		t.Fatal(e)
	}
	for _, fn := range []func() error{
		func() error { _, e := u.Status(st.UploadID, "b", "identity"); return e },
		func() error { _, e := u.Chunk(st.UploadID, "b", "identity", 0, []byte("abc")); return e },
		func() error { _, e := u.Commit(st.UploadID, "b", "identity"); return e },
		func() error { _, e := u.Abort(st.UploadID, "b", "identity"); return e },
		func() error { _, e := u.Status(st.UploadID, "a", "rotated"); return e },
	} {
		if e := fn(); e == nil {
			t.Fatal("identity boundary bypassed")
		}
	}
	if res, e := u.Resolve("same", "b", "identity"); e != nil || res != nil {
		t.Fatal(res, e)
	}
	altered := r
	altered.Title = "changed"
	if _, e := u.Begin(altered, "a", "identity"); e == nil {
		t.Fatal("metadata conflict accepted")
	}
	if _, e = u.Chunk(st.UploadID, "a", "identity", 1, []byte("x")); e == nil {
		t.Fatal("gap accepted")
	}
	if _, e = u.Chunk(st.UploadID, "a", "identity", 0, []byte("abcd")); e == nil {
		t.Fatal("oversize accepted")
	}
	if _, e = u.Chunk(st.UploadID, "a", "identity", 0, []byte("bad")); e != nil {
		t.Fatal(e)
	}
	if _, e = u.Commit(st.UploadID, "a", "identity"); e == nil || !strings.Contains(e.Error(), "SHA-256") {
		t.Fatal("corruption committed", e)
	}
	page, _ := s.List(Filter{})
	if len(page.Items) != 0 {
		t.Fatal("partial object visible")
	}
	st, e = u.Abort(st.UploadID, "a", "identity")
	if e != nil || st.State != "aborted" {
		t.Fatal(st, e)
	}
	if _, e = u.Abort(st.UploadID, "a", "identity"); e != nil {
		t.Fatal(e)
	}
	if _, e = u.Chunk(st.UploadID, "a", "identity", 0, []byte("abc")); e == nil {
		t.Fatal("aborted upload reopened")
	}
	if _, e = os.Stat(filepath.Join(s.Dir, "uploads", st.UploadID+".part")); !os.IsNotExist(e) {
		t.Fatal(e)
	}
}
func TestUploadReconcilesCrashAfterObjectCommit(t *testing.T) {
	s := uploadStore(t)
	u := NewUploads(s)
	raw := []byte("crash receipt")
	r := uploadRequest(raw, "crash")
	st, e := u.Begin(r, "a", "identity")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = u.Chunk(st.UploadID, "a", "identity", 0, raw); e != nil {
		t.Fatal(e)
	}
	// Simulate durable object commit followed by a crash before journal completion.
	m := r.Metadata
	m.IdempotencyKey = "upload:" + st.UploadID
	obj, e := s.ImportFile(filepath.Join(s.Dir, "uploads", st.UploadID+".part"), m, "a")
	if e != nil {
		t.Fatal(e)
	}
	st, e = NewUploads(s).Status(st.UploadID, "a", "identity")
	if e != nil || st.State != "succeeded" || st.ObjectID != obj.Data.ID {
		t.Fatal(st, e)
	}
	page, _ := s.List(Filter{})
	if len(page.Items) != 1 {
		t.Fatal(page)
	}
}
func TestUploadZeroLimitsSymlinksAndContentBounds(t *testing.T) {
	s := uploadStore(t)
	s.Config.MaxImportBytes = 4
	s.Config.QuotaBytes = 4
	u := NewUploads(s)
	if _, e := u.Begin(uploadRequest([]byte("12345"), "large"), "a", "i"); e == nil {
		t.Fatal("size limit")
	}
	st, e := u.Begin(uploadRequest(nil, "empty"), "a", "i")
	if e != nil {
		t.Fatal(e)
	}
	st, e = u.Commit(st.UploadID, "a", "i")
	if e != nil {
		t.Fatal(e)
	}
	part, e := s.Content(st.ObjectID, "a", 0, 1)
	if e != nil || !part.EOF || part.ContentBase64 != "" {
		t.Fatal(part, e)
	}
	for _, v := range []struct {
		offset int64
		limit  int
	}{{-1, 1}, {1, 1}, {0, 0}, {0, ChunkBytes + 1}} {
		if _, e = s.Content(st.ObjectID, "a", v.offset, v.limit); e == nil {
			t.Fatal("invalid content range", v)
		}
	}
	if _, e = u.Begin(uploadRequest([]byte("1234"), "full"), "a", "i"); e != nil {
		t.Fatal(e)
	}
	if _, e = u.Begin(uploadRequest([]byte("1"), "over"), "b", "i"); e == nil {
		t.Fatal("reservations exceeded quota")
	}
	if _, e = u.Status("../../outside", "a", "i"); e == nil {
		t.Fatal("path traversal")
	}
	target := filepath.Join(t.TempDir(), "target")
	if e = os.WriteFile(target, []byte("private"), 0600); e != nil {
		t.Fatal(e)
	}
	bad := uploadID("symlink", "a")
	link := filepath.Join(s.Dir, "uploads", bad+".json")
	if e = os.Symlink(target, link); e == nil {
		if _, e = u.Status(bad, "a", "i"); e == nil {
			t.Fatal("symlink followed")
		}
	}
}

func TestUploadConcurrentReplayAndReservedCapacity(t *testing.T) {
	s := uploadStore(t)
	u := NewUploads(s)
	r := uploadRequest([]byte("abc"), "concurrent")
	results := make(chan UploadStatus, 8)
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { st, e := NewUploads(s).Begin(r, "a", "i"); results <- st; errors <- e }()
	}
	var id string
	for i := 0; i < 8; i++ {
		st := <-results
		if e := <-errors; e != nil {
			t.Fatal(e)
		}
		if id != "" && id != st.UploadID {
			t.Fatal("duplicate sessions")
		}
		id = st.UploadID
	}
	for i := 0; i < 8; i++ {
		go func() { st, e := u.Chunk(id, "a", "i", 0, []byte("abc")); results <- st; errors <- e }()
	}
	for i := 0; i < 8; i++ {
		st := <-results
		if e := <-errors; e != nil || st.Offset != 3 {
			t.Fatal(st, e)
		}
	}
	for i := 0; i < 8; i++ {
		go func() { st, e := u.Commit(id, "a", "i"); results <- st; errors <- e }()
	}
	var object string
	for i := 0; i < 8; i++ {
		st := <-results
		if e := <-errors; e != nil {
			t.Fatal(e)
		}
		if object != "" && object != st.ObjectID {
			t.Fatal("duplicate commits")
		}
		object = st.ObjectID
	}
	for i := 0; i < 32; i++ {
		req := uploadRequest(nil, fmt.Sprint(i))
		if _, e := u.Begin(req, "a", "i"); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := u.Begin(uploadRequest(nil, "busy"), "a", "i"); e == nil {
		t.Fatal("unbounded pending uploads")
	}
}

func TestUploadTerminalReceiptCleansStagingAfterRestart(t *testing.T) {
	s := uploadStore(t)
	u := NewUploads(s)
	r := uploadRequest([]byte("done"), "terminal")
	st, e := u.Begin(r, "a", "i")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = u.Chunk(st.UploadID, "a", "i", 0, []byte("done")); e != nil {
		t.Fatal(e)
	}
	st, e = u.Commit(st.UploadID, "a", "i")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(s.Dir, "uploads", st.UploadID+".part")
	// Model a crash after journal success but before removing the staging file.
	if e = os.WriteFile(path, []byte("done"), 0600); e != nil {
		t.Fatal(e)
	}
	st, e = NewUploads(s).Status(st.UploadID, "a", "i")
	if e != nil || st.State != "succeeded" {
		t.Fatal(st, e)
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("terminal staging not cleaned", e)
	}
}
