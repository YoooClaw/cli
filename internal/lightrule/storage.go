// Package lightrule 提供灯效规则的本地文件存储（meta.json CRUD），
// 对齐 src/vendor/light-rules/storage.ts。规则落在 <base>/tasks/<name>/meta.json。
package lightrule

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/light"
)

// Error 携带稳定错误码（ALREADY_EXISTS / NOT_FOUND）。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Meta 是单条灯效规则元数据（meta.json）。
type Meta struct {
	Name        string           `json:"name"`
	Title       string           `json:"title"`
	Type        string           `json:"type"`
	Description string           `json:"description"`
	Segments    []map[string]any `json:"segments"`
	RepeatTimes int              `json:"repeat_times"`
	Enabled     bool             `json:"enabled"`
	CreatedAt   string           `json:"createdAt"`
	UpdatedAt   string           `json:"updatedAt,omitempty"`
}

// CreateParams 是创建规则的入参。
type CreateParams struct {
	Name        string
	Title       string
	Description string
	Segments    []map[string]any
	Repeat      *bool
	RepeatTimes *float64
}

// UpdateParams 是更新规则的入参（指针字段为 nil 表示不改）。
type UpdateParams struct {
	Name        string
	Title       *string
	Description *string
	Segments    []map[string]any
	HasSegments bool
	Repeat      *bool
	RepeatTimes *float64
	Enabled     *bool
}

// 写路径串行化锁（与 TS registry 的 write chain 等价的最朴素 mutex）。
var writeMu sync.Mutex

func tasksDir(base string) string { return filepath.Join(base, "tasks") }

func normalizeLookupName(name string) string {
	n := strings.TrimSpace(name)
	if strings.HasSuffix(strings.ToLower(n), ".json") {
		n = n[:len(n)-len(".json")]
	}
	return n
}

