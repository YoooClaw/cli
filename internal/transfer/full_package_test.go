package transfer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/YoooClaw/cli/internal/testutil"
)

func fullFixture(t *testing.T) string {
	t.Helper()
	src := newEnv(t)
	seedSource(t, src.roots)
	// The deliberately malformed legacy test line is not valid in a full snapshot.
	testutil.WriteFile(t, filepath.Join(src.roots[TypeNotifications], "2026-09-20.jsonl"), []byte(""))
	pkg := t.TempDir()
	m := Manifest{SchemaVersion: 2, PackageID: newUUID(), Files: []FullFile{}}
	add := func(cat, scope, p string, b []byte) {
		h := sha(string(b))
		testutil.WriteFile(t, filepath.Join(pkg, "blobs", h), b)
		m.Files = append(m.Files, FullFile{cat, scope, p, h, int64(len(b))})
		m.OriginalBytes += int64(len(b))
	}
	for cat, root := range src.roots {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			add(cat, "main", filepath.ToSlash(rel), b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"USER.md", "SOUL.md", "IDENTITY.md", "AGENTS.md", "TOOLS.md"} {
		add("agent-settings", "main", name, []byte("source "+name))
	}
	add("memory", "main", "MEMORY.md", []byte("stable facts"))
	add("memory", "main", "memory/2026-10-26.md", []byte("daily secret"))
	add("workspace", "main", "projects/readme.md", []byte("imported project"))
	add("sessions", "main", "sqlite/transcripts/a.jsonl", []byte("historical transcript"))
	add("skills", "main", "skills/demo/SKILL.md", []byte("---\nname: demo\ndescription: demo\n---\nDemo"))
	add("skills", "main", "skills/demo/helper.py", []byte("print('demo')"))
	add("cron", "main", "jobs.json", []byte(`{"sourceTimezone":"Asia/Shanghai","jobs":[{"id":"cron1","enabled":true,"schedule":{"kind":"cron","expr":"0 8 * * *"},"payload":{"kind":"agentTurn","message":"AI news"}}]}`))
	add("agent-backup", "other", "other-agents/other/cron/jobs.json", []byte(`{"jobs":[]}`))
	writeJSON(t, filepath.Join(pkg, "manifest.json"), m)
	return pkg
}
func TestFullImportAndRepeat(t *testing.T) {
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	home := dst.svc.Home
	pkg := fullFixture(t)
	testutil.WriteFile(t, filepath.Join(home, workspaceBase(), "projects/readme.md"), []byte("existing project"))
	testutil.WriteFile(t, filepath.Join(home, projectMemory(home), "2026-10-26.md"), []byte("existing daily"))
	testutil.WriteFile(t, filepath.Join(home, ".claude/skills/demo/SKILL.md"), []byte("existing skill"))
	for i := 0; i < 2; i++ {
		result := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
		if result["state"] != "SUCCEEDED" {
			t.Fatalf("import %d: %v", i, result)
		}
		index, err := os.ReadFile(result["migrationIndex"].(string))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			MemoryFiles map[string]string `json:"memoryFiles"`
			CronFile    string            `json:"cronFile"`
		}
		if err = json.Unmarshal(index, &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.MemoryFiles) != 6 || filepath.Base(doc.CronFile) != "from_openclaw_cron.json" {
			t.Fatalf("bad index %s", index)
		}
	}
	for p, want := range map[string]string{filepath.Join(workspaceBase(), "projects/readme.md"): "existing project", filepath.Join(projectMemory(home), "2026-10-26.md"): "existing daily", ".claude/skills/demo/SKILL.md": "existing skill"} {
		b, e := os.ReadFile(filepath.Join(home, p))
		if e != nil || string(b) != want {
			t.Fatalf("changed %s", p)
		}
	}
	if _, e := os.Stat(filepath.Join(home, ".claude/skills/demo/helper.py")); !os.IsNotExist(e) {
		t.Fatal("merged existing skill")
	}
	m, e := ReadPackage(pkg, ReadOptions{})
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(home, workspaceBase(), "migration/conflicts", m.PackageID, "workspace/main/projects/readme.md"))
	if e != nil || string(b) != "imported project" {
		t.Fatal("conflict copy missing", e)
	}
}
func TestFullInstallSkillAndLogs(t *testing.T) {
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	pkg := fullFixture(t)
	result := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if result["state"] != "SUCCEEDED" {
		t.Fatal(result)
	}
	for _, p := range []string{".claude/skills/demo/SKILL.md", ".claude/skills/demo/helper.py", projectMemory(dst.svc.Home) + "/2026-10-26.md", workspaceBase() + "/other-agents/other/cron/from_openclaw_cron.json"} {
		if _, e := os.Stat(filepath.Join(dst.svc.Home, p)); e != nil {
			t.Fatal(p, e)
		}
	}
}
func TestFullRejectUnsafeAndMissing(t *testing.T) {
	for _, kind := range []string{"traversal", "missing", "symlink", "disabled-cron"} {
		t.Run(kind, func(t *testing.T) {
			dst := newEnv(t)
			dst.svc.Home = t.TempDir()
			pkg := fullFixture(t)
			b, _ := os.ReadFile(filepath.Join(pkg, "manifest.json"))
			var m Manifest
			json.Unmarshal(b, &m)
			switch kind {
			case "traversal":
				m.Files[0].Path = "../escape"
				writeJSON(t, filepath.Join(pkg, "manifest.json"), m)
			case "missing":
				os.Remove(filepath.Join(pkg, "blobs", m.Files[0].Hash))
			case "symlink":
				os.Symlink(t.TempDir(), filepath.Join(dst.svc.Home, workspaceBase()))
			case "disabled-cron":
				for i, f := range m.Files {
					if f.Category == "cron" {
						raw := []byte(`{"jobs":[{"id":"x","enabled":false}]}`)
						m.Files[i].Hash = sha(string(raw))
						m.Files[i].Bytes = int64(len(raw))
						testutil.WriteFile(t, filepath.Join(pkg, "blobs", sha(string(raw))), raw)
					}
				}
				writeJSON(t, filepath.Join(pkg, "manifest.json"), m)
			}
			if _, e := dst.svc.Handle(Request{Action: "preview", File: pkg}); e == nil {
				t.Fatal("accepted unsafe package")
			}
		})
	}
}
func TestActualPluginPackage(t *testing.T) {
	pkg := os.Getenv("YOOOCLAW_TEST_V2_PACKAGE")
	if pkg == "" {
		t.Skip("optional local plugin export")
	}
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	result := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if result["state"] != "SUCCEEDED" {
		t.Fatal(result)
	}
	t.Logf("result counts: %v", outcomes(t, result))
}

