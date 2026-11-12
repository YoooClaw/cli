package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitFor[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for upload worker")
		var zero T
		return zero
	}
}

func TestUploadArtifactsBoundsConcurrencyAndUploadsEveryArtifact(t *testing.T) {
	started := make(chan struct{}, 10)
	release := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	var result error
	var active, peak atomic.Int32
	var mu sync.Mutex
	seen := map[string]int{}
	artifacts := make([]artifact, 10)
	for i := range artifacts {
		artifacts[i].Filename = fmt.Sprint(i)
	}
	// Always release blocked callbacks, including when a readiness assertion fails.
	t.Cleanup(func() { once.Do(func() { close(release) }); waitFor(t, done) })
	go func() {
		defer close(done)
		result = uploadArtifacts(artifacts, 4, func(_ context.Context, a artifact) error {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			mu.Lock()
			seen[a.Filename]++
			mu.Unlock()
			started <- struct{}{}
			<-release
			return nil
		})
	}()
	for i := 0; i < 4; i++ {
		waitFor(t, started)
	}
	once.Do(func() { close(release) })
	waitFor(t, done)
	if result != nil {
		t.Fatal(result)
	}
	if peak.Load() != 4 {
		t.Fatalf("peak = %d, want 4", peak.Load())
	}
	for _, a := range artifacts {
		if seen[a.Filename] != 1 {
			t.Errorf("artifact %s uploaded %d times, want 1", a.Filename, seen[a.Filename])
		}
	}
}

func TestUploadArtifactsRejectsInvalidLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		called := false
		err := uploadArtifacts([]artifact{{}}, limit, func(context.Context, artifact) error { called = true; return nil })
		if err == nil || called {
			t.Fatalf("limit=%d err=%v called=%v", limit, err, called)
		}
	}
}

func TestUploadArtifactsCancelsPeersAndStopsQueuedWork(t *testing.T) {
	for _, peerFails := range []bool{false, true} {
		t.Run(fmt.Sprint(peerFails), func(t *testing.T) {
			active := make(chan struct{})
			abort := make(chan struct{})
			done := make(chan struct{})
			failure, peerFailure := errors.New("permission denied"), errors.New("peer stopped")
			var result error
			var calls atomic.Int32
			t.Cleanup(func() { close(abort); waitFor(t, done) })
			go func() {
				defer close(done)
				result = uploadArtifacts([]artifact{{Filename: "active"}, {Filename: "fail"}, {Filename: "queued1"}, {Filename: "queued2"}}, 2,
					func(ctx context.Context, a artifact) error {
						calls.Add(1)
						switch a.Filename {
						case "active":
							close(active)
							select {
							case <-ctx.Done():
							case <-abort:
								return errors.New("test aborted")
							}
							if peerFails {
								return peerFailure
							}
							// A peer may finish successfully while another request fails. Its worker
							// must still check cancellation before taking the next queued artifact.
							return nil
						case "fail":
							select {
							case <-active:
							case <-abort:
								return errors.New("test aborted")
							}
							return failure
						default:
							return errors.New("queued task started after cancellation")
						}
					})
			}()
			waitFor(t, done)
			if !errors.Is(result, failure) {
				t.Fatalf("missing original error: %v", result)
			}
			if peerFails && !errors.Is(result, peerFailure) {
				t.Fatalf("missing peer error: %v", result)
			}
			if calls.Load() != 2 {
				t.Fatalf("started %d tasks, want 2", calls.Load())
			}
		})
	}
}

func TestUploadConcurrencyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		raw, only string
		dry       bool
		want      int
		invalid   bool
	}{
		{raw: "auto", only: "install", want: 1}, {raw: "auto", dry: true, want: 1},
		{raw: "", want: 4}, {raw: "1", want: 1}, {raw: "8", want: 8},
		{raw: "auto", invalid: true}, {raw: "0", invalid: true}, {raw: "9", only: "artifacts", invalid: true},
	} {
		t.Run(fmt.Sprintf("%s/%s/%v", tc.raw, tc.only, tc.dry), func(t *testing.T) {
			t.Setenv("OSS_UPLOAD_CONCURRENCY", tc.raw)
			got, err := uploadConcurrency(tc.only, tc.dry)
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", tc.raw)) {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("got=%d err=%v, want %d", got, err, tc.want)
			}
		})
	}
}
