package notif

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/YoooClaw/cli/internal/fsutil"
)

type transferSeen struct {
	IDs  []string `json:"ids"`
	Keys []string `json:"keys"`
}

var seenHashRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Preserve imported deduplication facts independently of the rebuildable local .keys cache.
// Content hashes include timestamp, so they remain valid across source/target time zones.
func (s *Storage) loadTransferSeen() error {
	s.transferredIDs = map[string]map[string]bool{}
	s.transferredKeys = map[string]bool{}
	dir := filepath.Join(s.dir, ".transfer-seen")
	list, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, entry := range list {
		day := strings.TrimSuffix(entry.Name(), ".json")
		if entry.IsDir() || !dateDirRE.MatchString(day) || entry.Name() != day+".json" {
			continue
		}
		raw, e := readTransferSeen(filepath.Join(dir, entry.Name()))
		if e != nil {
			return e
		}
		var value transferSeen
		if len(raw) > 64<<20 || json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("invalid transfer dedup index")
		}
		ids := map[string]bool{}
		for _, id := range value.IDs {
			ids[id] = true
		}
		s.transferredIDs[day] = ids
		for _, key := range value.Keys {
			if !seenHashRE.MatchString(key) {
				return fmt.Errorf("invalid transfer dedup hash")
			}
			s.transferredKeys[key] = true
		}
	}
	return nil
}

// ImportTransferSeen merges through the live store; it never overwrites notification day files.
func (s *Storage) ImportTransferSeen(day string, ids, keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !dateDirRE.MatchString(day) {
		return fmt.Errorf("invalid transfer dedup date")
	}
	if s.transferredIDs == nil {
		if e := s.loadTransferSeen(); e != nil {
			return e
		}
	}
	dir := filepath.Join(s.dir, ".transfer-seen")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	file := filepath.Join(dir, day+".json")
	value := transferSeen{}
	if raw, e := readTransferSeen(file); e == nil {
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("invalid transfer dedup index")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	is, ks := map[string]bool{}, map[string]bool{}
	for _, id := range append(value.IDs, ids...) {
		if len(id) > 4096 || strings.ContainsAny(id, "\r\n") {
			return fmt.Errorf("invalid transfer notification id")
		}
		is[id] = true
	}
	for _, key := range append(value.Keys, keys...) {
		if !seenHashRE.MatchString(key) {
			return fmt.Errorf("invalid transfer dedup hash")
		}
		ks[key] = true
	}
	value = transferSeen{IDs: []string{}, Keys: []string{}}
	for id := range is {
		value.IDs = append(value.IDs, id)
	}
	for key := range ks {
		value.Keys = append(value.Keys, key)
	}
	sort.Strings(value.IDs)
	sort.Strings(value.Keys)
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	if len(raw) > 64<<20 {
		return fmt.Errorf("transfer dedup index too large")
	}
	if e = fsutil.WriteAtomic(file, raw, 0600); e != nil {
		return e
	}
	s.transferredIDs[day] = is
	for key := range ks {
		s.transferredKeys[key] = true
	}
	return nil
}

func readTransferSeen(p string) ([]byte, error) {
	st, e := os.Lstat(p)
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() > 64<<20 {
		return nil, fmt.Errorf("invalid transfer dedup index")
	}
	return os.ReadFile(p)
}
