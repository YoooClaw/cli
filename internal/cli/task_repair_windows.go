//go:build windows

package cli

import (
	"github.com/YoooClaw/cli/internal/wintask"
	"github.com/spf13/cobra"
)

func addTaskRepairCommand(parent *cobra.Command) {
	parent.AddCommand(&cobra.Command{Use: "repair-task-permissions SID", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		return wintask.GrantTask(args[0])
	}})
}
