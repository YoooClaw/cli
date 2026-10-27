package reminders

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type Storage struct {
	dir string
	mu  sync.Mutex
}

func NewStorage(dir string) *Storage { return &Storage{dir: dir} }

// View keeps index, sync metadata and the optional detail on one committed view.
// A failed/interrupted multi-file replacement is recovered before any read.
func (s *Storage) View(fn func(Index, State, func(string) (Record, error)) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.recover(); err != nil {
		return err
	}
	idx, state, err := s.load()
	if err != nil {
		return err
	}
	return fn(idx, state, func(id string) (Record, error) {
		for _, entry := range idx.Items {
			if entry.RecordID != id {
				continue
			}
			rows, err := s.records(entry.File)
			if err != nil {
				return Record{}, err
			}
			for _, r := range rows {
				if r.RecordID == id {
					return r, nil
				}
			}
			return Record{}, errors.New("reminder index does not match data")
		}
		return Record{}, ErrNotFound
	})
}

func (s *Storage) Replace(p Payload) (Result, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return Result{}, err
	}
	if _, err := Decode(raw); err != nil {
		return Result{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := Result{OK: true}
	if err := s.recover(); err != nil {
		return result, err
	}
	idx, state, err := s.load()
	if err != nil {
		return result, err
	}
	result.Revision = idx.Revision
	when, err := time.Parse(time.RFC3339Nano, p.CollectedAt)
	if err != nil {
		return result, err
	}
	if previous, ok := state.Sources[p.SourceID]; ok {
		last, e := time.Parse(time.RFC3339Nano, previous.LastCollectedAt)
		if e != nil {
			return result, e
		}
		if !when.After(last) {
			return result, nil
		}
	}
	files := map[string]bool{}
	for _, entry := range idx.Items {
		files[entry.File] = true
	}
	rows := []Record{}
	for file := range files {
		previous, e := s.records(file)
		if e != nil {
			return result, e
		}
		for _, row := range previous {
			if row.SourceID != p.SourceID {
				rows = append(rows, row)
			}
		}
	}
	for _, item := range p.Items {
		rows = append(rows, Record{Item: item, SourceID: p.SourceID, RecordID: RecordID(p.SourceID, item.ID)})
	}
	next, content, err := makeSnapshot(rows)
	if err != nil {
		return result, err
	}
	state.Sources[p.SourceID] = SourceState{LastCollectedAt: p.CollectedAt}
	stateRaw, err := json.Marshal(state)
	if err != nil || len(stateRaw) > 1<<20 {
		return result, errors.New("reminders sync state exceeds limit")
	}
	// A time-only refresh does not rewrite JSONL or expire content cursors.
	_, indexErr := s.read("index.json", 32<<20)
	if idx.Revision == next.Revision && indexErr == nil {
		err = s.write("state.json", stateRaw)
	} else {
		indexRaw, _ := json.Marshal(next)
		content["index.json"] = string(indexRaw)
		content["state.json"] = string(stateRaw)
		remove := []string{}
		for file := range files {
			if _, exists := content[file]; !exists {
				remove = append(remove, file)
			}
		}
		err = s.commit(transaction{Files: content, Remove: remove})
	}
	if err != nil {
		return result, err
	}
	result.Applied, result.Ingested, result.Revision = true, len(p.Items), next.Revision
	return result, nil
}

func makeSnapshot(rows []Record) (Index, map[string]string, error) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].RecordID < rows[j].RecordID })
	idx := Index{SchemaVersion: SchemaVersion, Revision: hash(rows), Items: []Entry{}}
	parts := map[string]*bytes.Buffer{"active.jsonl": {}}
	bytesWritten := 0
	for i, r := range rows {
		if r.RecordID != RecordID(r.SourceID, r.ID) || i > 0 && rows[i-1].RecordID == r.RecordID {
			return idx, nil, errors.New("invalid stored reminder identity")
		}
		file := partition(r)
		if !validPartition(file) {
			return idx, nil, errors.New("invalid reminder partition")
		}
		if parts[file] == nil {
			parts[file] = &bytes.Buffer{}
		}
		raw, err := json.Marshal(r)
		if err != nil || len(raw) > RecordLimit {
			return idx, nil, errors.New("invalid stored reminder")
		}
		bytesWritten += len(raw) + 1
		if bytesWritten > StoreLimit/2 {
			return idx, nil, errors.New("reminders store exceeds limit")
		}
		parts[file].Write(raw)
		parts[file].WriteByte('\n')
		idx.Items = append(idx.Items, Entry{Summary: r.Summary, SourceID: r.SourceID, RecordID: r.RecordID, File: file})
	}
	raw, _ := json.Marshal(idx)
	if len(raw) > 32<<20 {
		return idx, nil, errors.New("reminders index exceeds limit")
	}
	files := map[string]string{}
	for name, b := range parts {
		files[name] = b.String()
	}
	return idx, files, nil
}

