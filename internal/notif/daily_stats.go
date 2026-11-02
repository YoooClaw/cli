package notif

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

const dailyStatsDirName = ".stats"

// DailyStatsReadLimit bounds cold rebuilds requested by HTTP readers. Valid
// indexes remain usable for larger days; the existing writer is not capped.
const DailyStatsReadLimit = 32 << 20

var ErrDailyStatsTooLarge = errors.New("notification day exceeds stats rebuild limit")

// DailyStats describes the valid notifications in one existing local-date file.
// Latest is selected by timestamp, with the later stored row winning ties.
// TimeZone is the CLI's local zone; timestamps themselves are never rewritten.
type DailyStats struct {
	Date     string              `json:"date"`
	TimeZone string              `json:"timeZone"`
	Count    int64               `json:"count"`
	Latest   *StoredNotification `json:"latest"`
}

type dailyStatsSource struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedNS int64  `json:"modifiedNs"`
}

type dailyStatsIndex struct {
	SchemaVersion int `json:"schemaVersion"`
	DailyStats
	Source dailyStatsSource `json:"source"`
}

func newDailyStats(date string) dailyStatsIndex {
	return dailyStatsIndex{SchemaVersion: 1, DailyStats: DailyStats{Date: date, TimeZone: time.Local.String()}}
}

func validStatsDate(date string) bool {
	t, err := time.Parse(time.DateOnly, date)
	return err == nil && t.Format(time.DateOnly) == date
}

func statsSource(info os.FileInfo) dailyStatsSource {
	return dailyStatsSource{Name: info.Name(), Size: info.Size(), ModifiedNS: info.ModTime().UnixNano()}
}

func (s *Storage) dailyStatsPath(date string) string {
	return filepath.Join(s.dir, dailyStatsDirName, date+".json")
}

// ReadDailyStats reads a rebuildable summary, not the ingest counters or ID index.
// Historical days are indexed lazily. Source errors are returned, never cached
// as empty days. A cache write failure does not prevent returning verified data.
// This method takes the storage lock and must not be used inside WithReadView.
func (s *Storage) ReadDailyStats(date string) (DailyStats, error) {
	return s.ReadDailyStatsContext(context.Background(), date)
}

func (s *Storage) ReadDailyStatsContext(ctx context.Context, date string) (DailyStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, rebuilt, err := s.readDailyStatsContext(ctx, date, DailyStatsReadLimit)
	if err != nil {
		return DailyStats{}, err
	}
	if rebuilt {
		s.saveDailyStats(idx)
	}
	return idx.DailyStats, nil
}

// readDailyStats is called with the storage lock held. It does not write a cache:
// flushDay can use the baseline and persist only once, after the source append.
func (s *Storage) readDailyStats(date string) (dailyStatsIndex, bool, error) {
	return s.readDailyStatsContext(context.Background(), date, 0)
}

func (s *Storage) readDailyStatsContext(ctx context.Context, date string, limit int64) (dailyStatsIndex, bool, error) {
	idx := newDailyStats(date)
	if err := ctx.Err(); err != nil {
		return idx, false, err
	}
	if !validStatsDate(date) {
		return idx, false, errors.New("invalid notification date; expected YYYY-MM-DD")
	}
	path, info, err := s.dailyStatsSource(date)
	if err != nil {
		return idx, false, err
	}
	if info == nil {
		s.removeDailyStats(date)
		return idx, false, nil
	}
	idx.Source = statsSource(info)
	// Indexes are local derived data. Missing, corrupt, old-version and stale
	// entries all fall back to the source, without touching notification files.
	if raw, err := s.readStatsCache(date); err == nil {
		var cached dailyStatsIndex
		if json.Unmarshal(raw, &cached) == nil && cached.SchemaVersion == 1 &&
			cached.Date == date && cached.TimeZone == idx.TimeZone && cached.Source == idx.Source &&
			cached.Count >= 0 && (cached.Count == 0) == (cached.Latest == nil) {
			if cached.Latest == nil || validStatsNotification(*cached.Latest) {
				return cached, false, nil
			}
		}
	}
	if limit > 0 && info.Size() > limit {
		return idx, false, ErrDailyStatsTooLarge
	}
	if err := rebuildDailyStats(ctx, path, info, &idx); err != nil {
		return idx, false, err
	}
	return idx, true, nil
}

// dailyStatsSource prefers JSONL just like the asset reader. Only regular files
// are accepted; a directory, symlink or unreadable file must not look like zero.
func (s *Storage) dailyStatsSource(date string) (string, os.FileInfo, error) {
	for _, path := range []string{dayFilePath(s.dir, date), legacyDayFilePath(s.dir, date)} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		if !info.Mode().IsRegular() {
			return "", nil, fmt.Errorf("notification source is not a regular file: %s", info.Name())
		}
		return path, info, nil
	}
	return "", nil, nil
}

// statsDirectory also prevents derived notification contents being written
// through an accidentally symlinked .stats directory.
func (s *Storage) statsDirectory() error {
	info, err := os.Lstat(filepath.Join(s.dir, dailyStatsDirName))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("notification stats path is not a directory")
	}
	return nil
}

func (s *Storage) readStatsCache(date string) ([]byte, error) {
	if err := s.statsDirectory(); err != nil {
		return nil, err
	}
	path := s.dailyStatsPath(date)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("notification stats file is not regular")
	}
	if info.Size() > DailyStatsReadLimit {
		return nil, ErrDailyStatsTooLarge
	}
	return os.ReadFile(path)
}

func (s *Storage) saveDailyStats(idx dailyStatsIndex) {
	if !validStatsDate(idx.Date) {
		return
	}
	dir := filepath.Join(s.dir, dailyStatsDirName)
	err := os.MkdirAll(dir, fsutil.DirMode)
	if err == nil {
		err = s.statsDirectory()
	}
	if err == nil {
		err = fsutil.WriteJSON(s.dailyStatsPath(idx.Date), idx, fsutil.SecretFileMode)
	}
	if err != nil {
		s.removeDailyStats(idx.Date)
		s.logger.Warn("通知日统计索引写入失败，将按源文件重建: " + idx.Date)
	}
}

func (s *Storage) removeDailyStats(date string) {
	if validStatsDate(date) && s.statsDirectory() == nil {
		_ = os.Remove(s.dailyStatsPath(date))
	}
}

// finishDailyStats runs only after successful source writes, including imports
// and legacy migration. Failure affects the derived cache, never ingest success.
func (s *Storage) finishDailyStats(idx dailyStatsIndex, baselineErr error, entries []StoredNotification) {
	if baselineErr != nil {
		s.removeDailyStats(idx.Date)
		s.logger.Warn("通知日统计基线不可读，将按源文件重建: " + idx.Date)
		return
	}
	_, info, err := s.dailyStatsSource(idx.Date)
	if err != nil || info == nil {
		s.removeDailyStats(idx.Date)
		return
	}
	for _, entry := range entries {
		idx.add(entry)
	}
	idx.Source = statsSource(info)
	s.saveDailyStats(idx)
}

func validStatsNotification(entry StoredNotification) bool {
	_, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	return entry.AppName != "" && err == nil
}

func (idx *dailyStatsIndex) add(entry StoredNotification) {
	if !validStatsNotification(entry) {
		return
	}
	idx.Count++
	at, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if idx.Latest != nil {
		latest, _ := time.Parse(time.RFC3339Nano, idx.Latest.Timestamp)
		if at.Before(latest) {
			return
		}
	}
	idx.Latest = &entry
}
