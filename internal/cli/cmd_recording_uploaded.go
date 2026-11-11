package cli

import (
	"github.com/YoooClaw/cli/internal/capturerecording"
	"github.com/YoooClaw/cli/internal/fsutil"
	"github.com/YoooClaw/cli/internal/recording"
)

// addCaptureUploadPaths resolves the gateway-written capture index artifacts.
// Capture recordings are only written through recordings.result.write.
func addCaptureUploadPaths(root string, entry recording.Entry, list, detail map[string]any) {
	missing := []string{}
	for _, artifact := range []struct{ kind, relative string }{
		{"audio", entry.AudioFile},
		{"transcript", entry.TranscriptFile},
		{"summary", entry.SummaryFile},
	} {
		resolved, ok := fsutil.ResolveExistingRegularFile(root, artifact.relative)
		if artifact.relative != "" && !ok {
			missing = append(missing, artifact.kind)
		}
		list["has_"+artifact.kind] = ok
		detail["has_"+artifact.kind] = ok
		detail[artifact.kind+"_path"] = nilIfEmpty(resolved)
	}
	for _, item := range []map[string]any{list, detail} {
		item["source_type"] = capturerecording.SourceType
		item["source_name"] = capturerecording.SourceName
		item["missing_artifacts"] = missing
	}
	detail["diagnostics"] = []string{}
}
