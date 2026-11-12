package transfer

import (
	"encoding/json"
	"github.com/YoooClaw/cli/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxLiteralPathImport(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("POSIX filename semantics")
	}
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	pkg := t.TempDir()
	name := "notes/report\\draft.txt"
	body := []byte("literal backslash")
	h := sha(string(body))
	testutil.WriteFile(t, filepath.Join(pkg, "blobs", h), body)
	writeJSON(t, filepath.Join(pkg, "manifest.json"), Manifest{SchemaVersion: 2, PackageID: newUUID(), OriginalBytes: int64(len(body)), Files: []FullFile{{Category: "workspace", Scope: "main", Path: name, Hash: h, Bytes: int64(len(body))}}})
	r := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if r["state"] != "SUCCEEDED" {
		t.Fatal(r)
	}
	got, e := os.ReadFile(filepath.Join(dst.svc.Home, workspaceBase(), name))
	if e != nil || string(got) != string(body) {
		t.Fatalf("%s %v", got, e)
	}
	for _, p := range []string{"../escape", "/absolute", "a/../escape", "a//b"} {
		if validRelative(p) {
			t.Fatalf("accepted unsafe path %q", p)
		}
	}
}
func TestPluginLinuxPathArchive(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("POSIX filename semantics")
	}
	p := os.Getenv("TRANSFER_PATH_FIXTURE")
	if p == "" {
		p = filepath.Join("testdata", "plugin-linux-path.tar.gz")
	}
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	r := runPlan(t, dst.svc, stagePlan(t, dst.svc, p))
	if r["state"] != "SUCCEEDED" {
		t.Fatal(r)
	}
	b, e := os.ReadFile(filepath.Join(dst.svc.Home, workspaceBase(), "report\\draft.txt"))
	if e != nil || string(b) != "literal backslash" {
		t.Fatalf("%s %v", b, e)
	}
}

func TestRelativePathPlatforms(t *testing.T) {
	for _, p := range []string{"a/b.txt", "CONSOLE.txt", "com10.txt", "COM12", "LPT23"} {
		for _, windows := range []bool{false, true} {
			if !compatibleRelative(p, windows) {
				t.Errorf("rejected %q windows=%v", p, windows)
			}
		}
	}
	for _, p := range []string{"a\nb", "a\rb", "a\tb", "../escape", "a/../escape", "/absolute", "a//b", "a\x00b"} {
		for _, windows := range []bool{false, true} {
			if compatibleRelative(p, windows) {
				t.Errorf("accepted %q windows=%v", p, windows)
			}
		}
	}
	for _, p := range []string{`a\b`, "CON", "con.txt", "PRN", "AUX", "NUL", "COM1", "LPT9.txt", "COM¹", "CONIN$", "foo.", "foo ", "a:b", "a<b", "a>b", `a"b`, "a|b", "a?b", "a*b"} {
		if compatibleRelative(p, true) {
			t.Errorf("Windows accepted %q", p)
		}
		if !compatibleRelative(p, false) {
			t.Errorf("POSIX rejected %q", p)
		}
	}
}

func TestWindowsUnsupportedFileDoesNotBlockPackage(t *testing.T) {
	root := t.TempDir()
	h := sha("data")
	testutil.WriteFile(t, filepath.Join(root, "blobs", h), []byte("data"))
	m := Manifest{SchemaVersion: 2, PackageID: newUUID(), OriginalBytes: 4, Files: []FullFile{}}
	for _, name := range []string{"good.txt", `bad\name`, "CON", "foo."} {
		m.Files = append(m.Files, FullFile{Category: "workspace", Scope: "main", Path: name, Hash: h, Bytes: 4})
	}
	raw, _ := json.Marshal(m)
	result, err := readFullPackageForPlatform(root, raw, ReadOptions{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "good.txt" || len(result.Warnings) != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestWindowsUnsupportedPathImport(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("Windows filesystem")
	}
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	pkg := t.TempDir()
	h := sha("ok")
	testutil.WriteFile(t, filepath.Join(pkg, "blobs", h), []byte("ok"))
	files := []FullFile{}
	for _, name := range []string{"good.txt", "CON", `bad\name`, "foo."} {
		files = append(files, FullFile{Category: "workspace", Scope: "main", Path: name, Hash: h, Bytes: 2})
	}
	writeJSON(t, filepath.Join(pkg, "manifest.json"), Manifest{SchemaVersion: 2, PackageID: newUUID(), OriginalBytes: 2, Files: files})
	result := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if result["state"] != "SUCCEEDED" {
		t.Fatal(result)
	}
	data, err := os.ReadFile(filepath.Join(dst.svc.Home, workspaceBase(), "good.txt"))
	if err != nil || string(data) != "ok" {
		t.Fatalf("%q %v", data, err)
	}
}

func TestLinuxPathImportDestinations(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("POSIX filename semantics")
	}
	for _, tc := range []struct {
		name, category, scope, path, dest string
		conflict                          bool
	}{
		{"skill asset", "skills", "main", `demo/assets/a\b.txt`, `.claude/skills/demo/assets/a\b.txt`, false},
		{"memory", "memory", "main", `memory/a\b.md`, workspaceBase() + `/memory/a\b.md`, false},
		{"daily memory", "memory", "main", "memory/2026-11-12.md", "", false},
		{"other agent", "agent-backup", "other", `other-agents/other/workspace/a\b.txt`, workspaceBase() + `/other-agents/other/workspace/a\b.txt`, false},
		{"other cron", "agent-backup", "other", "other-agents/other/cron/jobs.json", workspaceBase() + "/other-agents/other/cron/from_openclaw_cron.json", false},
		{"conflict", "workspace", "main", `a\b.txt`, workspaceBase() + `/a\b.txt`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := newEnv(t)
			dst.svc.Home = t.TempDir()
			pkg := t.TempDir()
			h := sha("content")
			id := newUUID()
			file := FullFile{Category: tc.category, Scope: tc.scope, Path: tc.path, Hash: h, Bytes: 7}
			m := Manifest{SchemaVersion: 2, PackageID: id, OriginalBytes: 7, Files: []FullFile{file}}
			testutil.WriteFile(t, filepath.Join(pkg, "blobs", h), []byte("content"))
			if tc.category == "skills" {
				m.Files = append(m.Files, FullFile{Category: "skills", Scope: "main", Path: "demo/SKILL.md", Hash: h, Bytes: 7})
			}
			dest := tc.dest
			if tc.name == "daily memory" {
				dest = projectMemory(dst.svc.Home) + "/2026-11-12.md"
			}
			if tc.conflict {
				testutil.WriteFile(t, filepath.Join(dst.svc.Home, dest), []byte("existing"))
			}
			writeJSON(t, filepath.Join(pkg, "manifest.json"), m)
			result := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
			if result["state"] != "SUCCEEDED" {
				t.Fatal(result)
			}
			if tc.conflict {
				old, _ := os.ReadFile(filepath.Join(dst.svc.Home, dest))
				if string(old) != "existing" {
					t.Fatal("overwrote existing")
				}
				dest = conflictDestination(&m, file)
			}
			data, err := os.ReadFile(filepath.Join(dst.svc.Home, dest))
			if err != nil || string(data) != "content" {
				t.Fatalf("%q %v destination=%s", data, err, dest)
			}
		})
	}
}
