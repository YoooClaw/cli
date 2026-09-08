//go:build !windows || !nativesetup

package installer

// Plain CLI builds can install themselves; setup embeds a matching CLI image.
var BundledCLI []byte
