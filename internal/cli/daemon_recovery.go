package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/diagnostics"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
	updater "github.com/YoooClaw/cli/internal/update"
)

// A broken Windows task must not keep an installed update offline. The task is
// left unchanged; the detached process still observes the normal ownership locks.
func recoverManagedStart(ctx *clictx.Context, opts daemon.StartOpts, platform string, startErr error) (any, error) {
	return recoverWindowsTask(platform, startErr, func() {
		_, status := serviceManaged()
		detail := fmt.Errorf("manager=%s unit=%s: %s", status.Manager, status.Unit, diagnostics.SafeError(startErr))
		diagnostics.Write(paths.RootDir(), "warn", "service.start_failed", map[string]any{"profile": ctx.Profile, "error": diagnostics.SafeError(detail)})
		id, err := updater.NewIdentity(ctx.Paths)
		if err != nil {
			return
		}
		exe, err := os.Executable()
		if err != nil {
			return
		}
		exe, err = filepath.EvalSymlinks(exe)
		if err != nil {
			return
		}
		target, err := updater.OwnedTarget(id.Root, exe)
		if err == nil {
			updater.ReportAutostartFailure(id, target, detail)
		}
	}, func() (any, error) {
		if err := daemon.PrecheckStart(ctx, opts); err != nil {
			return nil, err
		}
		state := daemon.State(ctx.Paths)
		lock := state.Lock
		if !state.Running {
			var err error
			lock, err = daemon.Spawn(ctx, opts)
			if err != nil {
				diagnostics.Write(paths.RootDir(), "error", "service.fallback_failed", map[string]any{"profile": ctx.Profile, "error": diagnostics.SafeError(err)})
				return nil, err
			}
		}
		diagnostics.Write(paths.RootDir(), "warn", "service.detached_recovery", map[string]any{"profile": ctx.Profile, "daemonPid": lock.PID})
		return map[string]any{"ok": true, "pid": lock.PID, "bind": lock.Bind, "port": lock.Port, "detached": true, "supervised": false, "autostart": false, "autostartError": diagnostics.SafeError(startErr)}, nil
	})
}

func recoverWindowsTask(platform string, startErr error, report func(), spawn func() (any, error)) (any, error) {
	var e *errs.Error
	if platform != "windows" || !errors.As(startErr, &e) || (e.Code != errs.CodeAutostartUnavailable && e.Code != errs.CodeDaemonNotRunning) {
		return nil, startErr
	}
	report()
	return spawn()
}
