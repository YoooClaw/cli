package transfer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func exportTarGz(t *testing.T, roots Roots) (string, *Manifest) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "pkg.tar.gz")
	m, size, err := ExportArchive(roots, out, ExportOptions{Include: "notifications,recordings,web-pages,images", WithAudio: true})
	if err != nil {
		t.Fatalf("export archive: %v", err)
	}
	if size <= 0 {
		t.Fatalf("archive size = %d", size)
	}
	return out, m
}

func TestArchiveRoundTripMatchesDirectoryPackage(t *testing.T) {
	src, dst := newEnv(t), newEnv(t)
	seedSource(t, src.roots)
	out, m := exportTarGz(t, src.roots)
	if names, _ := os.ReadDir(filepath.Dir(out)); len(names) != 1 {
		t.Fatalf("export should leave only the archive, got %v", names)
	}
	// 系统 tar 必须能读出我们写的格式，方便用户自行检查包内容。
	if listing, err := exec.Command("tar", "-tzf", out).Output(); err != nil {
		t.Fatalf("system tar: %v", err)
	} else if !strings.HasPrefix(string(listing), "manifest.json\nblobs/\nblobs/") {
		t.Fatalf("unexpected listing:\n%s", listing)
	}
	staged := stagePlan(t, dst.svc, out)
	if got := len(staged["decisions"].([]Decision)); got != len(m.Records) {
		t.Fatalf("decisions = %d, want %d", got, len(m.Records))
	}
	result := runPlan(t, dst.svc, staged)
	if result["state"] != "SUCCEEDED" || outcomes(t, result)[OutcomeAdded] != len(m.Records) {
		t.Fatalf("import = %v", result)
	}
	// 同一份数据的目录包再导一次：全部跳过，说明两种形态内容等价。
	if got := outcomes(t, runPlan(t, dst.svc, stagePlan(t, dst.svc, exportAll(t, src.roots)))); got[OutcomeSkipped] != len(m.Records) {
		t.Fatalf("directory re-import should skip everything, got %v", got)
	}
}

func TestArchiveFromSystemTarAndStowawayBlobs(t *testing.T) {
	src, dst := newEnv(t), newEnv(t)
	seedSource(t, src.roots)
	pkg := exportAll(t, src.roots)
	stowaway := strings.Repeat("f", 64)
	if err := os.WriteFile(filepath.Join(pkg, "blobs", stowaway), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "manual.tgz")
	cmd := exec.Command("tar", "--format", "ustar", "-czf", out, "-C", pkg, "manifest.json", "blobs")
	cmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("system tar: %v %s", err, raw)
	}
	staged := stagePlan(t, dst.svc, out)
	blobs, _ := os.ReadDir(filepath.Join(dst.svc.Dir, staged["localTransferId"].(string), "blobs"))
	for _, b := range blobs {
		if b.Name() == stowaway {
			t.Fatal("unreferenced blob should be dropped from staging")
		}
	}
}

