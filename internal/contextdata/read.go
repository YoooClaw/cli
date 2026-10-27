// Package contextdata reads synchronized source facts without advancing consumption.
package contextdata

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/YoooClaw/cli/internal/healthdata"
	"github.com/YoooClaw/cli/internal/paths"
)

type Item struct {
	ID      string         `json:"id"`
	Date    string         `json:"date"`
	Version string         `json:"version"`
	Data    map[string]any `json:"data"`
	Ref     string         `json:"ref"`
}
type Dataset struct {
	Items     []Item   `json:"items"`
	Dates     []string `json:"availableDates"`
	Connected bool     `json:"connected"`
	TimeBasis string   `json:"timeBasis"`
}

func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func Date(s string) bool {
	t, e := time.Parse(time.DateOnly, s)
	return e == nil && t.Format(time.DateOnly) == s
}
func Dir(p paths.Paths, source string) (string, error) {
	switch source {
	case "reminders":
		return p.Reminders, nil
	case "calendars":
		return p.Calendars, nil
	case "locations":
		return p.Locations, nil
	case "health-data":
		return p.HealthData, nil
	case "web-pages":
		return p.WebPages, nil
	}
	return "", fmt.Errorf("unsupported source %q", source)
}
func readJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("invalid trailing JSON in %s", path)
	}
	return nil
}
func rows(path string) ([]map[string]any, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	out := []map[string]any{}
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 2<<20)
	for s.Scan() {
		if len(bytes.TrimSpace(s.Bytes())) == 0 {
			continue
		}
		var row map[string]any
		d := json.NewDecoder(bytes.NewReader(s.Bytes()))
		d.UseNumber()
		if !json.Valid(s.Bytes()) {
			return nil, fmt.Errorf("invalid JSON in %s", path)
		}
		if e = d.Decode(&row); e != nil || row == nil {
			return nil, fmt.Errorf("invalid JSON object in %s", path)
		}
		out = append(out, row)
	}
	return out, s.Err()
}

