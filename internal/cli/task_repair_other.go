//go:build !windows

package cli

import "github.com/spf13/cobra"

func addTaskRepairCommand(*cobra.Command) {}
