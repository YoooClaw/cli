package assets

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"unicode/utf8"

	"github.com/YoooClaw/cli/internal/healthdata"
)

func (s *Service) readHealth(ctx context.Context, r request, p *page) *Error {
	if r.resume && r.Mode != "day_files" {
		return expired()
	}
	files, e := dates(s.sources[r.Kind], false, r.cursor)
	if e != nil {
		return e
	}
	c := r.cursor
	c.Mode = "day_files"
	visited := 0
	budget := ScanLimit
	for i, day := range files {
		if c.BeforeDate != "" && day.date >= c.BeforeDate {
			continue
		}
		if ctx.Err() != nil {
			return readFailed()
		}
		if visited >= 14 || len(p.Items) >= r.limit {
			s.next(p, c)
			return nil
		}
		raw, size, err := s.healthBytes(day, &budget)
		if err != nil {
			if err.Code == "SCAN_BUDGET_REACHED" {
				p.warn(err.Code)
				s.next(p, c)
				return nil
			}
			return err
		}
		var it Item
		id := digest(s.profile, r.Kind, day.date)
		if size > ScanLimit {
			it = oversizedItem(id, day.date, size)
		} else {
			data, legacy, err := healthData(raw, day.date)
			if err != nil {
				return err
			}
			if legacy {
				p.warn("UNSUPPORTED_SCHEMA")
				c.BeforeDate = day.date
				visited++
				continue
			}
			data["revision"] = digest(string(raw))
			it = item(id, day.date, data)
			b, _ := json.Marshal(it)
			if len(b) > HealthItemLimit {
				it = oversizedItem(id, day.date, size)
			}
		}
		if !p.add(it) {
			s.next(p, c)
			return nil
		}
		c.BeforeDate = day.date
		visited++
		if i == len(files)-1 {
			return nil
		}
	}
	return nil
}

// Store writes are atomic and use the same mutex. Recheck identity and version
// after reading as protection against external replacements, retrying once.
func (s *Service) healthBytes(day dayFile, budget *int) ([]byte, int64, *Error) {
	for attempt := 0; attempt < 2; attempt++ {
		snap, e := s.open("health-data", day)
		if e != nil {
			if e.Code == "CURSOR_EXPIRED" {
				return nil, 0, changed()
			}
			return nil, 0, e
		}
		size := snap.info.Size()
		if size > ScanLimit {
			snap.file.Close()
			return nil, size, nil
		}
		if size > int64(*budget) {
			snap.file.Close()
			if attempt > 0 {
				return nil, 0, changed()
			}
			return nil, 0, failure(200, "SCAN_BUDGET_REACHED", "", "none")
		}
		raw, err := io.ReadAll(io.LimitReader(snap.file, size))
		snap.file.Close()
		*budget -= len(raw)
		stable := false
		checkErr := s.sources["health-data"].view(func() error {
			root, e := sourceRoot(s.sources["health-data"])
			if e != nil {
				return e
			}
			defer root.Close()
			info, e := root.Lstat(day.name)
			if e != nil {
				return e
			}
			stable = os.SameFile(snap.info, info) && metadataRevision(snap.info) == metadataRevision(info)
			return nil
		})
		if checkErr != nil && !os.IsNotExist(checkErr) {
			return nil, 0, readFailed()
		}
		if !stable || os.IsNotExist(checkErr) {
			continue
		}
		if err != nil || checkErr != nil || int64(len(raw)) != size {
			return nil, 0, readFailed()
		}
		return raw, size, nil
	}
	return nil, 0, changed()
}

func healthData(raw []byte, date string) (map[string]any, bool, *Error) {
	if !utf8.Valid(raw) {
		return nil, false, readFailed()
	}
	lines := bytes.SplitN(bytes.TrimSpace(raw), []byte{'\n'}, 2)
	if len(lines) == 0 {
		return nil, false, readFailed()
	}
	header, ok := object(lines[0])
	if !ok {
		return nil, false, readFailed()
	}
	if header["schemaVersion"] != json.Number("3") {
		return nil, true, nil
	}
	day, meta, err := healthdata.ReadSnapshot(raw, date)
	if err != nil {
		return nil, false, readFailed()
	}
	summaries := make([]healthdata.SleepListSummary, 0, len(day.SleepSessions))
	for _, session := range day.SleepSessions {
		summaries = append(summaries, session.Summary())
	}
	return map[string]any{"schemaVersion": 3, "date": date, "timeZone": meta.TimeZone, "provider": meta.Provider,
		"updatedAt": meta.UpdatedAt, "items": day.Items, "sleepSessions": summaries}, false, nil
}
