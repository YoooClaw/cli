//go:build !race

package transfer

import "testing"

// Capacity checks exercise the former hard limits at their original scale.
// CI runs them explicitly without race/coverage instrumentation. The same
// helpers run with small fixtures in every race/coverage suite.
func TestTransferScale(t *testing.T) {
	t.Run("export", func(t *testing.T) {
		testExportRecordsAndManifest(t, 100001, 64*1024*1024)
	})
	t.Run("business-records", func(t *testing.T) {
		testImportBusinessRecords(t, 100001)
	})
	t.Run("files-and-metadata", func(t *testing.T) {
		testFullImportFilesAndMetadata(t, 100001, 64*1024*1024)
	})
}
