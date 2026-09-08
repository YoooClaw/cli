//go:build !windows

package cli

import (
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/spf13/cobra"
)

func installNative(_ *clictx.Context, _ *cobra.Command, _ []string) (any, error) {
	return nil, errs.New(errs.CodeInvalidArgument, "原生 setup 安装入口仅用于 Windows；macOS/Linux 安装方式不变")
}
