//go:build !darwin && !linux

package recording

import "os"

func exportFileIdentity(os.FileInfo) (uint64, uint64) { return 0, 0 }
