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
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch.daily == nil {
		return 0, fmt.Errorf("validated v3 snapshot required")
	}
	return s.replaceDaily(batch.daily)
}
