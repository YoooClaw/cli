package healthdata

import (
	"fmt"
	"sync"
)

type Storage struct {
	dir string
	mu  sync.Mutex
}

func NewStorage(dir string) *Storage        { return &Storage{dir: dir} }
func (s *Storage) Append(batch Batch) error { _, err := s.AppendResult(batch); return err }
func (s *Storage) AppendResult(batch Batch) (int, error) {
	result, err := s.AppendDetails(batch)
	return result.Committed, err
}

type Result struct {
	Committed    int
	ChangedDates []string
}

// AppendDetails reports only successfully replaced dates, even on partial failure.
func (s *Storage) AppendDetails(batch Batch) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch.daily == nil {
		return Result{}, fmt.Errorf("validated v3 snapshot required")
	}
	return s.replaceDaily(batch.daily)
}
