package cli

import (
	"bytes"
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/paths"
	"os"
	"testing"
)

func envTestContext(t *testing.T) *clictx.Context {
	t.Helper()
	t.Setenv("YOOOCLAW_HOME", t.TempDir())
	t.Setenv("PHONE_NOTIFICATIONS_ENV", "")
	t.Setenv("OPENCLAW_HOST_PRODUCTION", "")
	p := paths.For("default")
	if err := config.Save(p, config.Default(p.Credentials)); err != nil {
		t.Fatal(err)
	}
	return &clictx.Context{Profile: "default", Paths: p}
}
func TestEnvSwitchPreservesCredentialsAndUnknownConfig(t *testing.T) {
	ctx := envTestContext(t)
	credentials := []byte(`{"apiKeys":[{"label":"legacy","key":"fixture","default":true}]}`)
	if err := os.WriteFile(paths.SharedCredentialsPath(), credentials, 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := config.LoadRaw(ctx.Paths)
	raw["futureField"] = "keep"
	_ = config.SaveRaw(ctx.Paths, raw)
	for _, target := range []string{"test", "development", "production"} {
		value, err := envSwitch(ctx, nil, []string{target})
		if err != nil {
			t.Fatal(err)
		}
		if value.(map[string]any)["restarted"] != false {
			t.Fatal(value)
		}
		shown, err := envShow(ctx, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if shown.(map[string]any)["env"] != target {
			t.Fatal(shown)
		}
	}
	after, _ := os.ReadFile(paths.SharedCredentialsPath())
	if !bytes.Equal(after, credentials) {
		t.Fatal("credentials changed")
	}
	raw, _ = config.LoadRaw(ctx.Paths)
	if raw["futureField"] != "keep" {
		t.Fatal("unknown field lost")
	}
}
func TestEnvSwitchRejectsHermesBeforeWrite(t *testing.T) {
	ctx := envTestContext(t)
	before, _ := os.ReadFile(ctx.Paths.Config)
	old := writerLockStatus
	t.Cleanup(func() { writerLockStatus = old })
	writerLockStatus = func(paths.Paths) (bool, string, error) { return true, "hermes-plugin", nil }
	if _, err := envSwitch(ctx, nil, []string{"test"}); err == nil {
		t.Fatal("expected rejection")
	}
	after, _ := os.ReadFile(ctx.Paths.Config)
	if !bytes.Equal(before, after) {
		t.Fatal("config changed")
	}
}
func TestEnvSwitchRejectsOverridesAndCustomRelay(t *testing.T) {
	for _, mode := range []string{"env", "custom", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			ctx := envTestContext(t)
			target := "test"
			if mode == "env" {
				t.Setenv("PHONE_NOTIFICATIONS_ENV", "production")
			}
			if mode == "custom" {
				cfg, _ := config.Load(ctx.Paths)
				cfg.Relay.URL = "wss://private.example/relay"
				_ = config.Save(ctx.Paths, cfg)
			}
			if mode == "invalid" {
				target = "typo"
			}
			before, _ := os.ReadFile(ctx.Paths.Config)
			if _, err := envSwitch(ctx, nil, []string{target}); err == nil {
				t.Fatal("expected rejection")
			}
			after, _ := os.ReadFile(ctx.Paths.Config)
			if !bytes.Equal(before, after) {
				t.Fatal("config changed")
			}
		})
	}
}
