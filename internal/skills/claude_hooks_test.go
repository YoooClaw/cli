package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeHooksMergeReinstallRemove(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "skills")
	if _, _, err := Install(target, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "settings.json")
	original := `{"env":{"KEEP":"yes"},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"runner audit"}]}]}}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := configureClaudeMediaHooks(path, target, false); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		data, _ := os.ReadFile(path)
		var x map[string]any
		if err := json.Unmarshal(data, &x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	x := read()
	hooks := x["hooks"].(map[string]any)
	if len(hooks["PreToolUse"].([]any)) != 2 || len(hooks["Stop"].([]any)) != 2 {
		t.Fatal("duplicate or missing hooks", hooks)
	}
	if x["env"].(map[string]any)["KEEP"] != "yes" {
		t.Fatal("lost unrelated settings")
	}
	if err := configureClaudeMediaHooks(path, target, true); err != nil {
		t.Fatal(err)
	}
	hooks = read()["hooks"].(map[string]any)
	if len(hooks["PreToolUse"].([]any)) != 1 || len(hooks["Stop"].([]any)) != 0 {
		t.Fatal("removed foreign hooks")
	}
	backup, _ := os.ReadFile(path + ".before-yoooclaw-hooks")
	if string(backup) != original {
		t.Fatal("backup changed")
	}
}

func TestClaudeHooksInvalidJSONPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := []byte("{invalid")
	os.WriteFile(path, original, 0600)
	if err := configureClaudeMediaHooks(path, "", true); err == nil {
		t.Fatal("accepted corrupt settings")
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(original) {
		t.Fatal("overwrote corrupt settings")
	}
}

func TestClaudeHooksFreshIndependentHomes(t *testing.T) {
	for _, name := range []string{"new-user-a", "another user's desktop"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), name, ".claude")
			target := filepath.Join(root, "skills")
			if _, _, err := Install(target, false); err != nil {
				t.Fatal(err)
			}
			settings := filepath.Join(root, "settings.json")
			for i := 0; i < 2; i++ {
				if err := configureClaudeMediaHooks(settings, target, false); err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(settings)
			if err != nil {
				t.Fatal(err)
			}
			var x map[string]any
			if err := json.Unmarshal(data, &x); err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, groups := range x["hooks"].(map[string]any) {
				for _, group := range groups.([]any) {
					for _, handler := range group.(map[string]any)["hooks"].([]any) {
						command := handler.(map[string]any)["command"].(string)
						if !strings.HasSuffix(command, mediaHookMarker) {
							t.Fatal("missing ownership marker")
						}
						expected := strings.ReplaceAll(filepath.ToSlash(target), "'", `'\''`)
						if !strings.Contains(command, expected) {
							t.Fatalf("hook references wrong installation: %s", command)
						}
						count++
					}
				}
			}
			if count != 3 {
				t.Fatalf("got %d hooks", count)
			}
		})
	}
}
