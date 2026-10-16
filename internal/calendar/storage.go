package calendar

import (
	"path/filepath"
	"sync"

	"github.com/YoooClaw/cli/internal/fsutil"
)

// Storage 串行覆盖同一 profile 的日程日期文件，不读旧记录或比较 ID。
type Storage struct {
	dir string
	mu  sync.Mutex
}

func NewStorage(dir string) *Storage { return &Storage{dir: dir} }

// Replace 每个日期使用现有原子写出口；范围外的文件不会访问。
func (s *Storage) Replace(batch *Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, file := range batch.files {
		path := filepath.Join(s.dir, file.date+".jsonl")
		if err := fsutil.WriteAtomic(path, file.data, fsutil.SecretFileMode); err != nil {
			return err
		}
	}
	return nil
}
