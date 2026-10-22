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
		out.TimeBasis = "receipt date; collectedAt is unvalidated source metadata"
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") || !Date(strings.TrimSuffix(name, ".jsonl")) {
			continue
		}
		date := strings.TrimSuffix(name, ".jsonl")
		path := filepath.Join(dir, name)
		rs, e := rows(path)
		if e != nil {
			return out, e
		}
		out.Dates = append(out.Dates, date)
		if source == "health-data" {
			if len(rs) < 2 {
				return out, fmt.Errorf("empty health snapshot %s", date)
			}
			h := rs[0]
			if fmt.Sprint(h["schemaVersion"]) != "2" || Str(h, "date") != date {
				return out, fmt.Errorf("unsupported health snapshot %s", date)
			}
			if _, err := time.Parse(time.RFC3339Nano, Str(h, "updatedAt")); err != nil {
				return out, fmt.Errorf("invalid health updatedAt in %s", path)
			}
			if _, err := time.LoadLocation(Str(h, "timeZone")); err != nil {
				return out, err
			}
			metrics := rs[1:]
			sort.Slice(metrics, func(i, j int) bool { return Str(metrics[i], "metric") < Str(metrics[j], "metric") })
			for i, m := range metrics {
				if Str(m, "metric") == "" || Str(m, "unit") == "" {
					return out, fmt.Errorf("invalid health metric in %s", path)
				}
				if _, ok := m["value"].(json.Number); !ok {
					return out, fmt.Errorf("invalid health value in %s", path)
				}
				if i > 0 && Str(m, "metric") == Str(metrics[i-1], "metric") {
					return out, fmt.Errorf("duplicate health metric in %s", path)
				}
			}
			versionData := map[string]any{"date": date, "provider": h["provider"], "timeZone": h["timeZone"], "items": rs[1:]}
			data := map[string]any{"header": h, "items": rs[1:]}
			out.Items = append(out.Items, Item{ID: date, Date: date, Version: Hash(versionData), Data: data, Ref: path})
			continue
		}
		for _, r := range rs {
			identity := Hash(r)
			if source == "calendars" {
				// Exact copies across date buckets are one event; preserve differing records.
				if seen[identity] {
					continue
				}
				seen[identity] = true
				ts, err := time.Parse(time.RFC3339Nano, Str(r, "startAt"))
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
			ta, _ := time.Parse(time.RFC3339Nano, Str(a.Data, "startAt"))
			tb, _ := time.Parse(time.RFC3339Nano, Str(b.Data, "startAt"))
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
