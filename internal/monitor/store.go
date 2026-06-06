// Package monitor 持久化监控任务定义（state/monitors.json）。
//
// 本 build 负责定义 CRUD 与启用状态；cron 实时触发为后续迭代。
package monitor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/paths"
)

// Task 是一条监控任务。
type Task struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	MatchRules  any     `json:"matchRules"`
	Schedule    string  `json:"schedule"`
	Enabled     bool    `json:"enabled"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
	LastRunAt   *string `json:"lastRunAt"`
	LastResult  any     `json:"lastResult"`
}

// CreateInput 是创建任务的入参。
type CreateInput struct {
	Name        string
	Description string
	MatchRules  any
	Schedule    string
}

func storePath(p paths.Paths) string {
	return filepath.Join(p.State, "monitors.json")
}

// List 列出全部任务；不存在返回空。
func List(p paths.Paths) []Task {
	raw, err := os.ReadFile(storePath(p))
	if err != nil {
		return nil
	}
	var wrapper struct {
		Monitors []Task `json:"monitors"`
	}
	if json.Unmarshal(raw, &wrapper) != nil {
		return nil
	}
	return wrapper.Monitors
}

func save(p paths.Paths, monitors []Task) error {
	if err := fsutil.EnsureDir(p.State, fsutil.DirMode); err != nil {
		return err
	}
	return fsutil.WriteJSON(storePath(p), map[string]any{"monitors": monitors}, fsutil.ConfigFileMode)
}

// Get 按名查找任务。
func Get(p paths.Paths, name string) (Task, bool) {
	for _, m := range List(p) {
		if m.Name == name {
			return m, true
		}
	}
	return Task{}, false
}

// Create 新建任务。
func Create(p paths.Paths, in CreateInput) (Task, error) {
	if in.Name == "" || in.Description == "" || in.Schedule == "" || in.MatchRules == nil {
		return Task{}, errors.New("name / description / matchRules / schedule 均必填")
	}
	monitors := List(p)
	for _, m := range monitors {
		if m.Name == in.Name {
			return Task{}, errors.New("监控任务已存在：" + in.Name)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	task := Task{
		Name: in.Name, Description: in.Description, MatchRules: in.MatchRules, Schedule: in.Schedule,
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	monitors = append(monitors, task)
	if err := save(p, monitors); err != nil {
		return Task{}, err
	}
	return task, nil
}

// Delete 删除任务；返回是否删除。
func Delete(p paths.Paths, name string) (bool, error) {
	monitors := List(p)
	next := monitors[:0]
	for _, m := range monitors {
		if m.Name != name {
			next = append(next, m)
		}
	}
	if len(next) == len(monitors) {
		return false, nil
	}
	return true, save(p, next)
}

// SetEnabled 设置启用状态；返回是否命中。
func SetEnabled(p paths.Paths, name string, enabled bool) (bool, error) {
	monitors := List(p)
	found := false
	for i := range monitors {
		if monitors[i].Name == name {
			monitors[i].Enabled = enabled
			monitors[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			found = true
		}
	}
	if !found {
		return false, nil
	}
	return true, save(p, monitors)
}
