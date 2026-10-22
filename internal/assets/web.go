package assets

import (
	"context"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

type webEntry struct {
	fields    map[string]any
	url       string
	instant   time.Time
	validTime bool
}

func (s *Service) readWeb(ctx context.Context, r request, p *page) *Error {
	if r.resume && r.Mode != "web_index" {
		return expired()
	}
	var result *Error
	err := s.sources[r.Kind].view(func() error {
		result = s.webView(ctx, r, p)
		return nil
	})
	if err != nil {
		return readFailed()
	}
	return result
}

// Hold the existing writer lock while reading the index; never open page bodies.
// No session cache: a bounded cold index read can rebuild any valid cursor.
func (s *Service) webView(ctx context.Context, r request, p *page) *Error {
	root, err := sourceRoot(s.sources[r.Kind])
	if os.IsNotExist(err) {
		if r.resume {
			return expired()
		}
		return nil
	}
	if err != nil {
		return readFailed()
	}
	defer root.Close()
	raw, missing, e := readIndex(root)
	if e != nil {
		return e
	}
	if missing {
		if r.resume {
			return expired()
		}
		return nil
	}
	revision := digest(string(raw))
	if r.resume && r.IndexRevision != revision {
		return expired()
	}
	obj, ok := object(raw)
	if !ok {
		return readFailed()
	}
	all, ok := obj["pages"].([]any)
	if !ok {
		return readFailed()
	}
	entries := make([]webEntry, 0, len(all))
	for _, v := range all {
		m, ok := v.(map[string]any)
		if !ok {
			return readFailed()
		}
		link := str(m, "url")
		if strings.TrimSpace(link) == "" {
			// Older index entries may only contain the canonical URL.
			link = str(m, "canonicalUrl")
		}
		if str(m, "urlHash") == "" || strings.TrimSpace(link) == "" {
			return readFailed()
		}
		at, err := time.Parse(time.RFC3339Nano, str(m, "capturedAt"))
		entries = append(entries, webEntry{fields: m, url: link, instant: at, validTime: err == nil})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.validTime != b.validTime {
			return a.validTime
		}
		if a.validTime && !a.instant.Equal(b.instant) {
			return a.instant.After(b.instant)
		}
		return str(a.fields, "urlHash") > str(b.fields, "urlHash")
	})
	c := r.cursor
	c.Mode = "web_index"
	c.IndexRevision = revision
	if c.NextIndex < 0 || c.NextIndex > len(entries) {
		return expired()
	}
	for at := c.NextIndex; at < len(entries); at++ {
		if ctx.Err() != nil {
			return readFailed()
		}
		if len(p.Items) >= r.limit {
			s.next(p, c)
			return nil
		}
		m := entries[at].fields
		d := map[string]any{"title": str(m, "title"), "url": entries[at].url}
		it := item(digest(s.profile, r.Kind, str(m, "urlHash")), "", d)
		boundItem(&it, map[string]int{"title": 512}, ItemLimit)
		if !p.add(it) {
			s.next(p, c)
			return nil
		}
		c.NextIndex = at + 1
	}
	return nil
}

func readIndex(root *os.Root) ([]byte, bool, *Error) {
	info, e := root.Lstat("index.json")
	if os.IsNotExist(e) {
		return nil, true, nil
	}
	if e != nil || !info.Mode().IsRegular() {
		return nil, false, readFailed()
	}
	if info.Size() > IndexLimit {
		return nil, false, tooLarge()
	}
	f, e := root.Open("index.json")
	if e != nil {
		return nil, false, readFailed()
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, IndexLimit+1))
	if e != nil {
		return nil, false, readFailed()
	}
	if len(raw) > IndexLimit {
		return nil, false, tooLarge()
	}
	return raw, false, nil
}
