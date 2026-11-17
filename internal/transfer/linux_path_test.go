package transfer

import (
	"encoding/json"
	"github.com/YoooClaw/cli/internal/testutil"
	"os"
	"path/filepath"
	"strings"
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
	for _, p := range []string{`a\b`, "CON", "con.txt", "PRN", "AUX", "NUL", "COM1", "LPT9.txt", "COM¹", "CONIN$", "CLOCK$", "foo.", "foo ", "a:b", "a<b", "a>b", `a"b`, "a|b", "a?b", "a*b"} {
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

func TestWindowsAssetReferenceToSkippedFile(t *testing.T) {
	for _, windows := range []bool{false, true} {
		root := t.TempDir()
		m := Manifest{SchemaVersion: 2, PackageID: newUUID(), Files: []FullFile{}}
		add := func(name string, content []byte) {
			h := sha(string(content))
			testutil.WriteFile(t, filepath.Join(root, "blobs", h), content)
			m.Files = append(m.Files, FullFile{Category: TypeRecordings, Scope: "main", Path: name, Hash: h, Bytes: int64(len(content))})
			m.OriginalBytes += int64(len(content))
		}
		index, _ := json.Marshal(map[string]any{"recordings": []map[string]any{{"id": "bad", "metadata": map[string]any{"name": "bad", "created_at": "2026-11-12T00:00:00Z"}, "audioFile": `report\draft.m4a`}, {"id": "good", "metadata": map[string]any{"name": "good", "created_at": "2026-11-12T00:00:00Z"}, "audioFile": "good.m4a"}}})
		add("index.json", index)
		add(`report\draft.m4a`, []byte("bad-audio"))
		add("good.m4a", []byte("good-audio"))
		raw, _ := json.Marshal(m)
		result, err := readFullPackageForPlatform(root, raw, ReadOptions{}, windows)
		if err != nil {
			t.Fatal(err)
		}
		want := 2
		if windows {
			want = 1
		}
		if len(result.Records) != want {
			t.Fatalf("windows=%v records=%+v warnings=%v", windows, result.Records, result.Warnings)
		}
		if windows {
			if result.Records[0].RecordID != "good" {
				t.Fatal(result.Records)
			}
			warnings := strings.Join(result.Warnings, "\n")
			for _, expected := range []string{"REFERENCED_FILE_MISSING", `category="recordings"`, `scope="main"`} {
				if !strings.Contains(warnings, expected) {
					t.Fatalf("missing %s in %s", expected, warnings)
				}
			}
		}
		// A genuinely unsafe reference remains a structural error, not a platform skip.
		index = []byte(`{"recordings":[{"id":"unsafe","metadata":{"name":"unsafe","created_at":"2026-11-12T00:00:00Z"},"audioFile":"../outside.m4a"}]}`)
		add("new-index", index)
		m.Files[0].Hash = sha(string(index))
		m.Files[0].Bytes = int64(len(index))
		m.Files = m.Files[:len(m.Files)-1]
		raw, _ = json.Marshal(m)
		_, err = readFullPackageForPlatform(root, raw, ReadOptions{}, windows)
		if ErrorCode(err) != "UNSAFE_PATH" {
			t.Fatalf("unsafe reference: %v", err)
		}
	}
}
