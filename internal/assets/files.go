package assets

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type dayFile struct{ date, name string }
type snapshot struct {
	file *os.File
	info os.FileInfo
	id   string
}

func sourceRoot(src Source) (*os.Root, error) {
	info, err := os.Lstat(src.Dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("asset root is not a directory")
	}
	root, err := os.OpenRoot(src.Dir)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		root.Close()
		return nil, errors.New("asset root changed")
	}
	return root, nil
}

func dates(src Source, legacy bool, c cursor) ([]dayFile, *Error) {
	root, e := sourceRoot(src)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, readFailed()
	}
	defer root.Close()
	dir, e := root.Open(".")
	if e != nil {
		return nil, readFailed()
	}
	defer dir.Close()
	byDate := map[string]string{}
	for {
		entries, err := dir.ReadDir(256)
		if err != nil && err != io.EOF {
			return nil, readFailed()
		}
		for _, entry := range entries {
			name := entry.Name()
			if len(name) < 10 || !validDate(name[:10]) {
				continue
			}
			ext := name[10:]
			if ext != ".jsonl" && !(legacy && ext == ".json") {
				continue
			}
			day := name[:10]
			if c.Kind == "calendars" {
				if c.forward() && day < c.Query["anchorDate"] || !c.forward() && day > c.Query["anchorDate"] {
					continue
				}
			}
			if byDate[day] == "" || ext == ".jsonl" {
				byDate[day] = name
			}
		}
		if err == io.EOF {
			break
		}
	}
	out := make([]dayFile, 0, len(byDate))
	for date, name := range byDate {
		out = append(out, dayFile{date, name})
	}
	sort.Slice(out, func(i, j int) bool {
		if c.forward() {
			return out[i].date < out[j].date
		}
		return out[i].date > out[j].date
	})
	return out, nil
}

// fileID is append-stable and portable (os.SameFile works on Windows too).
// The bounded registry stores file identities, never client pagination sessions.
// Eviction expires old cursors instead of ever interpreting a stale offset.
func (s *Service) fileID(key string, info os.FileInfo) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	old, ok := s.files[key]
	if !ok || !os.SameFile(old.info, info) || info.Size() < old.info.Size() {
		old = fileIdentity{id: digest(s.boot, key, fmt.Sprint(s.sequence))}
	}
	old.info = info
	old.touched = s.sequence
	if !ok && len(s.files) >= 4096 {
		var oldest string
		seq := s.sequence
		for k, v := range s.files {
			if v.touched < seq {
				seq = v.touched
				oldest = k
			}
		}
		delete(s.files, oldest)
	}
	s.files[key] = old
	return old.id
}

func (s *Service) open(kind string, day dayFile) (snapshot, *Error) {
	var snap snapshot
	src := s.sources[kind]
	err := src.view(func() error {
		root, e := sourceRoot(src)
		if e != nil {
			return e
		}
		defer root.Close()
		// Date files must be regular files, never aliases to a different date.
		info, e := root.Lstat(day.name)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errors.New("non-regular date file")
		}
		f, e := root.Open(day.name)
		if e != nil {
			return e
		}
		actual, e := f.Stat()
		if e != nil || !os.SameFile(info, actual) {
			f.Close()
			return errors.New("source changed while opening")
		}
		snap = snapshot{file: f, info: actual, id: s.fileID(kind+"/"+day.name, actual)}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return snap, expired()
		}
		return snap, readFailed()
	}
	return snap, nil
}

func (s *Service) enter(c cursor, day dayFile) (cursor, *Error) {
	snap, e := s.open(c.Kind, day)
	if e != nil {
		return c, e
	}
	defer snap.file.Close()
	c.FileKey = day.date
	c.FileID = snap.id
	c.SnapshotEnd = snap.info.Size()
	c.SkipLine = nil
	c.NextIndex = 0
	c.IndexRevision = ""
	c.AfterByte = 0
	c.BeforeByte = c.SnapshotEnd
	c.Mode = "jsonl_reverse"
	if c.forward() {
		c.Mode = "jsonl_forward"
	}
	if strings.HasSuffix(day.name, ".json") {
		c.Mode = "legacy_array"
		c.FileVersion = metadataRevision(snap.info)
		// -1 is the unvisited end of a legacy array.
		c.NextIndex = -1
	}
	return c, nil
}

func metadataRevision(info os.FileInfo) string {
	return digest(fmt.Sprint(info.Size()), info.ModTime().UTC().String())
}

func (s *Service) validate(c cursor, day dayFile) (snapshot, *Error) {
	snap, e := s.open(c.Kind, day)
	if e != nil {
		return snap, e
	}
	if snap.id != c.FileID || snap.info.Size() < c.SnapshotEnd || c.position() < 0 || c.position() > c.SnapshotEnd ||
		c.Mode == "legacy_array" && metadataRevision(snap.info) != c.FileVersion {
		snap.file.Close()
		return snapshot{}, expired()
	}
	return snap, nil
}

func (s *Service) advance(p *page, c cursor, files []dayFile, index int) *Error {
	if index+1 >= len(files) {
		p.NextCursor = nil
		return nil
	}
	next, e := s.enter(c, files[index+1])
	if e != nil {
		return e
	}
	s.next(p, next)
	return nil
}
