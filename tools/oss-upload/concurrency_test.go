package main

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestUploadArtifactsBoundsConcurrency(t *testing.T) {
	started := make(chan struct{}, 10)
	release := make(chan struct{})
	done := make(chan error, 1)
	var active, peak atomic.Int32
	go func() {
		done <- uploadArtifacts(make([]artifact, 10), 4, func(artifact) error {
			n := active.Add(1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			return nil
		})
	}()
	for i := 0; i < 4; i++ {
		<-started
	}
	select {
	case <-started:
		t.Fatal("exceeded concurrency")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 4 {
		t.Fatalf("peak = %d", peak.Load())
	}
}

func TestUploadArtifactsFailureStopsScheduling(t *testing.T) {
	failure := errors.New("upload failed")
	calls := 0
	err := uploadArtifacts(make([]artifact, 5), 1, func(artifact) error {
		calls++
		return failure
	})
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestUploadArtifactsDrainsActiveRequestsOnFailure(t *testing.T) {
	active := make(chan struct{})
	failed := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	failure := errors.New("upload failed")
	var calls atomic.Int32
	go func() {
		done <- uploadArtifacts([]artifact{{Filename: "active"}, {Filename: "fail"}}, 2, func(a artifact) error {
			calls.Add(1)
			if a.Filename == "active" {
				close(active)
				<-release
				return nil
			}
			<-active
			close(failed)
			return failure
		})
	}()
	<-failed
	select {
	case err := <-done:
		t.Fatalf("returned before active request completed: %v", err)
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}
