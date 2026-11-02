package data

import (
	"encoding/base64"
	"io"
	"os"
)

// ChunkBytes bounds both memory and JSON Relay frames independently of object size.
const ChunkBytes = 256 << 10

// Resolve only searches the authenticated writer's idempotency namespace.
func (s *Store) Resolve(key, label string) (*Object, error) {
	if key == "" || len(key) > 256 {
		return nil, Error("INVALID_PARAMS", "idempotencyKey is required (maximum 256 bytes)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	x, err := s.read()
	if err != nil {
		return nil, err
	}
	id := x.Keys[label+"\x00"+key]
	for _, o := range x.Objects {
		if o.ID == id && o.ClientLabel == label {
			return &o, nil
		}
	}
	return nil, nil
}

type ContentChunk struct {
	ID            string `json:"id"`
	SHA256        string `json:"sha256"`
	SizeBytes     int64  `json:"sizeBytes"`
	Offset        int64  `json:"offset"`
	NextOffset    int64  `json:"nextOffset"`
	EOF           bool   `json:"eof"`
	ContentBase64 string `json:"contentBase64"`
}

// Content is an immutable, bounded range. Never exposes a local pathname.
func (s *Store) Content(id, scope string, offset int64, limit int) (ContentChunk, error) {
	if offset < 0 || limit < 1 || limit > ChunkBytes {
		return ContentChunk{}, Error("INVALID_PARAMS", "invalid content range")
	}
	// Serialize with delete/GC so the selected object remains readable during the range.
	s.mu.Lock()
	defer s.mu.Unlock()
	o, err := s.Show(id, scope)
	if err != nil {
		return ContentChunk{}, err
	}
	if offset > o.SizeBytes {
		return ContentChunk{}, Error("INVALID_PARAMS", "offset exceeds object size")
	}
	path, err := s.Path(o)
	if err != nil {
		return ContentChunk{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return ContentChunk{}, err
	}
	defer f.Close()
	size := min(int64(limit), o.SizeBytes-offset)
	b := make([]byte, size)
	if _, err = f.ReadAt(b, offset); err != nil && !(err == io.EOF && size == 0) {
		return ContentChunk{}, Error("INTEGRITY_MISMATCH", "stored content is incomplete")
	}
	next := offset + size
	return ContentChunk{o.ID, o.SHA256, o.SizeBytes, offset, next, next == o.SizeBytes, base64.StdEncoding.EncodeToString(b)}, nil
}

// Resolve returns only the caller's task; URLs and submitted metadata stay private.
func (s *Imports) Resolve(key, label string) (*ImportStatus, error) {
	if key == "" || len(key) > 256 {
		return nil, Error("INVALID_PARAMS", "idempotencyKey is required (maximum 256 bytes)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.Label == label && j.Request.IdempotencyKey == key {
			if j.Identity != s.identity(label) {
				return nil, Error("IMPORT_UNAVAILABLE", "client credentials changed")
			}
			status := j.Status
			return &status, nil
		}
	}
	return nil, nil
}
