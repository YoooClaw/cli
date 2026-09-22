package transfer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YoooClaw/cli/internal/notif"
	"github.com/YoooClaw/cli/internal/recording"
	"github.com/YoooClaw/cli/internal/testutil"
)

type env struct {
	roots Roots
	svc   *Service
}

func newEnv(t *testing.T) env {
	t.Helper()
	dir := t.TempDir()
	roots := Roots{
		TypeNotifications: filepath.Join(dir, "notifications"),
		TypeRecordings:    filepath.Join(dir, "recordings"),
		TypeWebPages:      filepath.Join(dir, "web-pages"),
		TypeImages:        filepath.Join(dir, "images"),
	}
	logger := testutil.Logger{T: t}
	ns := notif.NewStorage(roots[TypeNotifications], notif.PluginConfig{}, logger)
	if err := ns.Init(); err != nil {
		t.Fatal(err)
	}
	rs := recording.NewStorage(roots[TypeRecordings], logger)
	if err := rs.Init(); err != nil {
		t.Fatal(err)
	}
	return env{roots: roots, svc: &Service{Dir: filepath.Join(dir, "transfers"), Roots: roots, Notifications: ns, Recordings: rs}}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, _ := json.Marshal(v)
	testutil.WriteFile(t, path, raw)
}

// seedSource 摆放一份源数据：通知用 CLI 的 .jsonl，另有旧 .json 数组（插件格式）。
func seedSource(t *testing.T, roots Roots) {
	t.Helper()
	testutil.WriteFile(t, filepath.Join(roots[TypeNotifications], "2026-09-20.jsonl"),
		[]byte(`{"clientLabel":"default","appName":"wx","appDisplayName":"微信","title":"张三","content":"明天开会","timestamp":"2026-09-20T08:30:00+08:00"}`+"\n"+`not json`+"\n"))
	writeJSON(t, filepath.Join(roots[TypeNotifications], "2026-09-19.json"), []map[string]any{
		{"appName": "lark", "title": "群", "content": "hi", "timestamp": "2026-09-19T10:00:00+08:00"},
	})
	testutil.WriteFile(t, filepath.Join(roots[TypeRecordings], "audio", "r1.m4a"), []byte("AUDIO"))
	testutil.WriteFile(t, filepath.Join(roots[TypeRecordings], "transcripts", "r1.md"), []byte("# 纪要"))
	writeJSON(t, filepath.Join(roots[TypeRecordings], "index.json"), map[string]any{"recordings": []map[string]any{{
		"id": "r1", "clientLabel": "default", "title": "周会", "status": "transcribed",
		"metadata":  map[string]any{"name": "周会", "duration_sec": 61, "created_at": "2026-09-18T10:00:00+08:00", "markers": []any{}, "oss_audio_url": "https://signed"},
		"audioFile": "audio/r1.m4a", "transcriptFile": "transcripts/r1.md", "ingestedAt": "x", "updatedAt": "y",
	}}})
	testutil.WriteFile(t, filepath.Join(roots[TypeImages], "files", "i1.png"), []byte("PNG"))
	writeJSON(t, filepath.Join(roots[TypeImages], "index.json"), map[string]any{"images": []map[string]any{{
		"imageId": "i1", "metadata": map[string]any{"oss_image_url": "https://s", "created_at": "2026-09-16T00:00:00Z", "width": 10}, "localFile": "files/i1.png", "status": "synced",
	}}})
	hash := strings.Repeat("ab", 32)
	testutil.WriteFile(t, filepath.Join(roots[TypeWebPages], "files", "abababab-a.md"), []byte("body"))
	writeJSON(t, filepath.Join(roots[TypeWebPages], "index.json"), map[string]any{"pages": []map[string]any{{
		"urlHash": hash, "canonicalUrl": "https://a.example/", "title": "A", "relativePath": "files/abababab-a.md",
		"capturedAt": "2026-09-21T00:00:00Z", "firstCapturedAt": "2026-09-01T00:00:00Z", "captureCount": 2, "bytes": 4,
	}}})
}

func exportAll(t *testing.T, roots Roots) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "pkg")
	if _, err := Export(roots, out, ExportOptions{Include: "notifications,recordings,web-pages,images", WithAudio: true}); err != nil {
		t.Fatalf("export: %v", err)
	}
	return out
}

func stagePlan(t *testing.T, svc *Service, pkg string) map[string]any {
	t.Helper()
	res, err := svc.Handle(Request{Action: "preview", File: pkg})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	return res.(map[string]any)
}

