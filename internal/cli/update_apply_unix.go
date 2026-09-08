//go:build !windows

package cli

import (
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/errs"
)

func applyNativeUpdate(_ *clictx.Context, _ string) (any, error) {
	return nil, errs.New(errs.CodeInvalidArgument, "--apply 当前仅用于 Windows；macOS/Linux 的安装升级方式保持不变")
}
