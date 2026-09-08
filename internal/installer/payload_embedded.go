//go:build windows && nativesetup

package installer

import _ "embed"

//go:embed cli.bin
var BundledCLI []byte
