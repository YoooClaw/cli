// Package installer implements the file transaction shared by native setup and
// explicit updates. Platform hooks own PATH and daemon lifecycle management.
package installer

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Hooks struct {
	Stop         func() error
	Verify       func(string) error
	CommitPath   func() error
	RestorePath  func() error
	Resume       func() error
	RemoveBackup func(string) error
}

type Result struct {
	Installed []string `json:"installed"`
	Warnings  []string `json:"warnings,omitempty"`
}

// Install stages before Stop and uses same-volume renames for backups. It
// never deletes configuration or data. Rollback failure retains named backups.
func Install(source, dir string, force bool, h Hooks) (result Result, err error) {
	if !filepath.IsAbs(dir) || filepath.Dir(dir) == dir {
		return result, fmt.Errorf("install directory must be an absolute non-root directory")
	}
	payload, err := os.ReadFile(source)
	if err != nil {
		return result, err
	}
	if len(payload) == 0 {
		return result, fmt.Errorf("empty installer payload")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return result, err
	}
	stage, err := os.MkdirTemp(dir, ".yoooclaw-install-")
	if err != nil {
		return result, err
	}
	defer func() { _ = os.Remove(stage) }() // Only remove if empty.
	type entry struct {
		target, backup, staged string
		hadOld, placed         bool
	}
	entries := []*entry{}
	for _, name := range []string{"yoooclaw.exe", "yc.exe"} {
		e := &entry{target: filepath.Join(dir, name), backup: filepath.Join(stage, name+".backup"), staged: filepath.Join(stage, name)}
		info, checkErr := os.Lstat(e.target)
		if checkErr == nil {
			if !info.Mode().IsRegular() {
				return result, fmt.Errorf("refusing to replace non-regular target %s", e.target)
			}
			if !force {
				return result, fmt.Errorf("%s already exists; use --force", e.target)
			}
			e.hadOld = true
		} else if !os.IsNotExist(checkErr) {
			return result, checkErr
		}
		entries = append(entries, e)
	}
	defer func() {
		for _, e := range entries {
			_ = os.Remove(e.staged)
		}
	}()
	for _, e := range entries {
		if err := os.WriteFile(e.staged, payload, 0o700); err != nil {
			return result, err
		}
	}
	stopped, committed := false, false
	defer func() {
		if committed || !stopped {
			return
		}
		var rollback []error
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if e.placed {
				if removeErr := os.Remove(e.target); removeErr != nil {
					rollback = append(rollback, removeErr)
					continue
				}
			}
			if _, statErr := os.Stat(e.backup); statErr == nil {
				if restoreErr := os.Rename(e.backup, e.target); restoreErr != nil {
					rollback = append(rollback, restoreErr)
				}
			}
		}
		if h.RestorePath != nil {
			rollback = append(rollback, h.RestorePath())
		}
		if h.Resume != nil && errors.Join(rollback...) == nil {
			rollback = append(rollback, h.Resume())
		}
		if restoreErr := errors.Join(rollback...); restoreErr != nil {
			err = fmt.Errorf("%w; rollback incomplete (backups: %s): %v", err, stage, restoreErr)
		}
	}()
	stopped = true // Stop may partially succeed; Resume must handle that too.
	if h.Stop != nil {
		if err := h.Stop(); err != nil {
			return result, err
		}
	}
	for _, e := range entries {
		if e.hadOld {
			if err := os.Rename(e.target, e.backup); err != nil {
				return result, err
			}
		}
		if err := os.Rename(e.staged, e.target); err != nil {
			return result, err
		}
		e.placed = true
		got, err := os.ReadFile(e.target)
		if err != nil {
			return result, err
		}
		if !bytes.Equal(payload, got) {
			return result, fmt.Errorf("installed bytes differ: %s", e.target)
		}
		if h.Verify != nil {
			if err := h.Verify(e.target); err != nil {
				return result, err
			}
		}
		result.Installed = append(result.Installed, e.target)
	}
	if h.CommitPath != nil {
		if err := h.CommitPath(); err != nil {
			return result, err
		}
	}
	committed = true
	for _, e := range entries {
		if !e.hadOld {
			continue
		}
		remove := os.Remove
		if h.RemoveBackup != nil {
			remove = h.RemoveBackup
		}
		if err := remove(e.backup); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("backup retained at %s: %v", e.backup, err))
		}
	}
	return result, nil
}
