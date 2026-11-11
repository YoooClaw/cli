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

// DayWriteResult describes whether the incoming day snapshot was selected.
// Applied includes idempotent replacements; it does not imply a new revision.
type DayWriteResult struct {
	Date    string `json:"date"`
	Applied bool   `json:"applied"`
	Reason  string `json:"reason,omitempty"`
}

// Append returns completed day decisions even if a later day fails to write.
func (s *Storage) Append(batch Batch) ([]DayWriteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch.daily == nil {
		return nil, fmt.Errorf("validated v3 snapshot required")
	}
	return s.replaceDaily(batch.daily)
}
