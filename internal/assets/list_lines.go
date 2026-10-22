package assets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *Service) readLines(ctx context.Context, r request, p *page) *Error {
	files, e := dates(s.sources[r.Kind], r.Kind == "notifications", r.cursor)
	if e != nil {
		return e
	}
	index := 0
	if r.resume {
		index = -1
		for i, f := range files {
			if f.date == r.FileKey {
				index = i
				break
			}
		}
		if index < 0 {
			return expired()
		}
		expected := "jsonl_reverse"
		if r.forward() {
			expected = "jsonl_forward"
		}
		if strings.HasSuffix(files[index].name, ".json") {
			expected = "legacy_array"
		}
		if r.Mode != expected {
			return expired()
		}
	} else {
		if len(files) == 0 {
			return nil
		}
		r.cursor, e = s.enter(r.cursor, files[0])
		if e != nil {
			return e
		}
	}
	budget := ScanLimit
	fillPage := r.Kind == "calendars"
	fileLimit := 8
	if fillPage {
		fileLimit = 64
	}
	for visited := 0; visited < fileLimit && index < len(files); visited++ {
		if ctx.Err() != nil {
			return readFailed()
		}
		if r.Mode == "legacy_array" {
			done, err := s.readLegacy(r, p, files[index], &budget)
			if err != nil {
				return err
			}
			if !done {
				return nil
			}
		} else {
			snap, err := s.validate(r.cursor, files[index])
			if err != nil {
				return err
			}
			c, done, err := s.scanFile(ctx, r, p, snap, &budget)
			snap.file.Close()
			if err != nil {
				return err
			}
			r.cursor = c
			if !done {
				s.next(p, c)
				return nil
			}
		}
		// Sparse calendar days share one page and its scan/response budgets.
		// Other JSONL categories still stop at the first non-empty date.
		if !fillPage && len(p.Items) > 0 || len(p.Items) >= r.limit || visited+1 == fileLimit || budget < BlockSize {
			return s.advance(p, r.cursor, files, index)
		}
		index++
		if index < len(files) {
			r.cursor, e = s.enter(r.cursor, files[index])
			if e != nil {
				return e
			}
		}
	}
	return nil
}

func (s *Service) scanFile(ctx context.Context, r request, p *page, snap snapshot, budget *int) (cursor, bool, *Error) {
	c := r.cursor
	reader := lineReader{ctx: ctx, f: snap.file, end: c.SnapshotEnd, remaining: budget}
	for c.forward() && c.position() < c.SnapshotEnd || !c.forward() && c.position() > 0 {
		if len(p.Items) >= r.limit {
			return c, false, nil
		}
		row, next, e := reader.next(c)
		if errors.Is(e, errBudget) {
			p.warn("SCAN_BUDGET_REACHED")
			return next, false, nil
		}
		if e != nil {
			return c, false, readFailed()
		}
		id := digest(s.profile, c.Kind, c.FileKey, c.FileID, fmt.Sprint(row.recordEnd))
		var it Item
		switch {
		case row.oversized:
			it = oversizedItem(id, c.FileKey, row.end-row.start)
		case empty(row.raw):
			c = next
			continue
		default:
			obj, ok := object(row.raw)
			if !ok {
				p.badRecord(row.tail)
				c = next
				continue
			}
			it, ok = project(r.Kind, obj, id, c.FileKey)
			if !ok {
				p.badRecord(false)
				c = next
				continue
			}
			if it.Status == "oversized" {
				it.SourceBytes = row.end - row.start
			}
		}
		if !p.add(it) {
			return c, false, nil
		}
		c = next
	}
	return c, true, nil
}

// Legacy arrays are intentionally bounded full reads, with content and metadata
// revisions. They never migrate or modify an existing notification file.
func (s *Service) readLegacy(r request, p *page, day dayFile, budget *int) (bool, *Error) {
	snap, e := s.validate(r.cursor, day)
	if e != nil {
		return false, e
	}
	defer snap.file.Close()
	if snap.info.Size() > ScanLimit {
		return false, tooLarge()
	}
	if snap.info.Size() > int64(*budget) {
		p.warn("SCAN_BUDGET_REACHED")
		s.next(p, r.cursor)
		return false, nil
	}
	raw, err := io.ReadAll(io.LimitReader(snap.file, snap.info.Size()))
	*budget -= len(raw)
	if err != nil || int64(len(raw)) != snap.info.Size() {
		return false, readFailed()
	}
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil || rows == nil {
		return false, readFailed()
	}
	c := r.cursor
	revision := digest(snap.id, string(raw))
	if c.NextIndex >= 0 && c.IndexRevision != revision {
		return false, expired()
	}
	c.IndexRevision = revision
	at := c.NextIndex
	if at == -1 {
		at = len(rows) - 1
	}
	for at >= 0 {
		if len(p.Items) >= r.limit {
			break
		}
		obj, ok := object(rows[at])
		if !ok {
			p.badRecord(false)
			at--
			continue
		}
		it, ok := project(r.Kind, obj, digest(s.profile, r.Kind, day.date, revision, fmt.Sprint(at)), day.date)
		if !ok {
			p.badRecord(false)
			at--
			continue
		}
		if !p.add(it) {
			break
		}
		at--
	}
	if at >= 0 {
		c.NextIndex = at
		s.next(p, c)
		return false, nil
	}
	return true, nil
}
