package healthdata

import (
	"fmt"
	"sync"
)

type Storage struct {
	dir string
	mu  sync.Mutex
}

func NewStorage(dir string) *Storage { return &Storage{dir: dir} }
func (s *Storage) Append(batch Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch.daily == nil {
		return fmt.Errorf("validated v3 snapshot required")
	}
	return s.replaceDaily(batch.daily)
}
