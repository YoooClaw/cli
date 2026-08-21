package voice

import (
	"github.com/YoooClaw/cli/internal/fsutil"
)

func resolveAudioPath(root, relative string) (string, bool) {
	return fsutil.ResolveExistingRegularFile(root, relative)
}
