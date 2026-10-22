package cli

import (
	"net/url"

	"github.com/YoooClaw/cli/internal/assets"
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/spf13/cobra"
)

var reminderFlags = map[string]string{
	"status": "status", "source-id": "sourceId", "list-id": "listId",
	"due-from": "dueFrom", "due-to": "dueTo",
	"completed-from": "completedFrom", "completed-to": "completedTo",
	"limit": "limit", "cursor": "cursor",
}

func newRemindersCmd() *cobra.Command {
	root := &cobra.Command{Use: "reminders", Short: "查询已同步的系统提醒事项（只读）"}
	list := &cobra.Command{Use: "list", Short: "分页查询提醒摘要，默认未完成", Args: cobra.NoArgs, RunE: run(remindersRun)}
	for flag, key := range reminderFlags {
		list.Flags().String(flag, "", "Query "+key+"; dates use YYYY-MM-DD, limit 1–100")
	}
	get := &cobra.Command{Use: "get <recordId>", Short: "读取完整提醒事项（含备注和链接）", Args: cobra.ExactArgs(1), RunE: run(remindersRun)}
	get.Flags().String("revision", "", "Optional list revision; refresh on mismatch")
	root.AddCommand(list, get)
	return root
}

func remindersRun(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	path := "/assets/reminders"
	query := url.Values{}
	if cmd.Name() == "get" {
		path += "/" + args[0]
		if _, ok := assets.ReminderRoute(path); !ok {
			return nil, errs.New(errs.CodeInvalidArgument, "recordId must come from reminders list")
		}
		if cmd.Flags().Changed("revision") {
			query.Set("revision", flagStr(cmd, "revision"))
		}
	} else {
		for flag, key := range reminderFlags {
			if cmd.Flags().Changed(flag) {
				query.Set(key, flagStr(cmd, flag))
			}
		}
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	status, result, err := daemon.NewClient(ctx.Paths).Request("GET", path, nil)
	if status >= 400 {
		exitCode = 1
	}
	return result, err
}
