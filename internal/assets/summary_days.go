package assets

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/YoooClaw/cli/internal/notif"
)

func (s *Service) summaryNotifications(ctx context.Context, q summaryQuery, store *notif.Storage, r *SummaryResult) *Error {
	if store == nil {
		return readFailed()
	}
	// Keep the root/symlink policy identical to the other asset readers.
	root, err := sourceRoot(s.sources["notifications"])
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return readFailed()
	}
	root.Close()
	stats, err := store.ReadDailyStatsContext(ctx, q.date)
	if errors.Is(err, notif.ErrDailyStatsTooLarge) {
		return tooLarge()
	}
	if err != nil {
		return readFailed()
	}
	*r.Count = stats.Count
	if stats.Latest != nil {
		raw, err := json.Marshal(stats.Latest)
		if err != nil {
			return readFailed()
		}
		obj, ok := object(raw)
		if !ok {
			return readFailed()
		}
		it, ok := project("notifications", obj, digest(s.profile, "summary", "notifications", q.date, string(raw)), q.date)
		if !ok {
			return readFailed()
		}
		r.Latest = &it
	}
	return nil
}

// Acquire a single date file under the current writer lock. Reads use the
// captured length/handle, so appends or atomic replacements cannot mix versions.
func (s *Service) summaryDaySnapshot(kind, date string) (*snapshot, *Error) {
	var result *snapshot
	var failure *Error
	err := s.sources[kind].view(func() error {
		root, err := sourceRoot(s.sources[kind])
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		defer root.Close()
		name := date + ".jsonl"
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular summary source")
		}
		if info.Size() > ScanLimit {
			failure = tooLarge()
			return nil
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		actual, err := f.Stat()
		if err != nil || !os.SameFile(info, actual) || metadataRevision(info) != metadataRevision(actual) {
			f.Close()
			failure = changed()
			return nil
		}
		result = &snapshot{file: f, info: actual, id: s.fileID(kind+"/"+name, actual)}
		return nil
	})
	if err != nil {
		return nil, readFailed()
	}
	return result, failure
}

func (s *Service) summaryDay(ctx context.Context, q summaryQuery, kind string, r *SummaryResult) *Error {
	snap, err := s.summaryDaySnapshot(kind, q.date)
	if err != nil || snap == nil {
		return err
	}
	defer snap.file.Close()
	reader := bufio.NewReader(io.LimitReader(snap.file, snap.info.Size()))
	var offset int64
	var newest time.Time
	p := newPage()
	for {
		if ctx.Err() != nil {
			return readFailed()
		}
		line, e := reader.ReadBytes('\n')
		if e != nil && e != io.EOF {
			return readFailed()
		}
		offset += int64(len(line))
		if !empty(line) {
			obj, ok := object(line)
			var it Item
			if ok {
				it, ok = project(kind, obj, digest(s.profile, "summary", kind, q.date, snap.id, fmt.Sprint(offset)), q.date)
			}
			if !ok {
				p.badRecord(e == io.EOF)
			} else {
				*r.Count++
				field := "timestamp"
				if kind == "calendars" {
					field = "startAt"
				}
				at, _ := time.Parse(time.RFC3339Nano, str(obj, field))
				newest = summaryLatest(r, it, at, newest)
			}
		}
		if e == io.EOF {
			break
		}
	}
	if offset != snap.info.Size() {
		return changed()
	}
	r.Warnings = p.Warnings
	return nil
}

func (s *Service) summaryHealth(ctx context.Context, r *SummaryResult) *Error {
	files, err := dates(s.sources["health-data"], false, cursor{Kind: "health-data"})
	if err != nil {
		return err
	}
	budget := ScanLimit
	p := newPage()
	for i, day := range files {
		if ctx.Err() != nil {
			return readFailed()
		}
		if i >= 14 {
			return tooLarge()
		}
		raw, size, err := s.healthBytes(day, &budget)
		if err != nil {
			if err.Code == "SCAN_BUDGET_REACHED" {
				return tooLarge()
			}
			return err
		}
		if size > ScanLimit {
			return tooLarge()
		}
		data, legacy, err := healthData(raw, day.date)
		if err != nil {
			return err
		}
		if legacy {
			p.warn("UNSUPPORTED_SCHEMA")
			continue
		}
		data["revision"] = digest(string(raw))
		it := item(digest(s.profile, "health-data", day.date), day.date, data)
		r.Latest = &it
		r.Warnings = p.Warnings
		return nil
	}
	r.Warnings = p.Warnings
	return nil
}
