package monitor

import (
	"testing"

	"github.com/YoooClaw/cli/internal/testutil"
)

func validInput(name string) CreateInput {
	return CreateInput{
		Name:        name,
		Description: "desc",
		MatchRules:  map[string]any{"app": "wechat"},
		Schedule:    "0 9 * * *",
	}
}

func TestListEmpty(t *testing.T) {
	p := testutil.Sandbox(t)
	if got := List(p); len(got) != 0 {
		t.Errorf("empty store -> [], got %+v", got)
	}
}

func TestCreateGetList(t *testing.T) {
	p := testutil.Sandbox(t)
	task, err := Create(p, validInput("daily"))
	if err != nil {
		t.Fatal(err)
	}
	if !task.Enabled || task.CreatedAt == "" || task.UpdatedAt == "" {
		t.Errorf("created task fields wrong: %+v", task)
	}
	got, ok := Get(p, "daily")
	if !ok || got.Name != "daily" || got.Schedule != "0 9 * * *" {
		t.Errorf("Get mismatch: %+v ok=%v", got, ok)
	}
	if len(List(p)) != 1 {
		t.Error("list should have 1 task")
	}
	if _, ok := Get(p, "missing"); ok {
		t.Error("Get missing should be false")
	}
}

func TestCreateValidation(t *testing.T) {
	p := testutil.Sandbox(t)
	bad := []CreateInput{
		{Description: "d", MatchRules: 1, Schedule: "s"},          // 无 name
		{Name: "n", MatchRules: 1, Schedule: "s"},                 // 无 description
		{Name: "n", Description: "d", Schedule: "s"},              // 无 matchRules
		{Name: "n", Description: "d", MatchRules: 1},              // 无 schedule
	}
	for i, in := range bad {
		if _, err := Create(p, in); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestCreateDuplicate(t *testing.T) {
	p := testutil.Sandbox(t)
	if _, err := Create(p, validInput("dup")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(p, validInput("dup")); err == nil {
		t.Error("duplicate name should error")
	}
}

func TestDelete(t *testing.T) {
	p := testutil.Sandbox(t)
	Create(p, validInput("a"))
	Create(p, validInput("b"))
	removed, err := Delete(p, "a")
	if err != nil || !removed {
		t.Fatalf("delete a: removed=%v err=%v", removed, err)
	}
	if _, ok := Get(p, "a"); ok {
		t.Error("a should be gone")
	}
	if _, ok := Get(p, "b"); !ok {
		t.Error("b should remain")
	}
	removed, err = Delete(p, "missing")
	if err != nil || removed {
		t.Errorf("delete missing -> (false,nil), got (%v,%v)", removed, err)
	}
}

func TestSetEnabled(t *testing.T) {
	p := testutil.Sandbox(t)
	Create(p, validInput("x"))
	ok, err := SetEnabled(p, "x", false)
	if err != nil || !ok {
		t.Fatalf("SetEnabled: ok=%v err=%v", ok, err)
	}
	got, _ := Get(p, "x")
	if got.Enabled {
		t.Error("task should be disabled")
	}
	ok, err = SetEnabled(p, "missing", true)
	if err != nil || ok {
		t.Errorf("SetEnabled missing -> (false,nil), got (%v,%v)", ok, err)
	}
}

func TestListIgnoresInvalidFile(t *testing.T) {
	p := testutil.Sandbox(t)
	testutil.WriteFile(t, storePath(p), []byte("{bad json"))
	if got := List(p); len(got) != 0 {
		t.Errorf("invalid file -> [], got %+v", got)
	}
}
