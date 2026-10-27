package reminders

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"runtime"
	"sort"

	"github.com/YoooClaw/cli/internal/fsutil"
)

type transaction struct {
	Files  map[string]string `json:"files"`
	Remove []string          `json:"remove"`
}

func (s *Storage) root(create bool) (*os.Root, error) {
	info, err := os.Lstat(s.dir)
	if os.IsNotExist(err) && create {
		if err = fsutil.EnsureDir(s.dir, fsutil.DirMode); err != nil {
			return nil, err
		}
		info, err = os.Lstat(s.dir)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("invalid reminders directory")
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		root.Close()
		return nil, errors.New("reminders directory changed")
	}
	return root, nil
}

func (s *Storage) read(name string, limit int64) ([]byte, error) {
	root, err := s.root(false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("invalid or oversized reminders file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return nil, errors.New("reminders file changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if len(raw) > int(limit) {
		return nil, errors.New("reminders file exceeds limit")
	}
	return raw, err
}

func (s *Storage) write(name string, raw []byte) error {
	root, err := s.root(true)
	if err != nil {
		return err
	}
	defer root.Close()
	dir := path.Dir(name)
	if dir != "." {
		if err := root.MkdirAll(dir, fsutil.DirMode); err != nil {
			return err
		}
		info, err := root.Lstat(dir)
		if err != nil || !info.IsDir() {
			return errors.New("invalid reminders partition directory")
		}
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	temp := path.Join(dir, "."+hex.EncodeToString(random[:])+".tmp")
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fsutil.SecretFileMode)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, writeErr := f.Write(raw)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename(temp, name); err != nil {
		return err
	}
	return syncDirectory(root, dir)
}

func syncDirectory(root *os.Root, name string) error {
	// Windows does not support fsync on directory handles. The redo journal still
	// protects process interruption there; Unix filesystem errors must not be ignored.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := root.Open(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Storage) partitions() ([]string, error) {
	root, err := s.root(false)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	names := []string{}
	if _, err := root.Lstat("active.jsonl"); err == nil {
		names = append(names, "active.jsonl")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	info, err := root.Lstat("completed")
	if os.IsNotExist(err) {
		return names, nil
	}
	if err != nil || !info.IsDir() {
		return nil, errors.New("invalid reminders completed directory")
	}
	dir, err := root.Open("completed")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := "completed/" + entry.Name()
		if validPartition(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *Storage) commit(tx transaction) error {
	raw, err := json.Marshal(tx)
	if err != nil || len(raw) > StoreLimit {
		return errors.New("reminders transaction exceeds limit")
	}
	// This redo journal is durable before replacing any live partition. Recovery
	// completes it under the same lock before reads or another upload can proceed.
	if err := s.write(".pending.json", raw); err != nil {
		return err
	}
	return s.recover()
}

func (s *Storage) recover() error {
	raw, err := s.read(".pending.json", StoreLimit)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var tx transaction
	if json.Unmarshal(raw, &tx) != nil || tx.Files["index.json"] == "" || tx.Files["state.json"] == "" {
		return errors.New("invalid reminders transaction")
	}
	names := []string{}
	for name := range tx.Files {
		if name == "index.json" || name == "state.json" {
			continue
		}
		if !validPartition(name) {
			return errors.New("invalid reminders transaction path")
		}
		names = append(names, name)
	}
	for _, name := range tx.Remove {
		if !validPartition(name) {
			return errors.New("invalid reminders removal path")
		}
		if _, replaced := tx.Files[name]; replaced {
			return errors.New("conflicting reminders transaction paths")
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := s.write(name, []byte(tx.Files[name])); err != nil {
			return err
		}
	}
	root, err := s.root(false)
	if err != nil {
		return err
	}
	defer root.Close()
	removedDirs := map[string]bool{}
	for _, name := range tx.Remove {
		if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
			return err
		}
		removedDirs[path.Dir(name)] = true
	}
	// Persist deletions before dropping the journal; otherwise a completed partition
	// could reappear after power loss and later be included in an index rebuild.
	for dir := range removedDirs {
		if err := syncDirectory(root, dir); err != nil {
			return err
		}
	}
	for _, name := range []string{"index.json", "state.json"} {
		if err := s.write(name, []byte(tx.Files[name])); err != nil {
			return err
		}
	}
	if err := s.write("README.md", []byte(readme)); err != nil {
		return err
	}
	if err := root.Remove(".pending.json"); err != nil {
		return err
	}
	return syncDirectory(root, ".")
}

const readme = `# System reminders (V1)

This directory is a current mirror, not an event log. Each successful full
snapshot replaces only its sourceId. Absence means no longer visible to that
source; it is not proof of a user deletion. Completed and recurring reminders
are kept exactly as returned by EventKit, without expanding future occurrences.

active.jsonl contains incomplete reminders. completed/YYYY-MM-DD.jsonl uses the
completion timestamp's date/offset; completed/undated.jsonl has no completion time.
Each line contains the full reminder, sourceId and recordId. index.json contains
query summaries and relative file locations; it can be rebuilt from JSONL.
state.json stores each source's last accepted query-start collectedAt timestamp.
No byte offsets, event history or persistent sorted arrays are stored.

Use yoooclaw reminders list/get or GET /assets/reminders for consistent reads.
A private .pending.json redo journal may exist during a replacement; the daemon
recovers it before serving any reads. Do not consume partitions mid-transaction.
Pagination uses the full content revision; a timestamp-only sync keeps cursors valid.
All data is local to the current profile and may contain private notes.
`
