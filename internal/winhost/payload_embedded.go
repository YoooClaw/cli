//go:build windows && nativehost

package winhost

import _ "embed"

//go:embed host.bin
var payload []byte
