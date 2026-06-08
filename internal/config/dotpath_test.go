package config

import (
	"reflect"
	"testing"
)

func TestGetByPath(t *testing.T) {
	t.Parallel()
	obj := map[string]any{
		"daemon": map[string]any{"port": 8080.0},
		"top":    "v",
	}
	tests := []struct {
		path   string
		want   any
		wantOK bool
	}{
		{"top", "v", true},
		{"daemon.port", 8080.0, true},
		{"daemon.missing", nil, false},
		{"nope.deep", nil, false},
		{"top.deeper", nil, false}, // top 是字符串，不能继续下钻
	}
	for _, tt := range tests {
		got, ok := GetByPath(obj, tt.path)
		if ok != tt.wantOK || (ok && !reflect.DeepEqual(got, tt.want)) {
			t.Errorf("GetByPath(%q) = (%v,%v), want (%v,%v)", tt.path, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestSetByPathCreatesIntermediate(t *testing.T) {
	t.Parallel()
	obj := map[string]any{}
	SetByPath(obj, "a.b.c", 42)
	got, ok := GetByPath(obj, "a.b.c")
	if !ok || got != 42 {
		t.Fatalf("SetByPath did not create nested value: %+v", obj)
	}
}

func TestSetByPathOverwrites(t *testing.T) {
	t.Parallel()
	obj := map[string]any{"a": map[string]any{"b": "old"}}
	SetByPath(obj, "a.b", "new")
	got, _ := GetByPath(obj, "a.b")
	if got != "new" {
		t.Errorf("overwrite failed: %v", got)
	}
}

func TestUnsetByPath(t *testing.T) {
	t.Parallel()
	obj := map[string]any{"a": map[string]any{"b": 1, "c": 2}}
	if !UnsetByPath(obj, "a.b") {
		t.Error("UnsetByPath should report removal")
	}
	if _, ok := GetByPath(obj, "a.b"); ok {
		t.Error("a.b should be gone")
	}
	if _, ok := GetByPath(obj, "a.c"); !ok {
		t.Error("a.c should remain")
	}
	if UnsetByPath(obj, "a.missing") {
		t.Error("removing nonexistent key should report false")
	}
	if UnsetByPath(obj, "x.y") {
		t.Error("removing through nonexistent parent should report false")
	}
}

func TestCoerceValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path    string
		raw     string
		want    any
		wantErr bool
	}{
		{"daemon.port", "8080", 8080.0, false},
		{"daemon.port", "abc", nil, true},
		{"daemon.detach", "true", true, false},
		{"daemon.detach", "false", false, false},
		{"daemon.detach", "yes", nil, true},
		{"notification.ignoredApps", "a, b ,,c", []any{"a", "b", "c"}, false},
		{"notification.retentionDays", "", nil, false},
		{"notification.retentionDays", "null", nil, false},
		{"some.unknown.path", "raw-string", "raw-string", false},
	}
	for _, tt := range tests {
		got, err := CoerceValue(tt.path, tt.raw)
		if tt.wantErr {
			if err == nil {
				t.Errorf("CoerceValue(%q,%q) expected error", tt.path, tt.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("CoerceValue(%q,%q) unexpected error: %v", tt.path, tt.raw, err)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("CoerceValue(%q,%q) = %#v, want %#v", tt.path, tt.raw, got, tt.want)
		}
	}
}

func TestDeepCloneMapIsolation(t *testing.T) {
	t.Parallel()
	src := map[string]any{"a": map[string]any{"b": 1}}
	clone := deepCloneMap(src)
	SetByPath(clone, "a.b", 999)
	if got, _ := GetByPath(src, "a.b"); got != 1 {
		t.Errorf("clone mutation leaked into source: %v", got)
	}
}
