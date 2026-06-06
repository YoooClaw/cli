// Package version 暴露 CLI 版本号。
//
// Version 由构建期通过 -ldflags 注入，与 npm 包 version 同源：
//
//	go build -ldflags "-X github.com/YoooClaw/cli/internal/version.Version=<v>" ./cmd/yc
//
// 本地直接 go run/build 时回退为 "dev"。
package version

import (
	"os"
	"strings"
)

// Version 是 CLI 版本号；构建期注入，默认 "dev"。
var Version = "dev"

// Dist 推断安装来源（"npm" / "native"）。
//
// Go 重写后同一份二进制既走 npm 平台子包也走 GitHub Release，无法在构建期固定区分，
// 因此运行期按可执行文件路径是否在 node_modules 下来判定，决定 update 的升级命令。
func Dist() string {
	exe, err := os.Executable()
	if err == nil && strings.Contains(exe, "node_modules") {
		return "npm"
	}
	return "native"
}
