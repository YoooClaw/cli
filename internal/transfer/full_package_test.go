package transfer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	for _, kind := range []string{"traversal", "missing", "symlink"} {
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

func TestMainWorkspaceFilesImportDirectly(t *testing.T) {
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	pkg := fullFixture(t)
	existing := filepath.Join(dst.svc.Home, workspaceBase(), "AGENTS.md")
	testutil.WriteFile(t, existing, []byte("target instructions"))
	result := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if result["state"] != "SUCCEEDED" {
		t.Fatal(result)
	}
	raw, err := os.ReadFile(result["migrationIndex"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		MemoryFiles map[string]string `json:"memoryFiles"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"USER.md", "SOUL.md", "IDENTITY.md", "TOOLS.md", "MEMORY.md"} {
		expected := filepath.Join(dst.svc.Home, workspaceBase(), name)
		if index.MemoryFiles[name] != expected {
			t.Fatalf("%s: %s", name, index.MemoryFiles[name])
		}
		if _, err := os.Stat(expected); err != nil {
			t.Fatal(err)
		}
	}
	project := filepath.Join(dst.svc.Home, workspaceBase(), "projects/readme.md")
	if data, err := os.ReadFile(project); err != nil || string(data) != "imported project" {
		t.Fatalf("project: %s %v", data, err)
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "target instructions" {
		t.Fatal("overwrote target instructions", err)
	}
	if index.MemoryFiles["AGENTS.md"] == existing {
		t.Fatal("familiarity must reference the isolated source")
	}
	if data, err := os.ReadFile(index.MemoryFiles["AGENTS.md"]); err != nil || string(data) != "source AGENTS.md" {
		t.Fatal("missing isolated source", err)
	}
	if _, err := os.Stat(filepath.Join(dst.svc.Home, workspaceBase(), "migration/openclaw/main/agent-settings")); !os.IsNotExist(err) {
		t.Fatal("settings still archived", err)
	}
}

func TestImportMoreThan100000BusinessRecords(t *testing.T) {
	root := t.TempDir()
	rows := make([]map[string]any, 100001)
	for i := range rows {
		rows[i] = map[string]any{"appName": "test", "title": fmt.Sprint(i), "content": "test", "timestamp": "2026-11-02T00:00:00Z"}
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	h := sha(string(raw))
	testutil.WriteFile(t, filepath.Join(root, "blobs", h), raw)
	writeJSON(t, filepath.Join(root, "manifest.json"), Manifest{SchemaVersion: 2, PackageID: newUUID(), OriginalBytes: int64(len(raw)), Files: []FullFile{{Category: TypeNotifications, Scope: "main", Path: "2026-11-02.json", Hash: h, Bytes: int64(len(raw))}}})
	m, err := ReadPackage(root, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Records) != len(rows) {
		t.Fatalf("lost records: %d", len(m.Records))
	}
	// The legacy reader must also accept the same valid records, within its byte limit.
	m.SchemaVersion = 1
	m.Warnings = []string{}
	writeJSON(t, filepath.Join(root, "manifest.json"), m)
	m, err = ReadPackage(root, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Records) != len(rows) {
		t.Fatalf("lost legacy records: %d", len(m.Records))
	}
}

func TestFullImportWithoutFileCountOrMetadataSizeCaps(t *testing.T) {
	pkg := t.TempDir()
	body := append([]byte(`{"jobs":[]}`), []byte(strings.Repeat(" ", 64*1024*1024))...)
	h := sha(string(body))
	testutil.WriteFile(t, filepath.Join(pkg, "blobs", h), body)
	m := Manifest{SchemaVersion: 2, PackageID: newUUID(), Files: []FullFile{{"cron", "main", "jobs.json", h, int64(len(body))}}}
	emptyHash := sha("")
	testutil.WriteFile(t, filepath.Join(pkg, "blobs", emptyHash), nil)
	for i := 0; i < 100001; i++ {
		m.Files = append(m.Files, FullFile{"workspace", "main", fmt.Sprintf("files/%d.txt", i), emptyHash, 0})
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	// Valid JSON whitespace makes the manifest itself exceed the former 64 MiB cap.
	b = append(b, []byte(strings.Repeat(" ", 64*1024*1024))...)
	testutil.WriteFile(t, filepath.Join(pkg, "manifest.json"), b)
	got, err := ReadPackage(pkg, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != len(m.Files) {
		t.Fatalf("files=%d", len(got.Files))
	}
}

func TestFullImportLargeFileMetadata(t *testing.T) {
	pkg := t.TempDir()
	h := strings.Repeat("a", 64)
	p := filepath.Join(pkg, "blobs", h)
	testutil.WriteFile(t, p, nil)
	const size int64 = 10_000_000_001
	if err := os.Truncate(p, size); err != nil {
		t.Fatal(err)
	}
	m := Manifest{SchemaVersion: 2, PackageID: newUUID(), OriginalBytes: size, Files: []FullFile{{"workspace", "main", "large.bin", h, size}}}
	writeJSON(t, filepath.Join(pkg, "manifest.json"), m)
	// Sparse file tests metadata acceptance without reading 10 GB. Hash verification
	// remains covered by normal import tests; this is the trusted staging read mode.
	if _, err := ReadPackage(pkg, ReadOptions{SkipBlobHash: true}); err != nil {
		t.Fatal(err)
	}
}

func TestImportPreservesSkillDirectoryNamesAndResources(t *testing.T) {
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	pkg := t.TempDir()
	m := Manifest{SchemaVersion: 2, PackageID: newUUID(), Files: []FullFile{}}
	paths := []string{"skills/中文 技能.v2/SKILL.md", "skills/中文 技能.v2/scripts/run.py", "skills/中文 技能.v2/assets/data.txt", "skills/只有资源/readme.txt", "README.md"}
	for _, p := range paths {
		raw := []byte("original " + p)
		h := sha(string(raw))
		testutil.WriteFile(t, filepath.Join(pkg, "blobs", h), raw)
		m.Files = append(m.Files, FullFile{"skills", "main", p, h, int64(len(raw))})
	}
	writeJSON(t, filepath.Join(pkg, "manifest.json"), m)
	result := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if result["state"] != "SUCCEEDED" {
		t.Fatal(result)
	}
	for _, p := range paths {
		dest := filepath.Join(dst.svc.Home, ".claude", p)
		if p == "README.md" {
			dest = filepath.Join(dst.svc.Home, workspaceBase(), "migration/openclaw/main/skills", p)
		}
		got, err := os.ReadFile(dest)
		if err != nil || string(got) != "original "+p {
			t.Fatalf("%s: %q %v", dest, got, err)
		}
	}
	// Existing names remain untouched even if the new backup has different contents.
	target := filepath.Join(dst.svc.Home, ".claude/skills/中文 技能.v2/SKILL.md")
	testutil.WriteFile(t, target, []byte("local changes"))
	result = runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if result["state"] != "SUCCEEDED" {
		t.Fatal(result)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "local changes" {
		t.Fatalf("overwrote existing skill: %s", got)
	}
}

func TestSkillPathStillRejectsTraversal(t *testing.T) {
	for _, p := range []string{"skills/../escape.txt", "skills/./escape.txt", "/tmp/escape.txt", "skills/a/../../escape.txt"} {
		name, _ := skillPath(FullFile{Path: p})
		if name != "" {
			t.Fatalf("accepted unsafe path %q", p)
		}
	}
}

func TestFullImportReferenceCronAndMalformedBusinessRows(t *testing.T) {
	pkg := fullFixture(t)
	before, err := ReadPackage(pkg, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(pkg, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	originals := map[string]string{}
	for i, f := range m.Files {
		raw, err := os.ReadFile(filepath.Join(pkg, "blobs", f.Hash))
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case f.Category == "cron":
			raw = []byte("not executable cron JSON; retain for Agent review")
		case f.Path == "index.json" && indexKeys[f.Category] != "":
			var index map[string]json.RawMessage
			if err := json.Unmarshal(raw, &index); err != nil {
				t.Fatal(err)
			}
			var rows []json.RawMessage
			if err := json.Unmarshal(index[indexKeys[f.Category]], &rows); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, json.RawMessage(`null`), json.RawMessage(`{"metadata":false}`))
			index[indexKeys[f.Category]], _ = json.Marshal(rows)
			raw, _ = json.Marshal(index)
		case f.Category == TypeNotifications && f.Path == "2026-09-20.jsonl":
			raw = []byte("{broken\nnull\n")
		default:
			continue
		}
		originals[logicalKey(f)] = string(raw)
		m.OriginalBytes += int64(len(raw)) - f.Bytes
		m.Files[i].Hash = sha(string(raw))
		m.Files[i].Bytes = int64(len(raw))
		testutil.WriteFile(t, filepath.Join(pkg, "blobs", m.Files[i].Hash), raw)
	}
	writeJSON(t, filepath.Join(pkg, "manifest.json"), m)
	after, err := ReadPackage(pkg, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Records) != len(before.Records) || len(after.Warnings) < 8 {
		t.Fatalf("valid records lost or missing warnings: before=%d after=%d warnings=%v", len(before.Records), len(after.Records), after.Warnings)
	}
	dst := newEnv(t)
	dst.svc.Home = t.TempDir()
	result := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if result["state"] != "SUCCEEDED" {
		t.Fatal(result)
	}
	index, err := os.ReadFile(result["migrationIndex"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Warnings []string `json:"warnings"`
		CronFile string   `json:"cronFile"`
	}
	if err := json.Unmarshal(index, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Warnings) < 8 {
		t.Fatalf("warnings not persisted: %s", index)
	}
	cron, err := os.ReadFile(doc.CronFile)
	if err != nil || string(cron) != "not executable cron JSON; retain for Agent review" {
		t.Fatalf("cron changed: %s %v", cron, err)
	}
	for _, f := range m.Files {
		want, changed := originals[logicalKey(f)]
		if !changed || f.Category == "cron" {
			continue
		}
		p := filepath.Join(dst.svc.Home, workspaceBase(), "migration/openclaw", f.Scope, f.Category, f.Path)
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Fatalf("reference lost: %s: %v", p, err)
		}
	}
}
