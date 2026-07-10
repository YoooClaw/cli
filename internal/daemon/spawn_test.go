package daemon

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestProbeDaemonHealthRequiresHTTPResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"server":"yoooclaw"}`))
	}))
	defer server.Close()
	host, rawPort, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(rawPort)
	if err := probeDaemonHealth(&Lock{Bind: host, Port: port}, time.Second); err != nil {
		t.Fatalf("healthy daemon probe failed: %v", err)
	}
}

func TestProbeDaemonHealthRejectsUnrelatedHTTPService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"server":"something-else"}`))
	}))
	defer server.Close()
	host, rawPort, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(rawPort)
	if err := probeDaemonHealth(&Lock{Bind: host, Port: port}, time.Second); err == nil {
		t.Fatal("unrelated HTTP service must not satisfy daemon readiness")
	}
}

func TestWaitForDaemonReadyUsesHealthNotLockAlone(t *testing.T) {
	p := sandboxPaths(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if err := WriteLock(p, Lock{PID: os.Getpid(), Bind: "127.0.0.1", Port: port}); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{})
	go func() {
		time.Sleep(250 * time.Millisecond)
		close(ready)
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"server":"yoooclaw"}`))
		}))
	}()
	started := time.Now()
	lock, err := waitForDaemonReady(p, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	<-ready
	if lock.Port != port {
		t.Fatalf("ready lock port = %d, want %d", lock.Port, port)
	}
	if time.Since(started) < 200*time.Millisecond {
		t.Fatal("lock alone was treated as ready before HTTP started")
	}
}