func runPlan(t *testing.T, svc *Service, staged map[string]any) map[string]any {
	t.Helper()
	res, err := svc.Handle(Request{Action: "import", LocalTransferID: staged["localTransferId"].(string), PlanID: staged["planId"].(string)})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return res.(map[string]any)
}

func outcomes(t *testing.T, result map[string]any) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, r := range result["results"].([]Result) {
		counts[r.Outcome]++
	}
	return counts
}

func TestExportImportRoundTripIsIdempotent(t *testing.T) {
	src, dst := newEnv(t), newEnv(t)
	seedSource(t, src.roots)
	pkg := exportAll(t, src.roots)

	m, err := ReadPackage(pkg, ReadOptions{})
	if err != nil {
		t.Fatalf("read own package: %v", err)
	}
	if len(m.Records) != 5 {
		t.Fatalf("records = %d, want 5 (warnings %v)", len(m.Records), m.Warnings)
	}
	if !strings.Contains(strings.Join(m.Warnings, ";"), "unreadable lines skipped") {
		t.Fatalf("bad jsonl line should be reported, warnings = %v", m.Warnings)
	}
	if !pathExists(filepath.Join(src.roots[TypeNotifications], ".transfer-origin")) {
		t.Fatal("export should write the .transfer-origin marker")
	}

	first := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if first["state"] != "SUCCEEDED" || outcomes(t, first)[OutcomeAdded] != 5 {
		t.Fatalf("first import = %v", first)
	}
	if first["staging"] != "cleaned" {
		t.Fatalf("staging should be cleaned after success: %v", first)
	}
	second := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if got := outcomes(t, second); got[OutcomeSkipped] != 5 {
		t.Fatalf("re-import should skip everything, got %v", got)
	}

	// 迁入的录音带内容寻址附件与迁移标记；[] 标记点不能被存储层吞掉。
	entry, ok := dst.svc.Recordings.FindByID("r1")
	if !ok || entry.Transfer == nil || entry.Status != recording.StatusTranscribed || entry.AudioStatus != recording.AudioStatusDownloaded {
		t.Fatalf("imported recording = %+v", entry)
	}
	if entry.Metadata.Markers == nil || !strings.HasPrefix(entry.AudioFile, "files/transfer-") {
		t.Fatalf("imported recording lost markers or asset: %+v", entry)
	}
	if raw, _ := os.ReadFile(filepath.Join(dst.roots[TypeRecordings], entry.AudioFile)); string(raw) != "AUDIO" {
		t.Fatalf("audio content = %q", raw)
	}

	// 目标端再导出，记录身份与版本必须与源包一致（多跳迁移不漂移）。
	again, err := ReadPackage(exportAll(t, dst.roots), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, r := range m.Records {
		want[r.Type+":"+r.RecordID] = r.RecordVersion + "@" + r.Origin
	}
	for _, r := range again.Records {
		if want[r.Type+":"+r.RecordID] != r.RecordVersion+"@"+r.Origin {
			t.Errorf("re-export drifted for %s:%s", r.Type, r.RecordID)
		}
	}
}

func TestImportedNotificationsSkipMemorySync(t *testing.T) {
	src, dst := newEnv(t), newEnv(t)
	seedSource(t, src.roots)
	runPlan(t, dst.svc, stagePlan(t, dst.svc, exportAll(t, src.roots)))

	scope, _ := notif.ResolveScanScope(notif.SyncScopeOptions{All: true})
	next := notif.NextSync(dst.roots[TypeNotifications], scope)
	if next["done"] != false || next["returned"] != 0 || next["skippedOnly"] != true {
		t.Fatalf("sync next over imported history = %v", next)
	}
	scan := notif.ScanSync(dst.roots[TypeNotifications], scope)
	if scan["totalPending"] != 0 {
		t.Fatalf("imported notifications must not count as pending: %v", scan)
	}
}

func TestPreviewReportsConflictAndPlanStale(t *testing.T) {
	src, dst := newEnv(t), newEnv(t)
	seedSource(t, src.roots)
	pkg := exportAll(t, src.roots)
	runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))

	// 目标端改了录音名：再导同一个包必须报 conflict 并保留目标。
	if _, _, err := dst.svc.Recordings.Rename("r1", "目标端改名"); err != nil {
		t.Fatal(err)
	}
	staged := stagePlan(t, dst.svc, pkg)
	conflicts := 0
	for _, d := range staged["decisions"].([]Decision) {
		if d.Action == OutcomeConflict {
			conflicts++
		}
	}
	if conflicts != 1 {
		t.Fatalf("decisions = %v", staged["decisions"])
	}

	// 预览之后目标那条记录又变了：执行计划要拒绝（PLAN_STALE）。
	if _, _, err := dst.svc.Recordings.Rename("r1", "又改了"); err != nil {
		t.Fatal(err)
	}
	_, err := dst.svc.Handle(Request{Action: "import", LocalTransferID: staged["localTransferId"].(string), PlanID: staged["planId"].(string)})
	if ErrorCode(err) != "PLAN_STALE" {
		t.Fatalf("err = %v, want PLAN_STALE", err)
	}

	fresh := runPlan(t, dst.svc, stagePlan(t, dst.svc, pkg))
	if fresh["state"] != "PARTIAL" || fresh["reportPath"] == nil {
		t.Fatalf("conflict import should be PARTIAL with a kept report: %v", fresh)
	}
	if entry, _ := dst.svc.Recordings.FindByID("r1"); entry.Metadata.Name != "又改了" {
		t.Fatalf("target record overwritten: %q", entry.Metadata.Name)
	}
	if _, err := dst.svc.Handle(Request{Action: "import", LocalTransferID: fresh["localTransferId"].(string), PlanID: fresh["planId"].(string)}); err != nil {
		t.Fatalf("partial import stays re-runnable: %v", err)
	}
}

