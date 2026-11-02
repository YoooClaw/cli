//go:build !windows

package data

import "os"

func replaceIndex(from, to string) error { return os.Rename(from, to) }
func syncDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
