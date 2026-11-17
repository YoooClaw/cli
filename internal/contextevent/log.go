// Package contextevent records bounded, replayable invalidations of committed context.
// Data and this journal are deliberately not one transaction; consumers must reconcile.
package contextevent

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

const Name = "context.updated"
const MaxRecord = 32 << 10

var ErrExpired = errors.New("event cursor expired")
var ErrCursor = errors.New("invalid event cursor")

type Scope struct {
	Kind        string `json:"kind"`
	ClientLabel string `json:"clientLabel,omitempty"`
}
type Result struct {
	CommittedCount int    `json:"committedCount"`
	Partial        bool   `json:"partial"`
	Unit           string `json:"unit,omitempty"`
}

// Details locates committed data without carrying private content.
// Revision is an opaque commit fingerprint, not a historical read token.
type Details struct {
	ReadScope      string   `json:"readScope,omitempty"`
	ChangedDates   []string `json:"changedDates,omitempty"`
	DatesTruncated bool     `json:"datesTruncated,omitempty"`
	Revision       string   `json:"revision,omitempty"`
}

type Event struct {
	Details
	SchemaVersion      int       `json:"schemaVersion"`
	EventID            string    `json:"eventId"`
	Cursor             string    `json:"cursor"`
	OccurredAt         time.Time `json:"occurredAt"`
	Source             string    `json:"source"`
	Operation          string    `json:"operation"`
	Scope              Scope     `json:"scope"`
	Result             Result    `json:"result"`
	ResourceIDs        []string  `json:"resourceIds,omitempty"`
	ResourcesTruncated bool      `json:"resourcesTruncated,omitempty"`
}
type Page struct {
	Events        []Event `json:"events"`
	NextCursor    string  `json:"nextCursor"`
	HasMore       bool    `json:"hasMore"`
	HighWatermark string  `json:"highWatermark"`
}
type Options struct {
	MaxBytes     int64
	SegmentBytes int64
	Retention    time.Duration
	Now          func() time.Time
	Warn         func(string)
}
type metadata struct {
	Epoch string `json:"epoch"`
	Next  uint64 `json:"next"`
	Floor uint64 `json:"floor"`
}
type entry struct {
	event Event
	seq   uint64
	file  string
}
type segment struct {
	name string
	size int64
	last uint64
	at   time.Time
}
type Log struct {
	mu       sync.Mutex
	dir      string
	opts     Options
	meta     metadata
	entries  []entry
	segments []segment
	wake     chan struct{}
}