func TestSupplementsMissingAssetWithoutRemarking(t *testing.T) {
	src, dst := newEnv(t), newEnv(t)
	seedSource(t, src.roots)
	// 目标端已有同一张图片的元数据但没有本地文件（下载失败）。
	writeJSON(t, filepath.Join(dst.roots[TypeImages], "index.json"), map[string]any{"images": []map[string]any{{
		"imageId": "i1", "metadata": map[string]any{"oss_image_url": "https://other", "created_at": "2026-09-16T00:00:00Z", "width": 10}, "status": "sync_failed",
	}}})
	staged := stagePlan(t, dst.svc, exportAll(t, src.roots))
	result := runPlan(t, dst.svc, staged)
	if outcomes(t, result)[OutcomeSupplemented] != 1 {
		t.Fatalf("results = %v", result["results"])
	}
	raw, _ := os.ReadFile(filepath.Join(dst.roots[TypeImages], "index.json"))
	var idx struct {
		Images []map[string]any `json:"images"`
	}
	_ = json.Unmarshal(raw, &idx)
	img := idx.Images[0]
	if img["status"] != "synced" || img["transfer"] != nil || img["localFile"] == nil {
		t.Fatalf("supplemented image = %v", img)
	}
	if meta := img["metadata"].(map[string]any); meta["oss_image_url"] != "https://other" {
		t.Fatalf("local-only fields must be kept: %v", meta)
	}
}

func TestReadPackageRejectsTampering(t *testing.T) {
	src := newEnv(t)
	seedSource(t, src.roots)
	pkg := exportAll(t, src.roots)
	m, _ := ReadPackage(pkg, ReadOptions{})
	blob := filepath.Join(pkg, "blobs", m.Records[len(m.Records)-1].Assets[0].Hash)
	if err := os.WriteFile(blob, []byte("tampered!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackage(pkg, ReadOptions{}); ErrorCode(err) != "CHECKSUM_MISMATCH" {
		t.Fatalf("err = %v, want CHECKSUM_MISMATCH", err)
	}
}

func TestExportValidatesScopeAndOutput(t *testing.T) {
	src := newEnv(t)
	seedSource(t, src.roots)
	if _, err := Export(src.roots, "", ExportOptions{From: "2026-09-01T00:00:00"}); ErrorCode(err) != "INVALID_TIME" {
		t.Fatalf("timezone-less --from: %v", err)
	}
	if _, err := Export(src.roots, "", ExportOptions{Include: "memory"}); ErrorCode(err) != "INVALID_SCOPE" {
		t.Fatalf("unknown type: %v", err)
	}
	inside := filepath.Join(src.roots[TypeRecordings], "pkg")
	if _, err := Export(src.roots, inside, ExportOptions{}); ErrorCode(err) != "OUTPUT_INSIDE_SOURCE" {
		t.Fatalf("output inside source: %v", err)
	}
	m, err := Export(src.roots, "", ExportOptions{Include: "notifications", From: "2026-09-20T00:00:00+08:00"})
	if err != nil || len(m.Records) != 1 {
		t.Fatalf("time filter: %v %+v", err, m)
	}
	if pathExists(filepath.Join(src.roots[TypeNotifications], ".transfer-origin")) {
		t.Fatal("dry-run must not write the origin marker")
	}
}
