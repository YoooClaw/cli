package location

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

// Storage 串行追加同一 profile 下的位置记录，不去重或修改客户端字段。
type Storage struct {
	dir string
	mu  sync.Mutex
}

func NewStorage(dir string) *Storage {
	return &Storage{dir: dir}
}

// Append 在写入并关闭文件后返回成功。调用方先校验整批记录。
func (s *Storage) Append(items []Record) error {
	var lines []byte
	for _, item := range items {
		line, err := json.Marshal(item)
		if err != nil {
			return err
		}
		lines = append(lines, line...)
		lines = append(lines, '\n')
	}
	if len(lines) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fsutil.EnsureDir(s.dir, fsutil.DirMode); err != nil {
		return err
	}
	path := filepath.Join(s.dir, time.Now().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, fsutil.SecretFileMode)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	// 上次写入若留下半行，先结束该行，保证后续记录仍可逐行读取。
	if info.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			lines = append([]byte{'\n'}, lines...)
		}
	}
	n, err := f.Write(lines)
	if err != nil {
		return err
	}
	if n != len(lines) {
		return io.ErrShortWrite
	}
	return f.Close()
}
