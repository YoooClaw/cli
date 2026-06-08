package lightrule

import (
	"os"
	"path/filepath"
	"testing"
)

func segs() []map[string]any {
	return []map[string]any{{"mode": "static", "color": []any{255.0, 0.0, 0.0}}}
}

func boolPtr(b bool) *bool { return &b }

func TestCreateGetList(t *testing.T) {
	base := t.TempDir()
	m, err := Create(base, CreateParams{Name: "alarm", Title: "Alarm", Description: "red", Segments: segs(), Repeat: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "alarm" || m.Type != "light-rule" || !m.Enabled || m.RepeatTimes != 0 {
		t.Errorf("created meta wrong: %+v", m)
	}
	if m.CreatedAt == "" {
		t.Error("createdAt should be set")
	}
	got := Get(base, "alarm")
	if got == nil || got.Title != "Alarm" {
		t.Errorf("Get mismatch: %+v", got)
	}
	// .json 后缀也能解析
	if Get(base, "alarm.json") == nil {
		t.Error("lookup with .json suffix should resolve")
	}
	if list := List(base); len(list) != 1 {
		t.Errorf("list len = %d", len(list))
	}
}

func TestCreateDuplicate(t *testing.T) {
	base := t.TempDir()
	if _, err := Create(base, CreateParams{Name: "dup", Segments: segs(), Repeat: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	_, err := Create(base, CreateParams{Name: "dup", Segments: segs(), Repeat: boolPtr(false)})
	e, ok := err.(*Error)
	if !ok || e.Code != "ALREADY_EXISTS" {
		t.Errorf("want ALREADY_EXISTS, got %v", err)
	}
}

func TestCreateInvalidRepeatTimes(t *testing.T) {
	base := t.TempDir()
	rt := 2.0 // ANCS 仅支持 0/1
	if _, err := Create(base, CreateParams{Name: "x", Segments: segs(), RepeatTimes: &rt}); err == nil {
		t.Error("repeat_times=2 should be rejected on ANCS path")
	}
}

func TestUpdate(t *testing.T) {
	base := t.TempDir()
	Create(base, CreateParams{Name: "u", Title: "old", Description: "d", Segments: segs(), Repeat: boolPtr(false)})
	newTitle := "new"
	m, err := Update(base, UpdateParams{Name: "u", Title: &newTitle, Enabled: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "new" || m.Enabled || m.UpdatedAt == "" {
		t.Errorf("update result wrong: %+v", m)
	}
	// segments 仅当 HasSegments 时替换
	newSegs := []map[string]any{{"mode": "static", "color": []any{0.0, 255.0, 0.0}}}
	m, err = Update(base, UpdateParams{Name: "u", Segments: newSegs, HasSegments: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 1 {
		t.Errorf("segments not updated: %+v", m.Segments)
	}
}

func TestUpdateNotFound(t *testing.T) {
	base := t.TempDir()
	_, err := Update(base, UpdateParams{Name: "ghost"})
	e, ok := err.(*Error)
	if !ok || e.Code != "NOT_FOUND" {
		t.Errorf("want NOT_FOUND, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	base := t.TempDir()
	Create(base, CreateParams{Name: "d", Segments: segs(), Repeat: boolPtr(false)})
	name, err := Delete(base, "d")
	if err != nil || name != "d" {
		t.Fatalf("delete: name=%q err=%v", name, err)
	}
	if Get(base, "d") != nil {
		t.Error("rule should be gone")
	}
	_, err = Delete(base, "d")
	if e, ok := err.(*Error); !ok || e.Code != "NOT_FOUND" {
		t.Errorf("deleting missing should be NOT_FOUND, got %v", err)
	}
}

func TestListEmptyAndMissingDir(t *testing.T) {
	base := t.TempDir()
	if got := List(base); len(got) != 0 {
		t.Errorf("empty -> [], got %+v", got)
	}
}

func TestResolveByDirScanWhenNameDiffers(t *testing.T) {
	base := t.TempDir()
	// 目录名与 meta.name 不一致：写到目录 dirX，但 name=ruleY
	taskDir := filepath.Join(tasksDir(base), "dirX")
	m := &Meta{Name: "ruleY", Title: "Y", Type: "light-rule", Segments: segs(), Enabled: true}
	if err := writeMeta(taskDir, m); err != nil {
		t.Fatal(err)
	}
	if got := Get(base, "ruleY"); got == nil || got.Name != "ruleY" {
		t.Errorf("should resolve by scanning dirs: %+v", got)
	}
}

func TestReadMetaRejectsWrongType(t *testing.T) {
	base := t.TempDir()
	taskDir := filepath.Join(tasksDir(base), "bad")
	os.MkdirAll(taskDir, 0o700)
	os.WriteFile(filepath.Join(taskDir, "meta.json"), []byte(`{"type":"not-a-rule","segments":[]}`), 0o600)
	if Get(base, "bad") != nil {
		t.Error("non light-rule type should be ignored")
	}
	if len(List(base)) != 0 {
		t.Error("List should skip non light-rule")
	}
}