func TestFullArchiveRoundTrip(t *testing.T) {
	pkg := fullFixture(t)
	archive := filepath.Join(t.TempDir(), "full.tar.gz")
	if _, e := PackDirectory(pkg, archive); e != nil {
		t.Fatal(e)
	}
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	r := runPlan(t, dst.svc, stagePlan(t, dst.svc, archive))
	if r["state"] != "SUCCEEDED" {
		t.Fatal(r)
	}
}
func TestFullStalePlanDoesNotOverwrite(t *testing.T) {
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	pkg := fullFixture(t)
	plan := stagePlan(t, dst.svc, pkg)
	p := filepath.Join(dst.svc.Home, workspaceBase(), "projects/readme.md")
	testutil.WriteFile(t, p, []byte("new user edit"))
	_, e := dst.svc.Handle(Request{Action: "import", LocalTransferID: plan["localTransferId"].(string), PlanID: plan["planId"].(string)})
	if ErrorCode(e) != "PLAN_STALE" {
		t.Fatal("expected stale plan", e)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "new user edit" {
		t.Fatal("overwrote edit")
	}
}
func TestExclusiveSkillRenameDoesNotReplace(t *testing.T) {
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, "temp"), 0700)
	os.Mkdir(filepath.Join(home, "existing"), 0700)
	p, e := os.Open(home)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = renameDirectoryExclusive(p, "temp", "existing"); e == nil {
		t.Fatal("replaced existing empty skill")
	}
}
func TestFullResumeAfterIndexConflict(t *testing.T) {
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	pkg := fullFixture(t)
	index := filepath.Join(dst.svc.Home, workspaceBase(), "migration/INDEX.md")
	testutil.WriteFile(t, index, []byte("user index"))
	plan := stagePlan(t, dst.svc, pkg)
	r := runPlan(t, dst.svc, plan)
	if r["state"] != "PARTIAL" {
		t.Fatal(r)
	}
	if _, e := os.Stat(r["reportPath"].(string)); e != nil {
		t.Fatal(e)
	}
	os.Remove(index)
	out, e := dst.svc.Handle(Request{Action: "import", LocalTransferID: plan["localTransferId"].(string), PlanID: plan["planId"].(string), Resume: true})
	if e != nil {
		t.Fatal(e)
	}
	if out.(map[string]any)["state"] != "SUCCEEDED" {
		t.Fatal(out)
	}
}

