package recording

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
)

const SummaryIndexLimit = 32 << 20

var ErrSummaryIndexTooLarge = errors.New("recording index exceeds summary read limit")

// ReadIndexSnapshot reads only committed metadata under the existing writer
// lock. Unlike the legacy ListAll startup fallback, malformed indexes are errors.
// It never opens audio/transcripts or normalizes/rewrites the index on a read.
func (s *Storage) ReadIndexSnapshot(ctx context.Context) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(s.dir)
	if os.IsNotExist(err) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("recording root is not a directory")
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		return nil, errors.New("recording root changed")
	}
	info, err = root.Lstat(indexFileName)
	if os.IsNotExist(err) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("recording index is not a regular file")
	}
	if info.Size() > SummaryIndexLimit {
		return nil, ErrSummaryIndexTooLarge
	}
	f, err := root.Open(indexFileName)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err = f.Stat()
	if err != nil || !os.SameFile(info, actual) || actual.Size() != info.Size() || !actual.ModTime().Equal(info.ModTime()) {
		return nil, errors.New("recording index changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, SummaryIndexLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > SummaryIndexLimit {
		return nil, ErrSummaryIndexTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var wrapper indexWrapper
	if json.Unmarshal(raw, &wrapper) != nil || wrapper.Recordings == nil {
		return nil, errors.New("invalid recording index")
	}
	return wrapper.Recordings, nil
}
