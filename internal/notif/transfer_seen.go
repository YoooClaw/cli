package notif

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

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
		file := filepath.Join(dir, entry.Name())
		raw, e := readTransferSeen(file)
		if e != nil {
			return e
		}
		var value transferSeen
		if e := json.Unmarshal(raw, &value); e != nil {
			return fmt.Errorf("parse transfer dedup index %q: %w", file, e)
		}
		ids := map[string]bool{}
		for _, id := range value.IDs {
			ids[id] = true
		}
		s.transferredIDs[day] = ids
		for _, key := range value.Keys {
			if !seenHashRE.MatchString(key) {
				return fmt.Errorf("validate transfer dedup hash in %q: invalid hash format (length=%d)", file, len(key))
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
		if e := json.Unmarshal(raw, &value); e != nil {
			return fmt.Errorf("parse transfer dedup index %q: %w", file, e)
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	is, ks := map[string]bool{}, map[string]bool{}
	for i, id := range append(value.IDs, ids...) {
		// IDs are opaque strings. JSON safely escapes control characters.
		// Skip unreadable IDs instead of silently replacing bytes during JSON encoding.
		if !utf8.ValidString(id) {
			if s.logger != nil {
				s.logger.Warn(fmt.Sprintf("[transfer] notification-index.id_skipped path=%q entry=%d reason=invalid-utf8 bytes=%d", file, i+1, len(id)))
			}
			continue
		}
		is[id] = true
	}
	for _, key := range append(value.Keys, keys...) {
		if !seenHashRE.MatchString(key) {
			return fmt.Errorf("validate transfer dedup hash in %q: invalid hash format (length=%d)", file, len(key))
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
		return fmt.Errorf("transfer dedup index %q too large: bytes=%d limit=%d", file, len(raw), 64<<20)
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
		return nil, fmt.Errorf("invalid transfer dedup index %q: mode=%s bytes=%d limit=%d", p, st.Mode(), st.Size(), 64<<20)
	}
	return os.ReadFile(p)
}
