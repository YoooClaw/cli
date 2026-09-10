package cli

import (
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/YoooClaw/cli/internal/aitodo"
	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/creds"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/spf13/cobra"
)

var todoFlags = map[string]string{"title": "title", "due-at": "dueAt", "is-full-day": "isFullDay", "is-done": "isDone", "start-time": "startTime", "end-time": "endTime", "keyword": "keyword", "page-no": "pageNo", "page-size": "pageSize"}

func newTodoCmd() *cobra.Command {
	root := &cobra.Command{Use: "todo", Short: "管理云端待办（无需 daemon）"}
	for _, action := range []string{"list", "get", "create", "update", "delete"} {
		cmd := &cobra.Command{Use: action, Short: "AI TODO " + action, RunE: run(todoRun)}
		if action == "get" || action == "update" {
			cmd.Use += " [todoId]"
			cmd.Args = cobra.MaximumNArgs(1)
		} else if action == "delete" {
			cmd.Use += " [todoIds...]"
		} else {
			cmd.Args = cobra.NoArgs
		}
		cmd.Flags().String("json-file", "", "Read a JSON object from file, or - for stdin; cannot combine with field flags")
		switch action {
		case "list":
			for _, f := range []string{"is-done", "start-time", "end-time", "keyword", "page-no", "page-size"} {
				cmd.Flags().String(f, "", "Filter "+f+"; time bounds accept ISO with timezone offsets or null")
			}
		case "create", "update":
			cmd.Flags().String("title", "", "Todo title")
			cmd.Flags().String("due-at", "", "START time: ISO with timezone offset, all-day YYYY-MM-DD, or null")
			cmd.Flags().String("is-full-day", "", "true or false")
			if action == "update" {
				cmd.Flags().String("is-done", "", "true completes; false reopens")
			} else {
				cmd.Flags().String("request-key", "", "Reuse the returned key to retry the same creation after an unknown outcome")
			}
		case "delete":
			cmd.Flags().Bool("confirmed", false, "Confirm deletion of these specific todos")
		}
		root.AddCommand(cmd)
	}
	return root
}
func todoParams(cmd *cobra.Command, args []string) (aitodo.Fields, error) {
	bad := func(msg string) (aitodo.Fields, error) { return nil, errs.New(errs.CodeInvalidArgument, msg) }
	p := aitodo.Fields{}
	hasJSON := cmd.Flags().Changed("json-file")
	if hasJSON {
		for f := range todoFlags {
			if cmd.Flags().Changed(f) {
				return bad("--json-file cannot be combined with field flags")
			}
		}
		if cmd.Flags().Changed("confirmed") {
			return bad("--json-file cannot be combined with --confirmed")
		}
		var reader io.Reader
		var file *os.File
		if flagStr(cmd, "json-file") == "-" {
			reader = cmd.InOrStdin()
		} else {
			var err error
			file, err = os.Open(flagStr(cmd, "json-file"))
			if err != nil {
				return bad("Cannot read JSON file")
			}
			defer file.Close()
			reader = file
		}
		dec := json.NewDecoder(io.LimitReader(reader, 1024*1024))
		dec.UseNumber()
		if dec.Decode(&p) != nil || p == nil {
			return bad("Expected a JSON object")
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return bad("Expected exactly one JSON object")
		}
	} else {
		for f, k := range todoFlags {
			if !cmd.Flags().Changed(f) {
				continue
			}
			s := flagStr(cmd, f)
			switch k {
			case "title", "keyword":
				p[k] = s
			case "isDone", "isFullDay":
				if s == "true" {
					p[k] = true
				} else if s == "false" {
					p[k] = false
				} else if s == "null" && k == "isDone" {
					p[k] = nil
				} else {
					return bad(f + " expects true or false (list is-done also accepts null)")
				}
			case "pageNo", "pageSize":
				if s == "null" && k == "pageSize" {
					p[k] = nil
				} else {
					n := json.Number(s)
					if _, err := n.Int64(); err != nil {
						return bad(f + " expects an integer")
					}
					p[k] = n
				}
			default:
				if s == "null" {
					p[k] = nil
				} else {
					n := json.Number(s)
					if _, err := n.Int64(); err == nil {
						p[k] = n
					} else {
						p[k] = s
					}
				}
			}
		}
	}
	action := cmd.Name()
	if action == "get" || action == "update" {
		if len(args) > 0 {
			if id, exists := p["todoId"]; exists && id != args[0] {
				return bad("Positional todoId differs from JSON todoId")
			}
			p["todoId"] = args[0]
		}
	}
	if action == "delete" {
		if hasJSON && len(args) > 0 {
			return bad("Do not combine positional IDs with --json-file")
		}
		if !hasJSON {
			p["todoIds"] = args
			p["confirmed"] = flagBool(cmd, "confirmed")
		}
	}
	if action == "create" && !hasJSON {
		if _, exists := p["dueAt"]; !exists {
			p["dueAt"] = nil
		}
		if _, exists := p["isFullDay"]; !exists {
			p["isFullDay"] = false
		}
	}
	return p, nil
}
func todoRun(ctx *clictx.Context, cmd *cobra.Command, args []string) (any, error) {
	p, err := todoParams(cmd, args)
	if err != nil {
		return nil, err
	}
	key := ""
	if cmd.Name() == "create" {
		key = flagStr(cmd, "request-key")
		if strings.TrimSpace(key) == "" {
			if cmd.Flags().Changed("request-key") {
				return nil, errs.New(errs.CodeInvalidArgument, "request-key must not be empty")
			}
			key, err = aitodo.NewRequestKey()
			if err != nil {
				return nil, err
			}
		}
	}
	client := aitodo.Client{APIKey: creds.ResolveAPIKey().Value, Host: cloudHost(ctx)}
	data, err := client.Execute(cmd.Context(), cmd.Name(), p, key)
	if err != nil {
		data = aitodo.Payload(err)
	}
	if key != "" {
		data["requestKey"] = key
	}
	if data["ok"] == false {
		exitCode = 1
	}
	return data, nil
}
