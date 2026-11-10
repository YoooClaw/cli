package ossdownload

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLinkRefresherSharesRefresh(t *testing.T) {
	var calls atomic.Int32
	state := NewLinkRefresher(
		Link{URL: "https://example.test/old", ExpiresAt: time.Now().Add(time.Minute)},
		func(context.Context) (Link, error) {
			calls.Add(1)
			time.Sleep(10 * time.Millisecond)
			return Link{URL: "https://example.test/new", ExpiresAt: time.Now().Add(time.Hour)}, nil
		},
		LinkRefresherOptions{},
	)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := state.Current(context.Background())
			if err != nil || got.URL != "https://example.test/new" {
				t.Errorf("link=%+v err=%v", got, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls=%d", calls.Load())
	}
	if !state.HasRefreshed() {
		t.Fatal("HasRefreshed=false after refresh")
	}
}

func TestLinkRefresherCachesFailurePerAttempt(t *testing.T) {
	var calls atomic.Int32
	wantErr := errors.New("refresh failed")
	state := NewLinkRefresher(
		Link{URL: "https://example.test/old", ExpiresAt: time.Now().Add(time.Minute)},
		func(context.Context) (Link, error) {
			calls.Add(1)
			return Link{}, wantErr
		},
		LinkRefresherOptions{},
	)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := state.Current(context.Background()); !errors.Is(err, wantErr) {
				t.Errorf("err=%v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls=%d", calls.Load())
	}
	state.ResetError()
	if _, err := state.Current(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("retry err=%v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("refresh calls after reset=%d", calls.Load())
	}
}

func TestLinkRefresherZeroExpirySemantics(t *testing.T) {
	var calls atomic.Int32
	refresh := func(context.Context) (Link, error) {
		calls.Add(1)
		return Link{URL: "https://example.test/new", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}

	// Recording links may have no known expiry: use them as they are.
	unknown := NewLinkRefresher(Link{URL: "https://example.test/old"}, refresh, LinkRefresherOptions{ZeroExpiryValid: true})
	if got, err := unknown.Current(context.Background()); err != nil || got.URL != "https://example.test/old" {
		t.Fatalf("link=%+v err=%v", got, err)
	}
	if calls.Load() != 0 {
		t.Fatal("refreshed a link without expiry")
	}

	// Migration links always carry an expiry; zero must refresh instead.
	expired := NewLinkRefresher(Link{URL: "https://example.test/old"}, refresh, LinkRefresherOptions{})
	got, err := expired.Current(context.Background())
	if err != nil || got.URL != "https://example.test/new" {
		t.Fatalf("link=%+v err=%v", got, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("refreshes=%d", calls.Load())
	}

	// A refresh that returns no expiry is rejected for migration links.
	bad := NewLinkRefresher(Link{URL: "https://example.test/old", ExpiresAt: time.Now()}, func(context.Context) (Link, error) {
		return Link{URL: "https://example.test/new"}, nil
	}, LinkRefresherOptions{})
	if _, err := bad.Current(context.Background()); err == nil {
		t.Fatal("accepted a refresh without expiry")
	}
}

func TestRenewingClientUsesCurrentLink(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.URL.Path)
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer srv.Close()

	state := NewLinkRefresher(Link{URL: srv.URL + "/new", ExpiresAt: time.Now().Add(time.Hour)}, nil, LinkRefresherOptions{})
	client := &RenewingClient{Refresher: state, Next: http.DefaultClient}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://stale.example.test/old", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got.Load() != "/new" {
		t.Fatalf("path=%v", got.Load())
	}
}

func TestRenewingClientRejectsInvalidLink(t *testing.T) {
	state := NewLinkRefresher(Link{URL: "://bad", ExpiresAt: time.Now().Add(time.Hour)}, nil, LinkRefresherOptions{})
	client := &RenewingClient{Refresher: state, Next: http.DefaultClient}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://stale.example.test/old", nil)
	if _, err := client.Do(req); err == nil {
		t.Fatal("accepted invalid link")
	}
}
