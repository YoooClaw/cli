package update

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
)

type ScheduledCheck struct {
	From      string    `json:"from"`
	Artifact  Artifact  `json:"artifact"`
	NotBefore time.Time `json:"notBefore"`
}

func lockSchedule(id Identity) (func(), error) {
	if err := os.MkdirAll(id.Directory(), 0700); err != nil {
		return nil, err
	}
	return lock(filepath.Join(id.Directory(), "schedule.lock"))
}

// ScheduleCheck hands a checked offer to the daemon without starting an installer.
func ScheduleCheck(id Identity, offer Artifact, now time.Time) (time.Time, error) {
	release, err := lockSchedule(id)
	if err != nil {
		return time.Time{}, err
	}
	defer release()
	due := now.Add(10 * time.Minute)
	err = fsutil.WriteJSON(filepath.Join(id.Directory(), "scheduled-check.json"), ScheduledCheck{id.CurrentVersion, offer, due}, 0600)
	return due, err
}

type Scheduler struct {
	Candidate  *Artifact
	NextCheck  time.Time
	RetryAfter time.Time
	Identity   Identity
}

// Tick never substitutes a cached offer for the installer's exact recheck.
func (s *Scheduler) Tick(ctx context.Context, id Identity, c *Client, now time.Time, idle, enabled bool, launch func(Artifact) error) error {
	if s.Identity.BaseURL != id.BaseURL || s.Identity.KeyHash != id.KeyHash || s.Identity.CurrentVersion != id.CurrentVersion {
		s.Candidate = nil
		s.Identity = id
		s.NextCheck = now
		s.RetryAfter = time.Time{}
		_, _ = fsutil.ReadJSON(filepath.Join(id.Directory(), "install-cooldown.json"), &s.RetryAfter)
	}
	release, err := lockSchedule(id)
	if err != nil {
		return err
	}
	defer release()
	requestPath := filepath.Join(id.Directory(), "scheduled-check.json")
	var request ScheduledCheck
	scheduled, err := fsutil.ReadJSON(requestPath, &request)
	if err != nil {
		return err
	}
	if scheduled && request.From != id.CurrentVersion {
		if err := os.Remove(requestPath); err != nil {
			return err
		}
		scheduled = false
	}
	if scheduled {
		s.Candidate = &request.Artifact
		s.NextCheck = now.Add(4 * time.Hour)
	}
	if !now.Before(s.NextCheck) {
		s.NextCheck = now.Add(4 * time.Hour)
		candidate, err := c.Check(ctx, id, "")
		if err != nil {
			s.Candidate = nil
			return err
		}
		s.Candidate = candidate
	}
	// The new daemon or installer may confirm activation after we loaded the cooldown.
	if now.Before(s.RetryAfter) {
		if _, err := os.Stat(filepath.Join(id.Directory(), "install-cooldown.json")); os.IsNotExist(err) {
			s.RetryAfter = time.Time{}
		}
	}
	if (scheduled && now.Before(request.NotBefore)) || !enabled || !idle || s.Candidate == nil || now.Before(s.RetryAfter) || ctx.Err() != nil {
		return nil
	}
	s.RetryAfter = now.Add(4 * time.Hour)
	if err := fsutil.WriteJSON(filepath.Join(id.Directory(), "install-cooldown.json"), s.RetryAfter, 0600); err != nil {
		return err
	}
	if scheduled {
		if err := os.Remove(requestPath); err != nil {
			return err
		}
	}
	offer := *s.Candidate
	s.Candidate = nil
	return launch(offer)
}

// StartRuntime owns checking and queue replay, but never owns the detached installer.
func StartRuntime(identity func() (Identity, error), executable string, idle func() bool, connected func() bool, enabled func() bool, log func(string)) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		started := time.Now()
		scheduler := &Scheduler{}
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		tick := func() {
			id, err := identity()
			if err != nil {
				return
			}
			target, ownedErr := OwnedTarget(id.Root, executable)
			if ownedErr != nil {
				return
			}
			executable = target
			c := NewClient(id)
			Heartbeat(id)
			if connected() {
				ConfirmPending(id, executable)
			}
			Flush(ctx, c, id)
			if time.Since(started) < time.Minute {
				return
			}
			canInstall := enabled()
			if _, err := OwnedTarget(id.Root, executable); err != nil {
				canInstall = false
			}
			err = scheduler.Tick(ctx, id, c, time.Now(), idle(), canInstall, func(a Artifact) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				_, err := Launch(id, a, "silent", executable)
				return err
			})
			if err != nil && ctx.Err() == nil {
				log(safe(err.Error()))
			}
			cleanWorkers(id.Root)
		}
		tick()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tick()
			}
		}
	}()
	return cancel
}
func cleanWorkers(root string) {
	// Do not remove a running attempt. Workers hold the same install lock.
	release, err := lock(filepath.Join(root, "update", "install.lock"))
	if err != nil {
		return
	}
	defer release()
	dir := filepath.Join(root, "update", "attempts")
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if !entry.IsDir() || !regexpID(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err == nil && time.Since(info.ModTime()) > 7*24*time.Hour {
			_ = os.RemoveAll(path)
		}
	}
}
