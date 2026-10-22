package contextdata

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
)

type Change struct {
	Item
	PreviousVersion string         `json:"previousVersion,omitempty"`
	PreviousData    map[string]any `json:"previousData,omitempty"`
}
type Batch struct {
	Token     string   `json:"token"`
	Scope     Options  `json:"scope"`
	Items     []Change `json:"items"`
	Remaining int      `json:"remainingInScope"`
	Outside   int      `json:"outsideScopePending"`
}
type memoryState struct {
	Seen       map[string]Item `json:"seen"`
	Pending    *Batch          `json:"pending,omitempty"`
	LastCommit string          `json:"lastCommit,omitempty"`
}

func memoryItems(p paths.Paths, source string) (Dataset, error) {
	d, e := Read(p, source)
	if e != nil || source != "calendars" {
		return d, e
	}
	// A calendar date file is a complete snapshot, including an empty day. Preserve
	// the previous day's contents so removals remain attributable to this scope.
	d.Items = []Item{}
	for _, date := range d.Dates {
		path := filepath.Join(p.Calendars, date+".jsonl")
		rs, e := rows(path)
		if e != nil {
			return d, e
		}
		sort.Slice(rs, func(i, j int) bool { return Hash(rs[i]) < Hash(rs[j]) })
		data := map[string]any{"date": date, "events": rs, "meaning": "complete snapshot for this synchronized day; absence is source removal, not proof of cancellation"}
		d.Items = append(d.Items, Item{ID: date, Date: date, Version: Hash(data), Data: data, Ref: path})
	}
	return d, nil
}
func statePath(p paths.Paths, source string) (string, error) {
	if _, e := Dir(p, source); e != nil {
		return "", e
	}
	return filepath.Join(p.State, "memory-sync", source+".json"), nil
}
func loadState(path string) (memoryState, error) {
	s := memoryState{Seen: map[string]Item{}}
	e := readJSON(path, &s)
	if os.IsNotExist(e) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	if s.Seen == nil {
		return s, fmt.Errorf("invalid memory checkpoint")
	}
	return s, nil
}
func sameScope(a, b Options) bool {
	return a.All == b.All && a.Date == b.Date && a.From == b.From && a.To == b.To
}
func batchResult(source string, b *Batch) map[string]any {
	return map[string]any{"ok": true, "source": source, "done": false, "token": b.Token, "scope": b.Scope, "items": b.Items, "returned": len(b.Items), "remainingInScope": b.Remaining, "outsideScopePending": b.Outside, "commitArgs": []string{"sync", "commit", "--source", source, "--token", b.Token}}
}

// Next persists the exact fetched material. Retries return the same batch until
// commit; updates arriving during reasoning cannot be accidentally acknowledged.
func Next(p paths.Paths, source string, o Options, scan bool) (map[string]any, error) {
	if e := o.Validate(); e != nil {
		return nil, e
	}
	path, e := statePath(p, source)
	if e != nil {
		return nil, e
	}
	if e = fsutil.EnsureDir(filepath.Dir(path), fsutil.DirMode); e != nil {
		return nil, e
	}
	release, e := lock(path + ".lock")
	if e != nil {
		return nil, e
	}
	defer release()
	s, e := loadState(path)
	if e != nil {
		return nil, e
	}
	if s.Pending != nil {
		if !sameScope(o, s.Pending.Scope) {
			return nil, fmt.Errorf("pending batch uses another scope; resume its scope before starting a new one")
		}
		return batchResult(source, s.Pending), nil
	}
	d, e := memoryItems(p, source)
	if e != nil {
		return nil, e
	}
	if !d.Connected {
		return nil, fmt.Errorf("source %s is not connected", source)
	}
	pending := []Change{}
	outside := 0
	for _, item := range d.Items {
		old := s.Seen[item.ID]
		if old.Version == item.Version {
			continue
		}
		if !o.Includes(item.Date) {
			outside++
			continue
		}
		change := Change{Item: item, PreviousVersion: old.Version}
		if source == "calendars" || source == "health-data" {
			change.PreviousData = old.Data
		}
		pending = append(pending, change)
	}
	if len(pending) == 0 || scan {
		return map[string]any{"ok": true, "source": source, "done": len(pending) == 0, "scope": o, "totalPending": len(pending), "outsideScopePending": outside}, nil
	}
	n, bytes := 0, 0
	for n < len(pending) && n < 100 {
		raw, _ := json.Marshal(pending[n])
		if n > 0 && bytes+len(raw) > 64000 {
			break
		}
		bytes += len(raw)
		n++
	}
	token := make([]byte, 24)
	if _, e = rand.Read(token); e != nil {
		return nil, e
	}
	b := &Batch{Token: hex.EncodeToString(token), Scope: o, Items: pending[:n], Remaining: len(pending) - n, Outside: outside}
	s.Pending = b
	if e = fsutil.WriteJSON(path, s, fsutil.SecretFileMode); e != nil {
		return nil, e
	}
	return batchResult(source, b), nil
}
func Commit(p paths.Paths, source, token string) (map[string]any, error) {
	decoded, e := hex.DecodeString(token)
	if e != nil || len(decoded) != 24 {
		return nil, fmt.Errorf("invalid batch token")
	}
	path, e := statePath(p, source)
	if e != nil {
		return nil, e
	}
	if e = fsutil.EnsureDir(filepath.Dir(path), fsutil.DirMode); e != nil {
		return nil, e
	}
	release, e := lock(path + ".lock")
	if e != nil {
		return nil, e
	}
	defer release()
	s, e := loadState(path)
	if e != nil {
		return nil, e
	}
	if s.LastCommit != token {
		if s.Pending == nil || s.Pending.Token != token {
			return nil, fmt.Errorf("token does not match pending batch")
		}
		for _, change := range s.Pending.Items {
			item := change.Item
			if source != "calendars" && source != "health-data" {
				item.Data = nil
			}
			s.Seen[item.ID] = item
		}
		s.Pending = nil
		s.LastCommit = token
		if e = fsutil.WriteJSON(path, s, fsutil.SecretFileMode); e != nil {
			return nil, e
		}
	}
	return map[string]any{"ok": true, "source": source, "token": token, "commitMode": "exact-snapshot"}, nil
}
