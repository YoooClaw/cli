package transfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileReceiptResumeAndChangedFile(t *testing.T) {
	home, local := t.TempDir(), t.TempDir()
	blob := filepath.Join(t.TempDir(), "blob")
	data := []byte("original")
	os.WriteFile(blob, data, 0600)
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	hash := sha(string(data))
	if err = installFile(context.Background(), root, home, "file", blob, hash); err != nil {
		t.Fatal(err)
	}
	s := &Service{}
	if err = s.saveFileReceipt(local, home, "file", hash); err != nil {
		t.Fatal(err)
	}
	s = &Service{receipts: loadFileReceipts(filepath.Join(local, "files.jsonl"))}
	if got, e := s.targetState(home, "file"); e != nil || got != "file:"+hash {
		t.Fatal(got, e)
	}
	os.WriteFile(filepath.Join(home, "file"), []byte("modified"), 0600)
	os.Chtimes(filepath.Join(home, "file"), time.Now(), time.Now().Add(time.Second))
	if got, e := s.targetState(home, "file"); e != nil || got == "file:"+hash {
		t.Fatal("modified file treated as completed", got, e)
	}
	if err = installFile(context.Background(), root, home, "file", blob, hash); err == nil {
		t.Fatal("user file overwritten")
	}
	os.Remove(filepath.Join(home, "file"))
	if got, e := s.targetState(home, "file"); e != nil || got != "absent" {
		t.Fatal(got, e)
	}
}

func TestPendingTemporaryTargetCleanup(t *testing.T) {
	home, local := t.TempDir(), t.TempDir()
	s := &Service{Home: home}
	os.WriteFile(filepath.Join(home, ".transfer-partial"), []byte("partial"), 0600)
	os.WriteFile(filepath.Join(home, "user.txt"), []byte("keep"), 0600)
	if err := writePrivateJSON(filepath.Join(local, "pending-target.json"), ".transfer-partial"); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanupPendingTarget(local); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".transfer-partial")); !os.IsNotExist(err) {
		t.Fatal("partial retained")
	}
	if _, err := os.Stat(filepath.Join(home, "user.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestCommitFileCollisionReusesTemporaryFile(t *testing.T) {
	for _, identical := range []bool{false, true} {
		t.Run(fmt.Sprint(identical), func(t *testing.T) {
			home := t.TempDir()
			tmp := filepath.Join(home, ".transfer-test")
			if err := os.WriteFile(tmp, []byte("incoming"), 0600); err != nil {
				t.Fatal(err)
			}
			hash, err := hashFile(tmp)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(tmp)
			if err != nil {
				t.Fatal(err)
			}
			// Another writer claims the destination after the copy has completed.
			existing := "existing"
			if identical {
				existing = "incoming"
			}
			if err := os.WriteFile(filepath.Join(home, "note.md"), []byte(existing), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(home)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			actual := "note.md"
			if err := commitImportedFile(context.Background(), root, home, ".transfer-test", "note.md", hash, &actual); err != nil {
				t.Fatal(err)
			}
			want := "note-1.md"
			if identical {
				want = "note.md"
			}
			if actual != want {
				t.Fatalf("actual=%s", actual)
			}
			raw, err := os.ReadFile(filepath.Join(home, "note.md"))
			if err != nil || string(raw) != existing {
				t.Fatal("overwrote destination", err)
			}
			if !identical {
				after, err := os.Stat(filepath.Join(home, actual))
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(before, after) {
					t.Fatal("temporary file was copied again")
				}
			}
		})
	}
}
