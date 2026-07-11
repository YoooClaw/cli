package yclib_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/YoooClaw/cli/yclib"
)

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, _ := json.Marshal(v)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestImages_ListGet(t *testing.T) {
	root := t.TempDir()
	idx := filepath.Join(root, "profiles", "default", "images", "index.json")
	writeJSONFile(t, idx, map[string]any{"images": []map[string]any{
		{"imageId": "img1", "status": "synced", "metadata": map[string]any{"created_at": "2026-06-22T10:00:00+08:00", "source_app": "com.feishu"}},
		{"imageId": "img2", "status": "pending", "metadata": map[string]any{"created_at": "2026-06-22T12:00:00+08:00", "source_app": "com.wechat"}},
	}})
	c, _ := yclib.New(yclib.Config{RootDir: root})
	ctx := context.Background()

	all, err := c.Images().List(ctx, yclib.ImageListOpts{})
	if err != nil || len(all) != 2 {
		t.Fatalf("List = %d (%v), want 2", len(all), err)
	}
	// 倒序：img2(12:00) 在前。
	if all[0].ImageID != "img2" {
		t.Errorf("first = %s, want img2 (desc)", all[0].ImageID)
	}
	// 状态过滤。
	synced, _ := c.Images().List(ctx, yclib.ImageListOpts{Status: "synced"})
	if len(synced) != 1 || synced[0].ImageID != "img1" {
		t.Fatalf("status filter = %+v, want single img1", synced)
	}
	// Get + NotFound。
	if _, err := c.Images().Get(ctx, "img1"); err != nil {
		t.Fatalf("Get img1: %v", err)
	}
	if _, err := c.Images().Get(ctx, "nope"); yclib.ErrorCode(err) != yclib.CodeNotFound {
		t.Fatalf("Get missing ErrorCode = %q, want %q", yclib.ErrorCode(err), yclib.CodeNotFound)
	}
}

func TestRecordings_ListGet(t *testing.T) {
	root := t.TempDir()
	idx := filepath.Join(root, "profiles", "default", "recordings", "index.json")
	writeJSONFile(t, idx, map[string]any{"recordings": []map[string]any{
		{"id": "rec1", "status": "done", "metadata": map[string]any{}, "ingestedAt": "2026-06-22T10:00:00+08:00", "updatedAt": "2026-06-22T10:05:00+08:00"},
		{"id": "rec2", "status": "transcribing", "metadata": map[string]any{}, "ingestedAt": "2026-06-22T11:00:00+08:00", "updatedAt": "2026-06-22T11:00:00+08:00"},
	}})
	c, _ := yclib.New(yclib.Config{RootDir: root})
	ctx := context.Background()

	all, err := c.Recordings().List(ctx, yclib.RecordingListOpts{})
	if err != nil || len(all) != 2 {
		t.Fatalf("List = %d (%v), want 2", len(all), err)
	}
	done, _ := c.Recordings().List(ctx, yclib.RecordingListOpts{Status: "done"})
	if len(done) != 1 || done[0].ID != "rec1" {
		t.Fatalf("status filter = %+v, want single rec1", done)
	}
	if _, err := c.Recordings().Get(ctx, "rec2"); err != nil {
		t.Fatalf("Get rec2: %v", err)
	}
	if _, err := c.Recordings().Get(ctx, "nope"); yclib.ErrorCode(err) != yclib.CodeNotFound {
		t.Fatalf("Get missing ErrorCode = %q, want %q", yclib.ErrorCode(err), yclib.CodeNotFound)
	}
}

func TestLightRules_RequireAPIKey(t *testing.T) {
	c, _ := yclib.New(yclib.Config{RootDir: t.TempDir()})
	ctx := context.Background()
	if _, err := c.LightRules().List(ctx); yclib.ErrorCode(err) != yclib.CodeCredentialMissing {
		t.Fatalf("List ErrorCode = %q, want credential missing", yclib.ErrorCode(err))
	}
	if err := c.LightRules().Delete(ctx, "x"); yclib.ErrorCode(err) != yclib.CodeCredentialMissing {
		t.Fatalf("Delete ErrorCode = %q, want credential missing", yclib.ErrorCode(err))
	}
}

func TestTunnel_RequireDaemon(t *testing.T) {
	c, _ := yclib.New(yclib.Config{RootDir: t.TempDir()})
	ctx := context.Background()
	if _, err := c.Tunnel().Status(ctx, ""); yclib.ErrorCode(err) != yclib.CodeDaemonNotRunning {
		t.Fatalf("Status ErrorCode = %q, want %q", yclib.ErrorCode(err), yclib.CodeDaemonNotRunning)
	}
}

func TestNewCapabilities_ContextCancelled(t *testing.T) {
	c, _ := yclib.New(yclib.Config{RootDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Images().List(ctx, yclib.ImageListOpts{}); err == nil {
		t.Error("Images().List should honor cancelled ctx")
	}
	if _, err := c.Recordings().List(ctx, yclib.RecordingListOpts{}); err == nil {
		t.Error("Recordings().List should honor cancelled ctx")
	}
	if _, err := c.LightRules().List(ctx); err == nil {
		t.Error("LightRules().List should honor cancelled ctx")
	}
}
