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
// Dropped and Warnings report records discarded during validation; they only
// appear when the rest of the day was still written.
type DayWriteResult struct {
	Date     string   `json:"date"`
	Applied  bool     `json:"applied"`
	Reason   string   `json:"reason,omitempty"`
	Message  string   `json:"message,omitempty"`
	Dropped  int      `json:"dropped,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Append returns completed day decisions even if a later day fails to write.
func (s *Storage) Append(batch Batch) ([]DayWriteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch.daily == nil || len(batch.dayInfo) != len(batch.daily.Days) {
		return nil, fmt.Errorf("validated v3 snapshot required")
	}
	return s.replaceDaily(&batch)
}
