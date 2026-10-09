package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/YoooClaw/cli/internal/fsutil"
)

const mediaHookMarker = " # yoooclaw-media-hook-v1"

// ConfigureClaudeMediaHooks merges only our owned hook commands. Other agent and
// runner settings are preserved. remove supports clean removal without touching
// hooks installed by Wuying or the user.
func ConfigureClaudeMediaHooks(target string, remove bool) error {
	return configureClaudeMediaHooks(filepath.Join(claudeHome(), "settings.json"), target, remove)
}

func configureClaudeMediaHooks(settingsPath, target string, remove bool) error {
	var settings map[string]json.RawMessage
	data, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("invalid Claude settings (not overwritten): %w", err)
		}
		if settings == nil {
			return fmt.Errorf("Claude settings must be an object")
		}
	} else {
		settings = map[string]json.RawMessage{}
	}
	hooks := map[string][]map[string]json.RawMessage{}
	if raw, ok := settings["hooks"]; ok {
		if err = json.Unmarshal(raw, &hooks); err != nil || hooks == nil {
			return fmt.Errorf("invalid Claude hooks (not overwritten)")
		}
	}
	abs, err := filepath.Abs(filepath.Join(target, "yoooclaw-media-generate", "scripts"))
	if err != nil {
		return err
	}
	if !remove {
		body, err := os.ReadFile(filepath.Join(target, "yoooclaw-media-generate", "SKILL.md"))
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "\nhooks:") {
			return fmt.Errorf("media skill still has local hooks; reinstall skills with --force before global registration")
		}
		for _, name := range []string{"execution_guard.py", "link_guard.py"} {
			if _, err := os.Stat(filepath.Join(abs, name)); err != nil {
				return fmt.Errorf("media hook missing; reinstall skills with --force: %w", err)
			}
		}
	}
	// Remove our commands even from mixed groups, retaining every foreign entry.
	for event, groups := range hooks {
		kept := make([]map[string]json.RawMessage, 0, len(groups))
		for _, group := range groups {
			var handlers []json.RawMessage
			if err := json.Unmarshal(group["hooks"], &handlers); err != nil {
				return fmt.Errorf("invalid Claude hook group: %w", err)
			}
			remaining := make([]json.RawMessage, 0, len(handlers))
			owned := false
			for _, handler := range handlers {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(handler, &fields); err != nil {
					return err
				}
				var command string
				_ = json.Unmarshal(fields["command"], &command)
				if strings.HasSuffix(command, mediaHookMarker) {
					owned = true
					continue
				}
				remaining = append(remaining, handler)
			}
			if !owned {
				kept = append(kept, group)
				continue
			}
			if len(remaining) > 0 {
				group["hooks"], _ = json.Marshal(remaining)
				kept = append(kept, group)
			}
		}
		hooks[event] = kept
	}
	if !remove {
		for _, entry := range []struct{ event, script, matcher string }{{"PreToolUse", "execution_guard.py", "Bash"}, {"Stop", "execution_guard.py", ""}, {"Stop", "link_guard.py", ""}} {
			path := filepath.ToSlash(filepath.Join(abs, entry.script))
			command := "python3 '" + strings.ReplaceAll(path, "'", `'\''`) + "'" + mediaHookMarker
			handler, _ := json.Marshal([]any{map[string]any{"type": "command", "command": command, "timeout": 10}})
			group := map[string]json.RawMessage{"hooks": handler}
			if entry.matcher != "" {
				group["matcher"], _ = json.Marshal(entry.matcher)
			}
			hooks[entry.event] = append(hooks[entry.event], group)
		}
	}
	settings["hooks"], _ = json.Marshal(hooks)
	updated, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	// Save a private, one-time backup before the first managed edit.
	if len(data) > 0 {
		backup, err := os.OpenFile(settingsPath+".before-yoooclaw-hooks", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, werr := backup.Write(data)
			cerr := backup.Close()
			if werr != nil {
				return werr
			}
			if cerr != nil {
				return cerr
			}
		} else if !os.IsExist(err) {
			return err
		}
	}
	return fsutil.WriteAtomic(settingsPath, append(updated, '\n'), 0600)
}