func TestArchiveExportRefusesExistingAndSourceOutputs(t *testing.T) {
	src := newEnv(t)
	seedSource(t, src.roots)
	dir := t.TempDir()
	out := filepath.Join(dir, "pkg.tar.gz")
	if err := os.WriteFile(out, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ExportArchive(src.roots, out, ExportOptions{}); ErrorCode(err) != "OUTPUT_EXISTS" {
		t.Fatalf("err = %v, want OUTPUT_EXISTS", err)
	}
	inside := filepath.Join(src.roots[TypeNotifications], "pkg.tar.gz")
	if _, _, err := ExportArchive(src.roots, inside, ExportOptions{}); ErrorCode(err) != "OUTPUT_INSIDE_SOURCE" {
		t.Fatalf("err = %v, want OUTPUT_INSIDE_SOURCE", err)
	}
	names, _ := os.ReadDir(dir)
	if len(names) != 1 {
		t.Fatalf("failed export left files behind: %v", names)
	}
	notes, _ := os.ReadDir(src.roots[TypeNotifications])
	for _, n := range notes {
		if strings.Contains(n.Name(), "pkg.tar.gz") {
			t.Fatalf("export wrote into the source: %s", n.Name())
		}
	}
}

type rawEntry struct {
	name, body, typ, prefix string
	badChecksum             bool
}

// rawTar 手工拼 ustar 条目，用来构造导出端永远不会产出的恶意包。
func rawTar(entries ...rawEntry) []byte {
	var buf bytes.Buffer
	for _, e := range entries {
		h := make([]byte, 512)
		put := func(off int, s string) { copy(h[off:], s) }
		put(0, e.name)
		put(100, "0000600\x00")
		put(108, "0000000\x00")
		put(116, "0000000\x00")
		put(124, octal(len(e.body), 11)+"\x00")
		put(136, "00000000000\x00")
		put(148, "        ")
		typ := e.typ
		if typ == "" {
			typ = "0"
		}
		put(156, typ)
		put(257, "ustar\x0000")
		put(345, e.prefix)
		sum := 0
		for _, b := range h {
			sum += int(b)
		}
		if e.badChecksum {
			sum++
		}
		put(148, octal(sum, 6)+"\x00 ")
		buf.Write(h)
		buf.WriteString(e.body)
		buf.Write(make([]byte, (512-len(e.body)%512)%512))
	}
	buf.Write(make([]byte, 1024))
	return buf.Bytes()
}

func octal(n, width int) string { return fmt.Sprintf("%0*o", width, n) }

func gz(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(raw)
	_ = w.Close()
	return buf.Bytes()
}

func extractCode(t *testing.T, archive []byte) string {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "pkg.tar.gz")
	if err := os.WriteFile(file, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(filepath.Join(dest, "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ExtractArchive(file, dest); err != nil {
		if code := ErrorCode(err); code != "" {
			return code
		}
		return err.Error()
	}
	return "OK"
}

func TestExtractArchiveRejectsUnsafeAndMalformedInput(t *testing.T) {
	manifest := rawEntry{name: "manifest.json", body: "{}"}
	blob := "blobs/" + strings.Repeat("a", 64)
	var pax bytes.Buffer
	tw := tar.NewWriter(&pax)
	_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Size: 2, Mode: 0o600, Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": "x"}})
	_, _ = tw.Write([]byte("{}"))
	_ = tw.Close()
	var gnu bytes.Buffer
	tw = tar.NewWriter(&gnu)
	_ = tw.WriteHeader(&tar.Header{Name: "blobs/" + strings.Repeat("c", 64) + strings.Repeat("/x", 40), Size: 0, Mode: 0o600, Format: tar.FormatGNU})
	_ = tw.Close()
	bomb := rawTar(manifest, rawEntry{name: "blobs/" + strings.Repeat("b", 64), body: string(make([]byte, 80<<20))})
	full := gz(t, rawTar(manifest))
	cases := map[string]struct {
		archive []byte
		want    string
	}{
		"parent traversal":   {gz(t, rawTar(manifest, rawEntry{name: "../escape", body: "x"})), "ARCHIVE_UNSAFE_ENTRY"},
		"absolute path":      {gz(t, rawTar(manifest, rawEntry{name: "/etc/passwd", body: "x"})), "ARCHIVE_UNSAFE_ENTRY"},
		"symlink":            {gz(t, rawTar(manifest, rawEntry{name: blob, typ: "2"})), "ARCHIVE_UNSAFE_ENTRY"},
		"hard link":          {gz(t, rawTar(manifest, rawEntry{name: blob, typ: "1"})), "ARCHIVE_UNSAFE_ENTRY"},
		"pax header":         {gz(t, pax.Bytes()), "ARCHIVE_UNSAFE_ENTRY"},
		"gnu long name":      {gz(t, gnu.Bytes()), "ARCHIVE_UNSAFE_ENTRY"},
		"unknown file":       {gz(t, rawTar(manifest, rawEntry{name: "notes.txt", body: "x"})), "ARCHIVE_UNSAFE_ENTRY"},
		"duplicate":          {gz(t, rawTar(manifest, manifest)), "ARCHIVE_DUPLICATE_ENTRY"},
		"bad checksum":       {gz(t, rawTar(rawEntry{name: "manifest.json", body: "{}", badChecksum: true})), "ARCHIVE_INVALID"},
		"missing manifest":   {gz(t, rawTar(rawEntry{name: blob, body: "x"})), "ARCHIVE_INVALID"},
		"not gzip":           {[]byte("not a gzip file"), "ARCHIVE_INVALID"},
		"truncated":          {full[:30], "ARCHIVE_INVALID"},
		"compression bomb":   {gz(t, bomb), "ARCHIVE_TOO_LARGE"},
		"minimal valid":      {full, "OK"},
		"dot-slash accepted": {gz(t, rawTar(rawEntry{name: "./manifest.json", body: "{}"})), "OK"},
	}
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := cases[name]
		if got := extractCode(t, c.archive); got != c.want {
			t.Errorf("%s: got %s, want %s", name, got, c.want)
		}
	}
}

func TestCapabilitiesAdvertiseArchiveSupport(t *testing.T) {
	caps := Capabilities()
	if !reflect.DeepEqual(caps["packageFormats"], []string{"directory", "tar.gz"}) {
		t.Fatalf("packageFormats = %v", caps["packageFormats"])
	}
	if err := CheckArchiveSupport(map[string]any{"packageFormats": []any{"directory", "tar.gz"}}); err != nil {
		t.Fatal(err)
	}
	if err := CheckArchiveSupport(map[string]any{}); ErrorCode(err) != "ARCHIVE_UNSUPPORTED_BY_TARGET" {
		t.Fatalf("old target should be refused, got %v", err)
	}
	for path, want := range map[string]bool{"/x/pkg.tar.gz": true, "/x/pkg.TGZ": true, "/x/pkg": false} {
		if IsArchivePath(path) != want {
			t.Errorf("IsArchivePath(%q) != %v", path, want)
		}
	}
}
