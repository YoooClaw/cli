package transfer

import (
	"encoding/json"
	"testing"
)

func sum(a, b float64) float64 { return a + b }

// 期望值取自 Node/Bun 的 JSON.stringify 与 Array.prototype.sort 实际输出。
func TestJSNumberMatchesJavaScript(t *testing.T) {
	cases := map[float64]string{
		0: "0", 1: "1", 125: "125", 1.5: "1.5", sum(0.1, 0.2): "0.30000000000000004",
		1e21: "1e+21", 1e-7: "1e-7", 123456789012345680000: "123456789012345680000",
		1.5e-7: "1.5e-7", -2.5e-10: "-2.5e-10", 5e-324: "5e-324",
		1.7976931348623157e308: "1.7976931348623157e+308", 0.000001: "0.000001", 1e20: "100000000000000000000",
	}
	for in, want := range cases {
		if got := jsNumber(in); got != want {
			t.Errorf("jsNumber(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalSortsKeysByUTF16AndEscapesLikeJSON(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(`{"b":1,"a":2,"Z":3,"é":4,"😀":5,"ｚ":6}`), &v); err != nil {
		t.Fatal(err)
	}
	// JS 按 UTF-16 码元排序：😀（D83D…）排在 ｚ（FF5A）前面，与码点序相反。
	if got, want := canonical(v), `{"Z":3,"a":2,"b":1,"é":4,"😀":5,"ｚ":6}`; got != want {
		t.Fatalf("canonical = %s, want %s", got, want)
	}
	s := "<b>&amp; \" \\ \n\t\u2028\u0001\u007f😀"
	if got, want := canonical(s), "\"<b>&amp; \\\" \\\\ \\n\\t\u2028\\u0001\u007f😀\""; got != want {
		t.Fatalf("canonical(string) = %q, want %q", got, want)
	}
}

// 插件（phone-notifications src/transfer/package.ts）对同一条记录算出的 recordId /
// recordVersion。两端任何一处规范化漂移都会让这里失败——也就意味着互导会报 CHECKSUM_MISMATCH。
func TestRecordHashesMatchPluginGolden(t *testing.T) {
	golden := []string{
		`{"type":"notifications","data":{"clientLabel":"default","appName":"com.tencent.xin","appDisplayName":"微信","title":"张三 \"引号\" <b>&amp;","content":"第一行\n第二行\t制表 \u2028 分隔 😀 \\ 反斜杠 \u0001 ctl","timestamp":"2026-09-20T08:30:00+08:00"},"assets":[],"recordId":"9d1a7d4b743194d4b0517d047dc46262a18ad9e752fd0e374789de07607d0220","recordVersion":"e2992e774b4575314e7b1198768036f8a3b43c238ce8061e46424b9baa709126","recordSchemaVersion":1,"origin":"de079cfc-8612-460f-bf6a-53c2d5eaaaf6"}`,
		`{"type":"recordings","data":{"id":"rec-1","clientLabel":"default","title":"周会","metadata":{"name":"周会 录音","created_at":"2026-09-18T10:00:00+08:00","duration_sec":125,"duration_display":"2m 5s","markers":[{"index":1,"timestamp_ms":1500.5}],"location":{"latitude":31.2304,"longitude":121.4737}}},"assets":[{"field":"audioFile","hash":"ff3ea665fc290e0c7d2fada5a161c6f19dc872c71a8cb877d9f6af1d7f21eb0c","bytes":600,"extension":".m4a"},{"field":"transcriptFile","hash":"b6fb2ed792c01584be6173da73ea5a4bac078a28e320e0c8ced2a526d4b54ae8","bytes":21,"extension":".md"}],"recordId":"rec-1","recordVersion":"bdf4d74c239e9d71c655ac883a2fff121ba5f8a15bd719d0171ff7e864e5626b","recordSchemaVersion":1,"origin":"fb9e8ea6-6ea7-4b29-8453-03bf332631ae"}`,
	}
	for _, raw := range golden {
		var r Record
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		if err := validateData(r.Type, r.Data); err != nil {
			t.Fatalf("%s: validateData: %v", r.Type, err)
		}
		if got := identity(r.Type, r.Data); got != r.RecordID {
			t.Errorf("%s: recordId = %s, want %s", r.Type, got, r.RecordID)
		}
		if got := recordVersion(r.Data, r.Assets); got != r.RecordVersion {
			t.Errorf("%s: recordVersion = %s, want %s", r.Type, got, r.RecordVersion)
		}
	}
}

func TestPortableNormalizesRecordingMetadataLikePlugin(t *testing.T) {
	var raw map[string]any
	_ = json.Unmarshal([]byte(`{"id":"r","status":"synced","audioFile":"audio/r.m4a","metadata":{"name":"n","duration_sec":3725.9,"duration_display":"stale","created_at":"2026-01-01T00:00:00Z","oss_audio_url":"https://signed","size_bytes":9,"markers":[{"index":1,"timestamp_ms":2,"x":1},{"index":"bad"}],"location":{"latitude":1,"longitude":2,"alt":3}}}`), &raw)
	got := canonical(portable(TypeRecordings, raw))
	want := `{"id":"r","metadata":{"created_at":"2026-01-01T00:00:00Z","duration_display":"1h 2m 5s","duration_sec":3725,"location":{"latitude":1,"longitude":2},"markers":[{"index":1,"timestamp_ms":2}],"name":"n"}}`
	if got != want {
		t.Fatalf("portable = %s\nwant       %s", got, want)
	}
}

func TestCheckCapabilitiesAcceptsPluginFile(t *testing.T) {
	var plugin map[string]any
	_ = json.Unmarshal([]byte(`{"pluginVersion":"1.18.0","supportedSchemaVersions":[1],"supportedRecordVersions":[1],"supportedDataTypes":["notifications","recordings","web-pages","images"],"notificationImportPolicyVersion":1,"transport":["local"]}`), &plugin)
	if err := CheckCapabilities(plugin); err != nil {
		t.Fatalf("plugin capabilities rejected: %v", err)
	}
	self := toAny(Capabilities()).(map[string]any)
	if err := CheckCapabilities(self); err != nil {
		t.Fatalf("own capabilities rejected: %v", err)
	}
	delete(plugin, "notificationImportPolicyVersion")
	if ErrorCode(CheckCapabilities(plugin)) != "VERSION_UNSUPPORTED" {
		t.Fatal("expected VERSION_UNSUPPORTED")
	}
}