func TestFullSkillSourcePriority(t *testing.T) {
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	pkg := fullFixture(t)
	b, _ := os.ReadFile(filepath.Join(pkg, "manifest.json"))
	var m Manifest
	json.Unmarshal(b, &m)
	for _, f := range []FullFile{{Category: "skills", Scope: "main", Path: ".agents/skills/demo/SKILL.md"}, {Category: "skills", Scope: "shared", Path: "demo/SKILL.md"}} {
		raw := []byte("lower-priority definition " + f.Path)
		f.Bytes = int64(len(raw))
		f.Hash = sha(string(raw))
		testutil.WriteFile(t, filepath.Join(pkg, "blobs", f.Hash), raw)
		m.Files = append(m.Files, f)
	}
	writeJSON(t, filepath.Join(pkg, "manifest.json"), m)
	r := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if r["state"] != "SUCCEEDED" {
		t.Fatal(r)
	}
	installed, _ := os.ReadFile(filepath.Join(dst.svc.Home, ".claude/skills/demo/SKILL.md"))
	if string(installed) != "---\nname: demo\ndescription: demo\n---\nDemo" {
		t.Fatal("wrong skill precedence")
	}
}

func TestFullCronConflictKeepsRenamedFilename(t *testing.T) {
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	pkg := fullFixture(t)
	original := filepath.Join(dst.svc.Home, workspaceBase(), "migration/openclaw/cron/from_openclaw_cron.json")
	testutil.WriteFile(t, original, []byte(`{"jobs":[]}`))
	result := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if result["state"] != "SUCCEEDED" {
		t.Fatal(result)
	}
	raw, e := os.ReadFile(result["migrationIndex"].(string))
	if e != nil {
		t.Fatal(e)
	}
	var doc struct {
		CronFile string `json:"cronFile"`
	}
	if e = json.Unmarshal(raw, &doc); e != nil {
		t.Fatal(e)
	}
	if filepath.Base(doc.CronFile) != "from_openclaw_cron.json" || doc.CronFile == original {
		t.Fatal(doc.CronFile)
	}
}

func TestSkillRootDocumentsDoNotCollideWithWorkspaceDocuments(t *testing.T) {
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	pkg := t.TempDir()
	m := Manifest{SchemaVersion: 2, PackageID: newUUID(), Files: []FullFile{}}
	for _, f := range []struct{ scope, path, body string }{{"main", "skills/README.md", "workspace notes"}, {"shared", "README.md", "shared notes"}} {
		h := sha(f.body)
		testutil.WriteFile(t, filepath.Join(pkg, "blobs", h), []byte(f.body))
		m.Files = append(m.Files, FullFile{"workspace", f.scope, f.path, h, int64(len(f.body))})
		m.OriginalBytes += int64(len(f.body))
	}
	writeJSON(t, filepath.Join(pkg, "manifest.json"), m)
	result := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if result["state"] != "SUCCEEDED" {
		t.Fatalf("%v", result)
	}
	for p, want := range map[string]string{"skills/README.md": "workspace notes", "migration/openclaw/shared/skills/README.md": "shared notes"} {
		got, err := os.ReadFile(filepath.Join(dst.svc.Home, workspaceBase(), p))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", p, got, err)
		}
	}
}
