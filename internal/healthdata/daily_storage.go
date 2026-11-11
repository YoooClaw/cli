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

func (s *Storage) replaceDaily(b *DailyBatch) ([]DayWriteResult, error) {
	results := make([]DayWriteResult, 0, len(b.Days))
	if err := fsutil.EnsureDir(s.dir, fsutil.DirMode); err != nil {
		return results, err
	}
	if err := s.ensureDailyReadme(); err != nil {
		return results, err
	}
	collected, _ := time.Parse(time.RFC3339Nano, b.CollectedAt)
	for _, day := range b.Days {
		path := filepath.Join(s.dir, day.Date+".jsonl")
		prior, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return results, err
		}
		if len(prior) > 0 {
			var priorHeader struct {
				SchemaVersion int `json:"schemaVersion"`
			}
			first := bytes.SplitN(bytes.TrimSpace(prior), []byte{'\n'}, 2)[0]
			if json.Unmarshal(first, &priorHeader) != nil || priorHeader.SchemaVersion > 3 || priorHeader.SchemaVersion < 1 {
				return results, fmt.Errorf("unrecognized health file at %s", day.Date)
			}
			var header dailyHeader
			scan := bufio.NewScanner(bytes.NewReader(prior))
			scan.Buffer(make([]byte, 1024), len(prior)+1)
			if scan.Scan() && json.Unmarshal(scan.Bytes(), &header) == nil && header.SchemaVersion == 3 && header.Date == day.Date {
				// Apple owns the complete day once present. All other providers
				// share one priority class, including Android and Harmony vendors.
				priorApple, incomingApple := isAppleProvider(header.Provider), isAppleProvider(b.Provider)
				if priorApple && !incomingApple {
					results = append(results, DayWriteResult{Date: day.Date, Reason: "apple_priority"})
					continue
				}
				if priorApple == incomingApple {
					if previous, e := time.Parse(time.RFC3339Nano, header.UpdatedAt); e == nil && previous.After(collected) {
						results = append(results, DayWriteResult{Date: day.Date, Reason: "stale_collected_at"})
						continue
					}
				}
			}
		}
		header := dailyHeader{SchemaVersion: 3, Date: day.Date, TimeZone: b.TimeZone, UpdatedAt: b.CollectedAt, Provider: b.Provider}
		var out bytes.Buffer
		encoder := json.NewEncoder(&out)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(header); err != nil {
			return results, err
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
				return results, err
			}
			out.Write(compact.Bytes())
			out.WriteByte('\n')
		}
		for _, session := range day.SleepSessions {
			if err := encoder.Encode(struct {
				Type    string       `json:"type"`
				Session SleepSession `json:"session"`
			}{"sleepSession", session}); err != nil {
				return results, err
			}
		}
		details := []struct {
			Type  string
			Value any
		}{
			{"medications", day.Medications}, {"workouts", day.Workouts}, {"intakeRecords", day.IntakeRecords}, {"moods", day.Moods},
		}
		for _, detail := range details {
			encoded, err := json.Marshal(detail.Value)
			if err != nil {
				return results, err
			}
			if string(encoded) == "null" || string(encoded) == "[]" {
				continue
			}
			if err := encoder.Encode(struct {
				Type    string          `json:"type"`
				Records json.RawMessage `json:"records"`
			}{detail.Type, encoded}); err != nil {
				return results, err
			}
		}
		if out.Len() > MaxDayBytes {
			return results, fmt.Errorf("health day exceeds 4 MiB")
		}
		if err := fsutil.WriteAtomic(path, out.Bytes(), fsutil.SecretFileMode); err != nil {
			return results, err
		}
		results = append(results, DayWriteResult{Date: day.Date, Applied: true})
	}
	return results, nil
}
