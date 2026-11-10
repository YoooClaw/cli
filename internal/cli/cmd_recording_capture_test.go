package cli

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// writeUploadedCaptureIndex writes the gateway-owned capture index. Capture
// recordings are only written through recordings.result.write.
func writeUploadedCaptureIndex(t *testing.T, home, id, title string, created time.Time, missingTranscript bool) {
	t.Helper()
	root := filepath.Join(home, "profiles", "default", "voice", "recordings")
	audioDir := filepath.Join(root, "audio")
	if err := os.MkdirAll(audioDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(audioDir, id+".m4a"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	summaryDir := filepath.Join(root, "summaries")
	if err := os.MkdirAll(summaryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(summaryDir, id+".md"), []byte("summary"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The index always declares the transcript; missingTranscript only omits the
	// file so status can report it as a missing artifact.
	if !missingTranscript {
		dir := filepath.Join(root, "transcript-data")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(`{"transcripts":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index := fmt.Sprintf(`{"recordings":[{"id":%q,"sourceType":"capture_app","clientLabel":"wuying","metadata":{"name":%q,"created_at":%q},"status":"transcribed","audioStatus":"downloaded","audioFile":"audio/%s.m4a","transcriptFile":"transcript-data/%s.json","summaryFile":"summaries/%s.md"}]}`,
		id, title, created.Format(time.RFC3339), id, id, id)
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeLegacyCaptureIndex writes the pre-migration Capture daily index. The
// gateway index is authoritative; these entries are only a fallback.
func writeLegacyCaptureIndex(t *testing.T, home, id, title string, recorded time.Time) {
	t.Helper()
	rel := fmt.Sprintf("recordings/%s/%s/%s/audio.m4a", recorded.Format("2006"), recorded.Format("01"), id)
	audio := filepath.Join(home, "profiles", "default", "voice", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(audio), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(home, "profiles", "default", "voice", "recordings-jsonl")
	if err := os.MkdirAll(history, 0o700); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"id":%q,"title":%q,"audio_rel_path":%q,"transcript_rel_path":null,"summary_rel_path":null,"recorded_at":%q,"duration_ms":60000,"status":"completed"}`+"\n",
		id, title, rel, recorded.Format(time.RFC3339))
	file, err := os.OpenFile(filepath.Join(history, recorded.Format("2006-01-02")+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(line); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeHardwareCLIIndex(t *testing.T, home, id string, created time.Time) {
	t.Helper()
	dir := filepath.Join(home, "profiles", "default", "recordings")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	index := fmt.Sprintf(`{"recordings":[{"id":%q,"clientLabel":"desk-device","metadata":{"name":"硬件录音","created_at":%q,"duration_sec":20},"status":"transcribed"}]}`, id, created.Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRecordingMergesCaptureAndSmartHardwareSources(t *testing.T) {
	home := sandbox(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	writeHardwareCLIIndex(t, home, "hardware-id", base)
	writeUploadedCaptureIndex(t, home, "capture-id", "输入法会议", base.Add(time.Hour), true)

	out, code := execCLI(t, "recording", "list", "--format", "json")
	if code != 0 {
		t.Fatalf("recording list failed: %s", out)
	}
	result := decode(t, out)
	items := result["recordings"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["id"] != "capture-id" {
		t.Fatalf("mixed list must sort globally: %s", out)
	}
	if items[0].(map[string]any)["source_type"] != "capture_app" || items[0].(map[string]any)["source_name"] != "YoooClaw Capture" {
		t.Fatalf("capture source identity missing: %s", out)
	}
	if items[1].(map[string]any)["source_type"] != "mobile_app" || items[1].(map[string]any)["source_name"] != "YoooClaw 智能硬件" {
		t.Fatalf("hardware source identity missing: %s", out)
	}

	out, code = execCLI(t, "recording", "list", "--source", "capture_app", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(1) {
		t.Fatalf("capture source filter failed: code=%d %s", code, out)
	}
	out, code = execCLI(t, "recording", "list", "--client", "desk-device", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(1) {
		t.Fatalf("client filter should apply only to hardware: code=%d %s", code, out)
	}
	out, code = execCLI(t, "recording", "+latest", "--format", "json")
	if code != 0 || decode(t, out)["recording"].(map[string]any)["id"] != "capture-id" {
		t.Fatalf("mixed latest failed: code=%d %s", code, out)
	}

	out, code = execCLI(t, "recording", "status", "capture-id", "--format", "json")
	if code != 0 {
		t.Fatalf("capture status failed: %s", out)
	}
	detail := decode(t, out)["recording"].(map[string]any)
	if detail["has_audio"] != true || detail["has_transcript"] != false || detail["has_summary"] != true {
		t.Fatalf("capture status must use file facts: %+v", detail)
	}
	missing := detail["missing_artifacts"].([]any)
	if len(missing) != 1 || missing[0] != "transcript" || detail["transcript_path"] != nil {
		t.Fatalf("capture missing artifacts = %+v", detail)
	}
	if audioPath, ok := detail["audio_path"].(string); !ok || !filepath.IsAbs(audioPath) {
		t.Fatalf("capture status must return safe absolute artifact paths: %+v", detail)
	}

	out, code = execCLI(t, "recording", "storage-path", "--format", "json")
	sources := decode(t, out)["sources"].(map[string]any)
	if code != 0 || sources["capture_app"] == nil || sources["mobile_app"] == nil {
		t.Fatalf("recording storage-path must describe both sources: code=%d %s", code, out)
	}
}

func TestRecordingQueriesUploadedCaptureIndex(t *testing.T) {
	home := sandbox(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.Local)
	root := filepath.Join(home, "profiles", "default", "voice", "recordings")
	audio := filepath.Join(root, "audio", "uploaded.m4a")
	if err := os.MkdirAll(filepath.Dir(audio), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	index := fmt.Sprintf(`{"recordings":[
		{"id":"uploaded","sourceType":"capture_app","clientLabel":"wuying","metadata":{"name":"云端会议","created_at":%q},"status":"synced","audioStatus":"downloaded","audioFile":"audio/uploaded.m4a"},
		{"id":"local","sourceType":"capture_app","clientLabel":"wuying","metadata":{"name":"本机会议","created_at":%q},"status":"synced"}
	]}`, base.Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := execCLI(t, "recording", "list", "--source", "capture_app", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(2) {
		t.Fatalf("capture index entries: code=%d %s", code, out)
	}
	out, code = execCLI(t, "recording", "status", "uploaded", "--format", "json")
	if code != 0 {
		t.Fatalf("uploaded Capture status: code=%d %s", code, out)
	}
	detail := decode(t, out)["recording"].(map[string]any)
	wantAudio, err := filepath.EvalSymlinks(audio)
	if err != nil || detail["source_type"] != "capture_app" || detail["audio_path"] != wantAudio || detail["has_audio"] != true {
		t.Fatalf("unexpected uploaded Capture details: %v, %v", detail, err)
	}
}

func TestRecordingUsesLegacyCaptureIndexOnlyAsFallback(t *testing.T) {
	home := sandbox(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.Local)
	writeLegacyCaptureIndex(t, home, "legacy-id", "旧版会议", base)

	out, code := execCLI(t, "recording", "list", "--source", "capture_app", "--format", "json")
	if code != 0 {
		t.Fatalf("legacy capture list failed: %s", out)
	}
	result := decode(t, out)
	items := result["recordings"].([]any)
	if result["total"] != float64(1) || len(items) != 1 {
		t.Fatalf("legacy capture entry missing: %s", out)
	}
	item := items[0].(map[string]any)
	if item["id"] != "legacy-id" || item["source_type"] != "capture_app" || item["status"] != "completed" {
		t.Fatalf("unexpected legacy entry: %+v", item)
	}
	if item["has_audio"] != true || item["has_transcript"] != false {
		t.Fatalf("legacy artifact facts: %+v", item)
	}

	out, code = execCLI(t, "recording", "status", "legacy-id", "--format", "json")
	if code != 0 {
		t.Fatalf("legacy capture status failed: %s", out)
	}
	detail := decode(t, out)["recording"].(map[string]any)
	if audioPath, ok := detail["audio_path"].(string); !ok || !filepath.IsAbs(audioPath) {
		t.Fatalf("legacy status must resolve absolute artifact paths: %+v", detail)
	}

	// A legacy entry has no clientLabel, so --client cannot match it.
	out, code = execCLI(t, "recording", "list", "--source", "capture_app", "--client", "wuying", "--format", "json")
	if code != 0 || decode(t, out)["total"] != float64(0) {
		t.Fatalf("legacy entry should not satisfy --client: code=%d %s", code, out)
	}

	// Once the gateway knows the ID, it wins over stale legacy state.
	writeUploadedCaptureIndex(t, home, "legacy-id", "网关会议", base, false)
	out, code = execCLI(t, "recording", "list", "--source", "capture_app", "--format", "json")
	if code != 0 {
		t.Fatalf("merged capture list failed: %s", out)
	}
	merged := decode(t, out)
	if merged["total"] != float64(1) {
		t.Fatalf("gateway and legacy entries must dedupe by ID: %s", out)
	}
	item = merged["recordings"].([]any)[0].(map[string]any)
	if item["name"] != "网关会议" || item["status"] != "transcribed" || item["audio_status"] != "downloaded" {
		t.Fatalf("gateway entry must override legacy state: %+v", item)
	}
}

func TestRecordingListItemsShareSchemaAcrossSources(t *testing.T) {
	home := sandbox(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.Local)
	writeHardwareCLIIndex(t, home, "mobile-id", base)
	writeUploadedCaptureIndex(t, home, "capture-id", "已上报会议", base.Add(time.Hour), false)
	writeLegacyCaptureIndex(t, home, "legacy-id", "旧版会议", base.Add(2*time.Hour))

	out, code := execCLI(t, "recording", "list", "--source", "all", "--format", "json")
	if code != 0 {
		t.Fatalf("recording list failed: %s", out)
	}
	items := decode(t, out)["recordings"].([]any)
	if len(items) != 3 {
		t.Fatalf("expected three sources: %s", out)
	}
	byID := map[string]map[string]any{}
	for _, raw := range items {
		item := raw.(map[string]any)
		byID[item["id"].(string)] = item
	}
	keys := func(item map[string]any) []string {
		out := slices.Collect(maps.Keys(item))
		slices.Sort(out)
		return out
	}
	mobile, uploaded, legacy := byID["mobile-id"], byID["capture-id"], byID["legacy-id"]
	if mobile == nil || uploaded == nil || legacy == nil {
		t.Fatalf("missing source in merged list: %s", out)
	}
	if !slices.Equal(keys(mobile), keys(uploaded)) {
		t.Fatalf("gateway entries differ in schema:\n%v\n%v", keys(mobile), keys(uploaded))
	}
	// The legacy fallback may only add recorded_at/diagnostics on top of the
	// common schema; everything else must match.
	wantLegacy := append(slices.Clone(keys(mobile)), "diagnostics", "recorded_at")
	slices.Sort(wantLegacy)
	if !slices.Equal(keys(legacy), wantLegacy) {
		t.Fatalf("legacy schema = %v, want %v", keys(legacy), wantLegacy)
	}
	for id, item := range byID {
		if _, ok := item["has_srt"]; !ok {
			t.Fatalf("%s is missing has_srt: %+v", id, item)
		}
	}
	if legacy["clientLabel"] != "legacy" || legacy["audio_status"] != "" {
		t.Fatalf("legacy values must follow the common conventions: %+v", legacy)
	}
	if missing, ok := legacy["missing_artifacts"].([]any); !ok || len(missing) != 0 {
		t.Fatalf("legacy missing_artifacts must be an empty array: %+v", legacy["missing_artifacts"])
	}

	// Status details of the legacy fallback and the gateway index must also
	// expose the same key set.
	detailKeys := func(id string) []string {
		out, code := execCLI(t, "recording", "status", id, "--source", "capture_app", "--format", "json")
		if code != 0 {
			t.Fatalf("recording status %s failed: %s", id, out)
		}
		return keys(decode(t, out)["recording"].(map[string]any))
	}
	if legacyKeys, uploadedKeys := detailKeys("legacy-id"), detailKeys("capture-id"); !slices.Equal(legacyKeys, uploadedKeys) {
		t.Fatalf("detail schema differs:\nlegacy=%v\nuploaded=%v", legacyKeys, uploadedKeys)
	}
}

func TestRecordingStatusRequiresSourceWhenIDIsAmbiguous(t *testing.T) {
	home := sandbox(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.Local)
	writeHardwareCLIIndex(t, home, "same-id", base)
	writeUploadedCaptureIndex(t, home, "same-id", "同 ID 会议", base.Add(time.Hour), false)

	out, code := execCLI(t, "recording", "status", "same-id", "--format", "json")
	if code == 0 || decode(t, out)["error"].(map[string]any)["code"] != "YOOOCLAW_INVALID_ARGUMENT" {
		t.Fatalf("ambiguous status must fail: code=%d %s", code, out)
	}
	out, code = execCLI(t, "recording", "status", "same-id", "--source", "smart_hardware", "--format", "json")
	if code != 0 || decode(t, out)["recording"].(map[string]any)["source_type"] != "mobile_app" {
		t.Fatalf("smart_hardware alias should resolve to mobile_app: code=%d %s", code, out)
	}
	out, code = execCLI(t, "recording", "status", "same-id", "--source", "mobile_app", "--format", "json")
	if code != 0 || decode(t, out)["recording"].(map[string]any)["source_type"] != "mobile_app" {
		t.Fatalf("explicit mobile_app should disambiguate: code=%d %s", code, out)
	}
	out, code = execCLI(t, "recording", "list", "--source", "mobile", "--format", "json")
	if code == 0 || decode(t, out)["error"].(map[string]any)["code"] != "YOOOCLAW_INVALID_ARGUMENT" {
		t.Fatalf("invalid source should fail: code=%d %s", code, out)
	}
}