// Read fails closed on malformed or missing referenced material. Missing source roots
// are reported separately from a connected source containing no records.
func Read(p paths.Paths, source string) (Dataset, error) {
	out := Dataset{Items: []Item{}, Dates: []string{}}
	dir, e := Dir(p, source)
	if e != nil {
		return out, e
	}
	entries, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	out.Connected = true
	if source == "web-pages" {
		return readPages(dir, out)
	}
	out.TimeBasis = "source-local calendar date"
	if source == "locations" {
		out.TimeBasis = "receipt date; timestamp is source observation time (legacy collectedAt); source time is unvalidated"
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") || !Date(strings.TrimSuffix(name, ".jsonl")) {
			continue
		}
		date := strings.TrimSuffix(name, ".jsonl")
		path := filepath.Join(dir, name)
		if source == "health-data" {
			info, err := os.Lstat(path)
			if err != nil {
				return out, err
			}
			if !info.Mode().IsRegular() {
				return out, fmt.Errorf("health snapshot must be a regular file: %s", date)
			}
			if info.Size() > healthdata.MaxDayBytes {
				return out, fmt.Errorf("health snapshot exceeds 4 MiB: %s", date)
			}
			file, err := os.Open(path)
			if err != nil {
				return out, err
			}
			raw, err := io.ReadAll(io.LimitReader(file, healthdata.MaxDayBytes+1))
			file.Close()
			if err != nil {
				return out, err
			}
			if len(raw) > healthdata.MaxDayBytes {
				return out, fmt.Errorf("health snapshot exceeds 4 MiB: %s", date)
			}
			var header map[string]any
			first := bytes.SplitN(bytes.TrimSpace(raw), []byte{'\n'}, 2)[0]
			if err := json.Unmarshal(first, &header); err != nil {
				return out, err
			}
			day, _, err := healthdata.ReadSnapshot(raw, date)
			if err != nil {
				return out, fmt.Errorf("health snapshot %s: %w", date, err)
			}
			metrics := []map[string]any{}
			for _, item := range day.Items {
				var metric map[string]any
				decoder := json.NewDecoder(bytes.NewReader(item))
				decoder.UseNumber()
				if err := decoder.Decode(&metric); err != nil {
					return out, err
				}
				metrics = append(metrics, metric)
			}
			sort.Slice(metrics, func(i, j int) bool { return Str(metrics[i], "metric") < Str(metrics[j], "metric") })
			data := map[string]any{"header": header, "items": metrics, "sleepSessions": day.SleepSessions}
			version := Hash(map[string]any{"date": date, "provider": header["provider"], "timeZone": header["timeZone"], "items": metrics, "sleepSessions": day.SleepSessions})
			out.Items = append(out.Items, Item{ID: date, Date: date, Version: version, Data: data, Ref: path})
			out.Dates = append(out.Dates, date)
			continue
		}
		rs, e := rows(path)
		if e != nil {
			return out, e
		}
		out.Dates = append(out.Dates, date)
		for _, r := range rs {
			identity := Hash(r)
			if source == "calendars" {
				// Exact copies across date buckets are one event; preserve differing records.
				if seen[identity] {
					continue
				}
				seen[identity] = true
				ts, err := calendarStart(r)
				if err != nil {
					return out, fmt.Errorf("invalid calendar startAt in %s", path)
				}
				zone, err := time.LoadLocation(Str(r, "timeZone"))
				if err != nil {
					return out, err
				}
				date = ts.In(zone).Format(time.DateOnly)
			}
			id := identity
			if source == "locations" {
				id = strings.TrimSuffix(name, ".jsonl") + ":" + identity
				if seen[id] {
					continue
				}
				seen[id] = true
			}
			out.Items = append(out.Items, Item{ID: id, Date: date, Version: Hash(r), Data: r, Ref: path})
		}
	}
	sort.Strings(out.Dates)
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if source == "calendars" {
			ta, _ := calendarStart(a.Data)
			tb, _ := calendarStart(b.Data)
			if !ta.Equal(tb) {
				return ta.Before(tb)
			}
		}
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		return a.ID < b.ID
	})
	return out, nil
}
func readPages(dir string, out Dataset) (Dataset, error) {
	out.TimeBasis = "latest capturedAt date in its supplied offset"
	var idx struct {
		Pages []map[string]any `json:"pages"`
	}
	if e := readJSON(filepath.Join(dir, "index.json"), &idx); e != nil {
		return out, e
	}
	dates := map[string]bool{}
	for _, p := range idx.Pages {
		id := Str(p, "urlHash")
		rel := Str(p, "relativePath")
		if id == "" || rel == "" {
			return out, fmt.Errorf("incomplete webpage index")
		}
		root, e := filepath.EvalSymlinks(dir)
		if e != nil {
			return out, e
		}
		path, e := filepath.EvalSymlinks(filepath.Join(dir, rel))
		if e != nil {
			return out, e
		}
		safe, e := filepath.Rel(root, path)
		if e != nil || filepath.IsAbs(safe) || safe == ".." || strings.HasPrefix(safe, ".."+string(os.PathSeparator)) {
			return out, fmt.Errorf("webpage path escapes source")
		}
		info, e := os.Stat(path)
		if e != nil {
			return out, e
		}
		if info.Size() > 2<<20 {
			return out, fmt.Errorf("webpage exceeds read limit")
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return out, e
		}
		ts, e := time.Parse(time.RFC3339Nano, Str(p, "capturedAt"))
		if e != nil {
			return out, e
		}
		date := ts.Format(time.DateOnly)
		// Strip generated frontmatter from the version hash so timestamp-only resyncs
		// do not become new content. Retain complete source text for attribution.
		body := string(raw)
		if strings.HasPrefix(body, "---\n") {
			if n := strings.Index(body[4:], "\n---\n"); n >= 0 {
				body = body[4+n+5:]
			}
		}
		data := map[string]any{"metadata": p, "markdown": body}
		version := Hash(map[string]any{"url": p["canonicalUrl"], "title": p["title"], "markdown": body})
		out.Items = append(out.Items, Item{ID: id, Date: date, Version: version, Data: data, Ref: path})
		dates[date] = true
	}
	for d := range dates {
		out.Dates = append(out.Dates, d)
	}
	sort.Strings(out.Dates)
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ID < out.Items[j].ID })
	return out, nil
}

// calendarStart uses the source's current field, with the older optional spelling
// as a fallback. An explicit false is authoritative when both are present.
func calendarStart(record map[string]any) (time.Time, error) {
	start, err := time.Parse(time.RFC3339Nano, Str(record, "startAt"))
	if err != nil {
		return time.Time{}, err
	}
	allDay, present := record["isAllDay"].(bool)
	if !present {
		allDay, _ = record["allDay"].(bool)
	}
	if allDay {
		zone, err := time.LoadLocation(Str(record, "timeZone"))
		if err != nil {
			return time.Time{}, err
		}
		local := start.In(zone)
		return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone), nil
	}
	return start, nil
}

// Summaries keep default queries and memory batches independent of timeline size.
// Version still covers all segments, so timeline corrections remain consumable.
func healthSummary(data map[string]any) map[string]any {
	out := make(map[string]any, len(data))
	for k, v := range data {
		out[k] = v
	}
	summaries := []healthdata.SleepListSummary{}
	for _, session := range data["sleepSessions"].([]healthdata.SleepSession) {
		summaries = append(summaries, session.Summary())
	}
	out["sleepSessions"] = summaries
	out["sleepDetailsHint"] = "health-data get --date YYYY-MM-DD returns complete segments; use the returned date"
	return out
}
