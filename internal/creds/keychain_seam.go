package creds

import "github.com/YoooClaw/cli/internal/keychain"

// keychain seam —— 生产指向真实 keychain 包；测试通过替换这些函数变量来注入
// 假后端，从而在任意平台（含无钥匙串的 CI）确定性地覆盖 keychain 分支，
// 且绝不触碰真实系统钥匙串。见 *_test.go 中的 withFakeKeychain。
var (
	keychainAvailableFn = keychain.Available
	keychainGetFn       = keychain.Get
	keychainSetFn       = keychain.Set
)
