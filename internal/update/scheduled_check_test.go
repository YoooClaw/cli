package update

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestScheduledCheckWaitsAcrossRestartAndRecordingActivity(t *testing.T) {
	id, c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("scheduled offer should not wait for periodic check")
	})
	now := time.Now()
	offer := Artifact{TargetVersion: "0.13.0"}
	due, err := ScheduleCheck(id, offer, now)
	if err != nil {
		t.Fatal(err)
	}
	if !due.Equal(now.Add(10 * time.Minute)) {
		t.Fatal(due)
	}
	launches := 0
	launch := func(a Artifact) error {
		launches++
		if a.TargetVersion != offer.TargetVersion {
			t.Fatal(a)
		}
		return nil
	}
	s := &Scheduler{}
	if err := s.Tick(context.Background(), id, c, now.Add(9*time.Minute), true, true, launch); err != nil {
		t.Fatal(err)
	}
	if launches != 0 {
		t.Fatal("installed before countdown")
	}
	s = &Scheduler{} // The deadline must survive a daemon restart.
	if err := s.Tick(context.Background(), id, c, due, false, true, launch); err != nil {
		t.Fatal(err)
	}
	if launches != 0 {
		t.Fatal("installed while recording busy")
	}
	if err := s.Tick(context.Background(), id, c, due.Add(time.Minute), true, false, launch); err != nil {
		t.Fatal(err)
	}
	if launches != 0 {
		t.Fatal("installed with auto-update disabled")
	}
	if err := s.Tick(context.Background(), id, c, due.Add(2*time.Minute), true, true, launch); err != nil {
		t.Fatal(err)
	}
	if launches != 1 {
		t.Fatal(launches)
	}
}

func TestRepeatedCheckResetsCountdown(t *testing.T) {
	id, c := fixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected periodic check") })
	now := time.Now()
	offer := Artifact{TargetVersion: "0.13.0"}
	if _, err := ScheduleCheck(id, offer, now); err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{}
	launch := func(Artifact) error { t.Fatal("installed before renewed deadline"); return nil }
	if err := s.Tick(context.Background(), id, c, now, true, true, launch); err != nil {
		t.Fatal(err)
	}
	if _, err := ScheduleCheck(id, offer, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(context.Background(), id, c, now.Add(10*time.Minute), true, true, launch); err != nil {
		t.Fatal(err)
	}
}
