package data

import (
	"os"
	"path/filepath"
)

// Temp files are private and exclusive; readers always observe a complete index.
func writeIndex(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".index-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = replaceIndex(f.Name(), path); e != nil {
		return e
	}
	return syncDirectory(filepath.Dir(path))
}
