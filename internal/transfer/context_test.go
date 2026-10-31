package transfer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCancellationAndConfiguredTimeout(t *testing.T) {
	t.Setenv("YOOOCLAW_TRANSFER_TIMEOUT", "37m")
	if Timeout() != 37*time.Minute {
		t.Fatal("timeout ignored")
	}
	t.Setenv("YOOOCLAW_TRANSFER_TIMEOUT", "")
	if Timeout() != 6*time.Hour {
		t.Fatal("default timeout")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := contextReader{ctx, strings.NewReader("data")}
	if _, e := r.Read(make([]byte, 4)); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	dst := newEnv(t)
	if _, e := dst.svc.HandleContext(ctx, Request{Action: "capabilities"}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
