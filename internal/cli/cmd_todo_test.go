package cli

import (
	"github.com/YoooClaw/cli/internal/aitodo"
	"strings"
	"testing"
)

func TestTodoStructuredInputs(t *testing.T) {
	for _, tc := range []struct {
		argv  []string
		stdin string
		bad   bool
	}{
		{[]string{"update", "--json-file", "-"}, `{"todoId":"9007199254740993","dueAt":null,"isDone":false}`, false},
		{[]string{"update", "1", "--json-file", "-"}, `{"todoId":"2","title":"x"}`, true},
		{[]string{"create", "--json-file", "-", "--title", "x"}, `{}`, true},
		{[]string{"create", "--json-file", "-"}, `null`, true},
		{[]string{"create", "--json-file", "-"}, `{} {}`, true},
		{[]string{"delete", "1", "--json-file", "-"}, `{"todoIds":["2"],"confirmed":true}`, true},
		{[]string{"delete", "1", "--confirmed"}, ``, false},
		{[]string{"list", "--is-done", "false", "--page-size", "20"}, ``, false},
	} {
		root := newTodoCmd()
		cmd, args, e := root.Find(tc.argv)
		if e != nil {
			t.Fatal(e)
		}
		if e = cmd.ParseFlags(args); e != nil {
			t.Fatal(e)
		}
		cmd.SetIn(strings.NewReader(tc.stdin))
		p, e := todoParams(cmd, cmd.Flags().Args())
		if (e != nil) != tc.bad {
			t.Fatalf("%v: %v", tc.argv, e)
		}
		if e == nil {
			if _, e = aitodo.Validate(cmd.Name(), p); e != nil {
				t.Fatal(p, e)
			}
		}
	}
}
func TestTodoCLIUndatedAndBoolean(t *testing.T) {
	root := newTodoCmd()
	cmd, args, _ := root.Find([]string{"create", "--title", "照片"})
	cmd.ParseFlags(args)
	p, e := todoParams(cmd, nil)
	if e != nil || p["dueAt"] != nil || p["isFullDay"] != false {
		t.Fatal(p, e)
	}
	root = newTodoCmd()
	cmd, args, _ = root.Find([]string{"update", "1", "--is-done", "false"})
	cmd.ParseFlags(args)
	p, e = todoParams(cmd, cmd.Flags().Args())
	if e != nil || p["isDone"] != false {
		t.Fatal(p, e)
	}
	if _, exists := p["dueAt"]; exists {
		t.Fatal("omitted time became clear")
	}
}
