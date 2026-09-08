package cli

import (
	"reflect"
	"testing"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
)

func TestSetupEntrypointRequiresNoFilenameConvention(t *testing.T) {
	if got := setupArgs([]string{"--yes", "--force"}); !reflect.DeepEqual(got, []string{"install", "native", "--yes", "--force"}) {
		t.Fatalf("args=%v", got)
	}
	if got := setupArgs([]string{"--version"}); !reflect.DeepEqual(got, []string{"--version"}) {
		t.Fatalf("version args=%v", got)
	}
}

func TestConfigDefaultsNeedsNoShellOrStdin(t *testing.T) {
	sandbox(t)
	ctx, _ := clictx.Build("default", "json", true, false)
	_, err := initCore(ctx, initOpts{defaults: true, noStart: true, noAutostart: true})
	if err != nil {
		t.Fatal(err)
	}
	if !config.Exists(ctx.Paths) {
		t.Fatal("config not created")
	}
}
