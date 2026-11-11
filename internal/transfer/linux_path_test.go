package transfer

import (
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
	p := os.Getenv("TRANSFER_PATH_FIXTURE")
	if p == "" {
		t.Skip("cross-end fixture not set")
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
