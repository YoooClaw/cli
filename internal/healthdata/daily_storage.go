package healthdata

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

func (s *Storage) replaceDaily(b *DailyBatch) error {
	if err := fsutil.EnsureDir(s.dir, fsutil.DirMode); err != nil {
		return err
	}
	if err := s.ensureDailyReadme(); err != nil {
		return err
	}
	if err := s.archiveLegacyRoot(); err != nil {
		return err
	}
	collected, _ := time.Parse(time.RFC3339Nano, b.CollectedAt)
	for _, day := range b.Days {
		path := filepath.Join(s.dir, day.Date+".jsonl")
		prior, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if len(prior) > 0 {
			var header dailyHeader
			scan := bufio.NewScanner(bytes.NewReader(prior))
			scan.Buffer(make([]byte, 1024), len(prior)+1)
			if scan.Scan() && json.Unmarshal(scan.Bytes(), &header) == nil && header.SchemaVersion == 2 && header.Date == day.Date {
				if previous, e := time.Parse(time.RFC3339Nano, header.UpdatedAt); e == nil && previous.After(collected) {
					continue
				}
			}
		}
		header := dailyHeader{SchemaVersion: 2, Date: day.Date, TimeZone: b.TimeZone, UpdatedAt: b.CollectedAt, Provider: b.Provider}
		var out bytes.Buffer
		encoder := json.NewEncoder(&out)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(header); err != nil {
			return err
		}
		metrics := append([]json.RawMessage(nil), day.Items...)
		sort.Slice(metrics, func(i, j int) bool {
			var a, c struct {
				Metric string `json:"metric"`
			}
			_ = json.Unmarshal(metrics[i], &a)
			_ = json.Unmarshal(metrics[j], &c)
			return a.Metric < c.Metric
		})
		for _, item := range metrics {
			var compact bytes.Buffer
			if err := json.Compact(&compact, item); err != nil {
				return err
			}
			out.Write(compact.Bytes())
			out.WriteByte('\n')
		}
		if err := fsutil.WriteAtomic(path, out.Bytes(), fsutil.SecretFileMode); err != nil {
			return err
		}
	}
	return nil
}

// Preserve a legacy receipt-day file before replacing the colliding date with a daily snapshot.
// Copy first, then atomically replace; a failed write leaves the original intact.
func (s *Storage) preserveLegacy(date string, raw []byte) error {
	dir := filepath.Join(s.dir, "legacy", "imported")
	if err := fsutil.EnsureDir(dir, fsutil.DirMode); err != nil {
		return err
	}
	path := filepath.Join(dir, date+".jsonl")
	if old, err := os.ReadFile(path); err == nil {
		if bytes.Equal(old, raw) {
			return nil
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.jsonl", date, time.Now().UnixNano()))
	} else if !os.IsNotExist(err) {
		return err
	}
	return fsutil.WriteAtomic(path, raw, fsutil.SecretFileMode)
}

// Keep raw receipt-day batches out of the daily reader's root, including files whose
// receipt date is outside the new snapshot window. Content is copied before removal.
func (s *Storage) archiveLegacyRoot() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) != len("2006-01-02.jsonl") {
			continue
		}
		date := name[:10]
		if _, err := time.Parse("2006-01-02", date); err != nil || name[10:] != ".jsonl" {
			continue
		}
		path := filepath.Join(s.dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var header dailyHeader
		first := bytes.SplitN(raw, []byte{'\n'}, 2)[0]
		if json.Unmarshal(first, &header) == nil && header.SchemaVersion == 2 && header.Date == date {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(first, &fields) != nil || fields["items"] == nil {
			return fmt.Errorf("health date file %s has an unrecognized format; original preserved", name)
		}
		if version, exists := fields["schemaVersion"]; exists && string(bytes.TrimSpace(version)) != "1" {
			return fmt.Errorf("health date file %s has an unsupported version; original preserved", name)
		}
		if err := s.preserveLegacy(date, raw); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}