func readMeta(taskDir string) *Meta {
	raw, err := os.ReadFile(filepath.Join(taskDir, "meta.json"))
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	if t, _ := m["type"].(string); t != "light-rule" {
		return nil
	}
	segsRaw, ok := m["segments"].([]any)
	if !ok {
		return nil
	}
	segs := make([]map[string]any, 0, len(segsRaw))
	for _, s := range segsRaw {
		if sm, ok := s.(map[string]any); ok {
			segs = append(segs, sm)
		}
	}

	name := optStr(m["name"])
	if name == "" {
		name = filepath.Base(taskDir)
	}
	title := optStr(m["title"])
	if title == "" {
		title = name
	}
	description := optStr(m["description"])
	if description == "" {
		description = name
	}
	createdAt := optStr(m["createdAt"])
	if createdAt == "" {
		if info, err := os.Stat(filepath.Join(taskDir, "meta.json")); err == nil {
			createdAt = info.ModTime().UTC().Format(time.RFC3339)
		}
	}
	enabled := true
	if b, ok := m["enabled"].(bool); ok {
		enabled = b
	}
	repeatTimes, err := light.NormalizeRepeatTimes(light.RepeatInputFromAny(m["repeat"], m["repeat_times"]))
	if err != nil {
		return nil
	}
	if light.AssertAncsRepeatTimes(repeatTimes) != nil {
		return nil
	}
	updatedAt := optStr(m["updatedAt"])

	return &Meta{
		Name: name, Title: title, Type: "light-rule", Description: description,
		Segments: segs, RepeatTimes: repeatTimes, Enabled: enabled,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func writeMeta(taskDir string, m *Meta) error {
	if err := fsutil.EnsureDir(taskDir, fsutil.DirMode); err != nil {
		return err
	}
	return fsutil.WriteJSON(filepath.Join(taskDir, "meta.json"), m, fsutil.ConfigFileMode)
}

type resolved struct {
	taskDir string
	meta    *Meta
}

func resolve(base, name string) *resolved {
	norm := normalizeLookupName(name)
	if norm == "" {
		return nil
	}
	dir := tasksDir(base)
	if meta := readMeta(filepath.Join(dir, norm)); meta != nil {
		return &resolved{taskDir: filepath.Join(dir, norm), meta: meta}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		taskDir := filepath.Join(dir, e.Name())
		if meta := readMeta(taskDir); meta != nil && meta.Name == norm {
			return &resolved{taskDir: taskDir, meta: meta}
		}
	}
	return nil
}

// List 返回全部规则（含 disabled），按目录扫描去重（首个 name 胜出）。
func List(base string) []Meta {
	out := []Meta{}
	seen := map[string]bool{}
	entries, err := os.ReadDir(tasksDir(base))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta := readMeta(filepath.Join(tasksDir(base), e.Name()))
		if meta == nil || seen[meta.Name] {
			continue
		}
		out = append(out, *meta)
		seen[meta.Name] = true
	}
	return out
}

// Get 按名精确查找；不存在返回 nil。
func Get(base, name string) *Meta {
	if r := resolve(base, name); r != nil {
		return r.meta
	}
	return nil
}

// Create 创建规则；同名已存在报 ALREADY_EXISTS。
func Create(base string, p CreateParams) (*Meta, error) {
	writeMu.Lock()
	defer writeMu.Unlock()

	if resolve(base, p.Name) != nil {
		return nil, &Error{Code: "ALREADY_EXISTS", Message: "灯效规则 '" + p.Name + "' 已存在"}
	}
	taskDir := filepath.Join(tasksDir(base), p.Name)
	if fsutil.Exists(taskDir) {
		return nil, &Error{Code: "ALREADY_EXISTS", Message: "灯效规则 '" + p.Name + "' 已存在"}
	}
	repeatTimes, err := light.NormalizeRepeatTimes(light.RepeatInput{Repeat: p.Repeat, RepeatTimes: p.RepeatTimes})
	if err != nil {
		return nil, err
	}
	if err := light.AssertAncsRepeatTimes(repeatTimes); err != nil {
		return nil, err
	}
	meta := &Meta{
		Name: p.Name, Title: p.Title, Type: "light-rule", Description: p.Description,
		Segments: p.Segments, RepeatTimes: repeatTimes, Enabled: true,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeMeta(taskDir, meta); err != nil {
		return nil, err
	}
	return meta, nil
}

// Update 更新规则；不存在报 NOT_FOUND。
func Update(base string, p UpdateParams) (*Meta, error) {
	writeMu.Lock()
	defer writeMu.Unlock()

	r := resolve(base, p.Name)
	if r == nil {
		return nil, &Error{Code: "NOT_FOUND", Message: "灯效规则 '" + p.Name + "' 不存在"}
	}
	meta := r.meta
	if p.Description != nil {
		meta.Description = *p.Description
	}
	if p.Title != nil {
		meta.Title = *p.Title
	}
	if p.HasSegments {
		meta.Segments = p.Segments
	}
	if p.Repeat != nil || p.RepeatTimes != nil {
		rt, err := light.NormalizeRepeatTimes(light.RepeatInput{Repeat: p.Repeat, RepeatTimes: p.RepeatTimes})
		if err != nil {
			return nil, err
		}
		if err := light.AssertAncsRepeatTimes(rt); err != nil {
			return nil, err
		}
		meta.RepeatTimes = rt
	}
	if p.Enabled != nil {
		meta.Enabled = *p.Enabled
	}
	meta.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeMeta(r.taskDir, meta); err != nil {
		return nil, err
	}
	return meta, nil
}

// Delete 删除规则；不存在报 NOT_FOUND。
func Delete(base, name string) (string, error) {
	writeMu.Lock()
	defer writeMu.Unlock()

	r := resolve(base, name)
	if r == nil {
		return "", &Error{Code: "NOT_FOUND", Message: "灯效规则 '" + name + "' 不存在"}
	}
	if err := os.RemoveAll(r.taskDir); err != nil {
		return "", err
	}
	return r.meta.Name, nil
}

func optStr(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
