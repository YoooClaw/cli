package transfer

import (
	"bufio"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Receipts are a fast path for files this import has already committed.
// Missing/truncated receipts fall back to the existing content comparison.
type fileReceipt struct {
	Path     string `json:"path"`
	Hash     string `json:"hash"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

func loadFileReceipts(path string) map[string]fileReceipt {
	out := map[string]fileReceipt{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		var item fileReceipt
		if json.Unmarshal(line, &item) == nil {
			out[item.Path] = item
		}
	}
	return out
}
func (s *Service) saveFileReceipt(local, home, dest, hash string) error {
	st, err := os.Lstat(filepath.Join(home, filepath.FromSlash(dest)))
	if err != nil {
		return err
	}
	item := fileReceipt{dest, hash, st.Size(), st.ModTime().UnixNano()}
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(local, "files.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	// A leading newline isolates a torn last record after a power loss.
	_, err = f.Write(append(append([]byte{'\n'}, raw...), '\n'))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if s.receipts == nil {
		s.receipts = map[string]fileReceipt{}
	}
	s.receipts[dest] = item
	return nil
}
func (s *Service) targetState(home, rel string) (string, error) {
	return targetStateWithReceipts(home, rel, s.receipts)
}

// The journal names only a temporary target created by this import, never a user file.
func (s *Service) cleanupPendingTarget(local string) error {
	marker := filepath.Join(local, "pending-target.json")
	raw, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var rel string
	if json.Unmarshal(raw, &rel) != nil || !validRelative(rel) || !strings.HasPrefix(path.Base(rel), ".transfer-") {
		return fail("UNSAFE_TARGET_PATH")
	}
	home, err := s.importHome()
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = safeTargetParents(root, home, rel); err != nil {
		return err
	}
	if err = root.RemoveAll(filepath.FromSlash(rel)); err != nil {
		return err
	}
	return os.Remove(marker)
}
