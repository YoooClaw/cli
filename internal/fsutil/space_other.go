//go:build !darwin && !linux

package fsutil

// Other supported platforms retain their filesystem write-error handling.
func RequireSpace(path string, bytes int64) error { return nil }
