package transfer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportRecordsAndManifestRoundTrip(t *testing.T) {
	testExportRecordsAndManifest(t, 100, 64*1024)
}

// Shared by race-tested small fixtures and the full-size capacity regression.
func testExportRecordsAndManifest(t *testing.T, count, contentBytes int) {
	t.Helper()
	for _, tc := range []struct {
		name          string
		count         int
		content       string
		largeManifest bool
	}{
		{name: "records", count: count, content: "test"},
		{name: "manifest", count: 1, content: strings.Repeat("x", contentBytes+1), largeManifest: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			rows := make([]map[string]any, tc.count)
			for i := range rows {
				rows[i] = map[string]any{"appName": "test", "title": fmt.Sprint(i), "content": tc.content, "timestamp": "2026-11-01T00:00:00Z"}
			}
			writeJSON(t, filepath.Join(root, "2026-11-01.json"), rows)
			out := filepath.Join(t.TempDir(), "package")
			got, err := Export(Roots{TypeNotifications: root}, out, ExportOptions{Include: TypeNotifications})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Records) != tc.count || len(got.Warnings) != 0 {
				t.Fatalf("records=%d warnings=%v", len(got.Records), got.Warnings)
			}
			if tc.largeManifest {
				info, err := os.Stat(filepath.Join(out, "manifest.json"))
				if err != nil {
					t.Fatal(err)
				}
				if info.Size() <= int64(contentBytes) {
					t.Fatalf("manifest too small: %d", info.Size())
				}
			}
			restored, err := ReadPackage(out, ReadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(restored.Records) != tc.count {
				t.Fatalf("lost records: %d", len(restored.Records))
			}
		})
	}
}
