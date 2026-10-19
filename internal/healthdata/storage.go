package healthdata

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

// Storage 串行更新业务日期快照；旧版原始批次按接收日单独归档。
type Storage struct {
	dir string
	mu  sync.Mutex
}

func NewStorage(dir string) *Storage { return &Storage{dir: dir} }

func (s *Storage) Append(batch Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch.daily != nil {
		return s.replaceDaily(batch.daily)
	}
	legacyDir := filepath.Join(s.dir, "legacy", "receipts")
	if err := fsutil.EnsureDir(legacyDir, fsutil.DirMode); err != nil {
		return err
	}
	path := filepath.Join(legacyDir, time.Now().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, fsutil.SecretFileMode)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	var line []byte
	if info.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
			return err
		}
		// A prior interrupted write must not run into the next complete batch.
		if last[0] != '\n' {
			line = append(line, '\n')
		}
	}
	line = append(line, batch.line...)
	line = append(line, '\n')
	n, err := f.Write(line)
	if err != nil {
		return err
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	return f.Close()
}