func (s *Storage) load() (Index, State, error) {
	idx := Index{SchemaVersion: SchemaVersion, Revision: hash([]Record{}), Items: []Entry{}}
	state := State{SchemaVersion: SchemaVersion, Sources: map[string]SourceState{}}
	raw, err := s.read("state.json", 1<<20)
	stateMissing := os.IsNotExist(err)
	if err == nil {
		var loaded State
		if json.Unmarshal(raw, &loaded) != nil || loaded.SchemaVersion != SchemaVersion || loaded.Sources == nil {
			return idx, state, errors.New("invalid reminders state")
		}
		for source, value := range loaded.Sources {
			if strings.TrimSpace(source) == "" || len(source) > 256 || !timestamp(&value.LastCollectedAt) {
				return idx, state, errors.New("invalid reminder source watermark")
			}
		}
		state = loaded
	} else if !os.IsNotExist(err) {
		return idx, state, err
	}
	raw, err = s.read("index.json", 32<<20)
	if err == nil {
		var loaded Index
		if stateMissing || json.Unmarshal(raw, &loaded) != nil || loaded.SchemaVersion != SchemaVersion || loaded.Items == nil ||
			len(loaded.Revision) != 64 || strings.Trim(loaded.Revision, "0123456789abcdef") != "" {
			return idx, state, errors.New("invalid reminders index")
		}
		seen := map[string]bool{}
		for _, entry := range loaded.Items {
			_, sourceExists := state.Sources[entry.SourceID]
			if !sourceExists || seen[entry.RecordID] || entry.RecordID != RecordID(entry.SourceID, entry.ID) ||
				!validPartition(entry.File) || entry.File != partition(Record{Item: Item{Summary: entry.Summary}}) {
				return idx, state, errors.New("invalid reminders index entry")
			}
			seen[entry.RecordID] = true
		}
		return loaded, state, nil
	}
	if !os.IsNotExist(err) {
		return idx, state, err
	}
	// The index is derived. Rebuild a missing index from current partitions only.
	files, err := s.partitions()
	if err != nil {
		return idx, state, err
	}
	if stateMissing && len(files) > 0 {
		return idx, state, errors.New("missing reminders source watermarks")
	}
	rows := []Record{}
	for _, file := range files {
		r, e := s.records(file)
		if e != nil {
			return idx, state, e
		}
		for _, row := range r {
			if _, exists := state.Sources[row.SourceID]; !exists {
				return idx, state, errors.New("missing reminder source watermark during index rebuild")
			}
		}
		rows = append(rows, r...)
	}
	if len(files) == 0 {
		if len(state.Sources) > 0 {
			return idx, state, errors.New("missing reminders partitions")
		}
		return idx, state, nil
	}
	idx, _, err = makeSnapshot(rows)
	if err == nil {
		raw, _ = json.Marshal(idx)
		err = s.write("index.json", raw)
	}
	return idx, state, err
}

func (s *Storage) records(file string) ([]Record, error) {
	if !validPartition(file) {
		return nil, errors.New("invalid reminder path")
	}
	raw, err := s.read(file, StoreLimit/2)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), RecordLimit+1)
	rows := []Record{}
	for scanner.Scan() {
		var row Record
		if json.Unmarshal(scanner.Bytes(), &row) != nil || row.RecordID != RecordID(row.SourceID, row.ID) || partition(row) != file {
			return nil, errors.New("invalid reminder JSONL")
		}
		rows = append(rows, row)
	}
	return rows, scanner.Err()
}
