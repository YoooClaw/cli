//go:build keychain_integration

// 该文件含会真正写入系统钥匙串的破坏性用例，默认不跑。
// 本地手动验证：go test -tags=keychain_integration ./internal/keychain/...
// （macOS 上首次可能弹出钥匙串授权对话框。）
package keychain

import "testing"

func TestSetGetRoundTrip(t *testing.T) {
	if !Available() {
		t.Skip("keychain backend not available on this host")
	}
	const service = "yoooclaw-integration-test"
	const account = "round-trip"
	if !Set(service, account, "integration-value") {
		t.Fatal("Set should succeed when keychain is available")
	}
	got := Get(service, account)
	if !got.Available || got.Value != "integration-value" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}
