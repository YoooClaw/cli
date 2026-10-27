package calendar

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

// stateFile 记录最近一次成功应用的 collectedAt。和日期文件放在同一目录，
// 随 profile 一起备份/迁移；读取日期文件的调用方只认 YYYY-MM-DD.jsonl。
const stateFile = "state.json"

type state struct {
	Version         int    `json:"version"`
	LastCollectedAt string `json:"lastCollectedAt"`
}

// Result 描述一次快照写入结果。
// Applied 为 false 表示 collectedAt 早于上次应用的快照，整批被跳过。
type Result struct {
	Applied      bool
	FilesWritten int
}

// Storage 串行覆盖同一 profile 的日程日期文件，不读旧记录或比较 ID。
type Storage struct {
	dir string
	mu  sync.Mutex
}

func NewStorage(dir string) *Storage { return &Storage{dir: dir} }

// Replace 每个日期使用现有原子写出口；范围外的文件不会访问。
// 迟到的旧快照整批跳过，避免乱序重试把新数据覆盖回旧版本。
func (s *Storage) Replace(batch *Batch) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if last := s.lastCollectedAt(); last.After(batch.collectedAt) {
		return Result{Applied: false}, nil
	}
	for _, file := range batch.files {
		path := filepath.Join(s.dir, file.date+".jsonl")
		if err := fsutil.WriteAtomic(path, file.data, fsutil.SecretFileMode); err != nil {
			return Result{}, err
		}
	}
	// 日期文件写成功后再推进水位；中途失败时客户端重试同一份快照会整批重放。
	st := state{Version: 1, LastCollectedAt: batch.collectedAt.Format(time.RFC3339Nano)}
	if err := fsutil.WriteJSON(filepath.Join(s.dir, stateFile), st, fsutil.SecretFileMode); err != nil {
		return Result{}, err
	}
	return Result{Applied: true, FilesWritten: len(batch.files)}, nil
}

// lastCollectedAt 读取上次应用的快照时间。状态文件缺失或损坏都返回零值：
// 宁可多应用一次，也不要让一条坏元数据把后续上报永久卡住。
func (s *Storage) lastCollectedAt() time.Time {
	var st state
	if exists, err := fsutil.ReadJSON(filepath.Join(s.dir, stateFile), &st); err != nil || !exists {
		return time.Time{}
	}
	last, err := time.Parse(time.RFC3339Nano, st.LastCollectedAt)
	if err != nil {
		return time.Time{}
	}
	return last
}