func uuid() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func Open(dir string, opts Options) (*Log, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 64 << 20
	}
	if opts.SegmentBytes <= 0 {
		opts.SegmentBytes = 1 << 20
	}
	if opts.Retention <= 0 {
		opts.Retention = 7 * 24 * time.Hour
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	l := &Log{dir: dir, opts: opts, wake: make(chan struct{})}
	if e := fsutil.EnsureDir(dir, 0700); e != nil {
		return nil, e
	}
	exists, e := fsutil.ReadJSON(filepath.Join(dir, "state.json"), &l.meta)
	if e != nil || !exists || l.meta.Epoch == "" || l.meta.Next == 0 {
		if e != nil && opts.Warn != nil {
			opts.Warn("context.event.journal_reset: invalid metadata")
		}
		if e = l.reset(); e != nil {
			return nil, e
		}
	}
	if e = l.load(); e != nil {
		if opts.Warn != nil {
			opts.Warn("context.event.journal_reset: corrupt journal")
		}
		if e = l.reset(); e != nil {
			return nil, e
		}
	}
	if e = l.prune(); e != nil {
		return nil, e
	}
	return l, nil
}
func (l *Log) save() error { return fsutil.WriteJSON(filepath.Join(l.dir, "state.json"), l.meta, 0600) }
func (l *Log) reset() error {
	// Persist the new generation first. An interrupted cleanup is detected again on load.
	l.meta = metadata{Epoch: uuid(), Next: 1}
	if e := l.save(); e != nil {
		return e
	}
	files, e := filepath.Glob(filepath.Join(l.dir, "*.jsonl"))
	if e != nil {
		return e
	}
	for _, p := range files {
		if e = os.Remove(p); e != nil {
			return e
		}
	}
	l.entries = nil
	l.segments = nil
	return nil
}
func (l *Log) cursor(n uint64) string { return l.meta.Epoch + ":" + strconv.FormatUint(n, 10) }
func (l *Log) parse(c string) (uint64, error) {
	parts := strings.Split(c, ":")
	if len(parts) != 2 {
		return 0, ErrCursor
	}
	n, e := strconv.ParseUint(parts[1], 10, 64)
	if e != nil {
		return 0, ErrCursor
	}
	if parts[0] != l.meta.Epoch || n < l.meta.Floor {
		return 0, ErrExpired
	}
	if n >= l.meta.Next {
		return 0, ErrCursor
	}
	return n, nil
}
func (l *Log) load() error {
	files, e := filepath.Glob(filepath.Join(l.dir, "*.jsonl"))
	if e != nil {
		return e
	}
	sort.Strings(files)
	var previous uint64
	for i, p := range files {
		f, e := os.OpenFile(p, os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		reader := bufio.NewReaderSize(f, MaxRecord+1)
		var offset int64
		seg := segment{name: p}
		for {
			line, e := reader.ReadSlice('\n')
			if e == io.EOF {
				if len(line) > 0 {
					if i != len(files)-1 {
						f.Close()
						return errors.New("partial middle segment")
					}
					if e = f.Truncate(offset); e != nil {
						f.Close()
						return e
					}
				}
				break
			}
			if e != nil || len(line) > MaxRecord {
				f.Close()
				return errors.New("invalid record")
			}
			var ev Event
			if json.Unmarshal(line, &ev) != nil || ev.SchemaVersion != 1 || ev.EventID == "" {
				f.Close()
				return errors.New("invalid event")
			}
			// Already pruned files can remain after a crash between metadata and removal.
			parts := strings.Split(ev.Cursor, ":")
			if len(parts) != 2 || parts[0] != l.meta.Epoch {
				f.Close()
				return ErrExpired
			}
			n, er := strconv.ParseUint(parts[1], 10, 64)
			if er != nil || n <= previous || n >= l.meta.Next {
				f.Close()
				return ErrCursor
			}
			previous = n
			offset += int64(len(line))
			seg.last = n
			if seg.at.IsZero() {
				seg.at = ev.OccurredAt
			}
			if n > l.meta.Floor {
				l.entries = append(l.entries, entry{ev, n, p})
			}
		}
		f.Close()
		seg.size = offset
		l.segments = append(l.segments, seg)
	}
	return nil
}
func (l *Log) prune() error {
	var total int64
	for _, s := range l.segments {
		total += s.size
	}
	for len(l.segments) > 0 {
		s := l.segments[0]
		if s.last > l.meta.Floor && total <= l.opts.MaxBytes && s.at.After(l.opts.Now().Add(-l.opts.Retention)) {
			break
		}
		if s.last > l.meta.Floor {
			l.meta.Floor = s.last
			if e := l.save(); e != nil {
				return e
			}
		}
		if e := os.Remove(s.name); e != nil && !os.IsNotExist(e) {
			return e
		}
		total -= s.size
		l.segments = l.segments[1:]
		j := 0
		for j < len(l.entries) && l.entries[j].seq <= l.meta.Floor {
			j++
		}
		l.entries = l.entries[j:]
	}
	return nil
}
func (l *Log) Append(ev Event) (Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.prune(); e != nil {
		return Event{}, e
	}
	// Copy before sorting/capping: callers may reuse their commit result.
	ev.ChangedDates = append([]string(nil), ev.ChangedDates...)
	sort.Strings(ev.ChangedDates)
	dates := ev.ChangedDates[:0]
	for _, date := range ev.ChangedDates {
		if len(dates) == 0 || date != dates[len(dates)-1] {
			dates = append(dates, date)
		}
	}
	ev.ChangedDates = dates
	if len(ev.ChangedDates) > 100 {
		ev.ChangedDates = ev.ChangedDates[:100]
		ev.DatesTruncated = true
	}
	ev.SchemaVersion = 1
	ev.EventID = uuid()
	ev.OccurredAt = l.opts.Now().UTC()
	ev.Cursor = l.cursor(l.meta.Next)
	if len(ev.ResourceIDs) > 100 {
		ev.ResourceIDs = ev.ResourceIDs[:100]
		ev.ResourcesTruncated = true
	}
	raw, e := json.Marshal(ev)
	if e != nil {
		return Event{}, e
	}
	raw = append(raw, '\n')
	if len(raw) > MaxRecord || int64(len(raw)) > l.opts.MaxBytes {
		return Event{}, errors.New("event too large")
	}
	seq := l.meta.Next
	l.meta.Next++
	if e = l.save(); e != nil {
		return Event{}, e
	}
	if len(l.segments) == 0 || l.segments[len(l.segments)-1].size+int64(len(raw)) > l.opts.SegmentBytes {
		l.segments = append(l.segments, segment{name: filepath.Join(l.dir, fmt.Sprintf("%020d.jsonl", seq))})
	}
	seg := &l.segments[len(l.segments)-1]
	f, e := os.OpenFile(seg.name, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return Event{}, e
	}
	// Remove an incomplete prior write before retrying this segment.
	if e = f.Truncate(seg.size); e == nil {
		_, e = f.Seek(seg.size, io.SeekStart)
	}
	if e == nil {
		var n int
		n, e = f.Write(raw)
		if e == nil && n != len(raw) {
			e = io.ErrShortWrite
		}
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return Event{}, e
	}
	seg.size += int64(len(raw))
	seg.last = seq
	if seg.at.IsZero() {
		seg.at = ev.OccurredAt
	}
	l.entries = append(l.entries, entry{ev, seq, seg.name})
	if e = l.prune(); e != nil {
		return Event{}, e
	}
	close(l.wake)
	l.wake = make(chan struct{})
	return ev, nil
}

// Read atomically returns a page and the next wake channel. Wakeups are hints;
// consumers always read from the journal, so no replay/live registration gap exists.
func (l *Log) Read(after string, limit int, scope string) (Page, <-chan struct{}, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := Page{Events: []Event{}, HighWatermark: l.cursor(l.meta.Next - 1)}
	if e := l.prune(); e != nil {
		return p, l.wake, e
	}
	n := l.meta.Floor
	var e error
	if after != "" {
		n, e = l.parse(after)
		if e != nil {
			return p, l.wake, e
		}
	}
	if limit < 1 || limit > 500 {
		return p, l.wake, errors.New("invalid limit")
	}
	p.NextCursor = l.cursor(n)
	start := sort.Search(len(l.entries), func(i int) bool { return l.entries[i].seq > n })
	for _, x := range l.entries[start:] {
		if len(p.Events) == limit {
			p.HasMore = true
			break
		}
		p.NextCursor = l.cursor(x.seq)
		if scope == "" || x.event.Scope.Kind == "shared" || x.event.Scope.ClientLabel == scope {
			p.Events = append(p.Events, x.event)
		}
	}
	if !p.HasMore {
		p.NextCursor = p.HighWatermark
	}
	return p, l.wake, nil
}
