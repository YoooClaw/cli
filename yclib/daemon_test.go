package yclib_test

import (
	"context"
	"testing"

	"github.com/YoooClaw/cli/yclib"
)

func TestDaemonStatus_NotRunning(t *testing.T) {
	c, _ := yclib.New(yclib.Config{RootDir: t.TempDir()})
	st, err := c.Daemon().Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Running || st.Stale {
		t.Fatalf("fresh root should be not-running & not-stale, got %+v", st)
	}
}

func TestDaemonStop_NoopWhenNotRunning(t *testing.T) {
	c, _ := yclib.New(yclib.Config{RootDir: t.TempDir()})
	if err := c.Daemon().Stop(context.Background()); err != nil {
		t.Fatalf("Stop on not-running should be no-op, got %v", err)
	}
}

func TestLightSend_RequiresDaemon(t *testing.T) {
	c, _ := yclib.New(yclib.Config{RootDir: t.TempDir()})
	// daemon 未运行：即便参数合法也应报 CodeDaemonNotRunning（arc §5：不自动拉起）。
	_, err := c.Light().Send(context.Background(), yclib.LightSendOpts{Preset: "red-strobe-3"})
	if code := yclib.ErrorCode(err); code != yclib.CodeDaemonNotRunning {
		t.Fatalf("ErrorCode = %q, want %q", code, yclib.CodeDaemonNotRunning)
	}
}

func TestLightSend_InvalidArgBeforeDaemon(t *testing.T) {
	c, _ := yclib.New(yclib.Config{RootDir: t.TempDir()})
	// 三选一校验在本地，先于 daemon 检查：给 0 个 → CodeInvalidArgument。
	if _, err := c.Light().Send(context.Background(), yclib.LightSendOpts{}); yclib.ErrorCode(err) != yclib.CodeInvalidArgument {
		t.Fatalf("empty opts ErrorCode = %q, want %q", yclib.ErrorCode(err), yclib.CodeInvalidArgument)
	}
	// 给 2 个 → 同样 CodeInvalidArgument。
	two := yclib.LightSendOpts{Preset: "x", Rule: "y"}
	if _, err := c.Light().Send(context.Background(), two); yclib.ErrorCode(err) != yclib.CodeInvalidArgument {
		t.Fatalf("two-source ErrorCode = %q, want %q", yclib.ErrorCode(err), yclib.CodeInvalidArgument)
	}
}

func TestStaticCredentials(t *testing.T) {
	if (yclib.StaticCredentials{Token: "abc"}).GatewayToken() != "abc" {
		t.Fatal("StaticCredentials.GatewayToken mismatch")
	}
}

func TestContextCancelled_DaemonAndLight(t *testing.T) {
	c, _ := yclib.New(yclib.Config{RootDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Daemon().Status(ctx); err == nil {
		t.Error("Daemon().Status should honor cancelled ctx")
	}
	if _, err := c.Light().Send(ctx, yclib.LightSendOpts{Preset: "x"}); err == nil {
		t.Error("Light().Send should honor cancelled ctx")
	}
}
