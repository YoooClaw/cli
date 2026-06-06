// Package version 暴露 CLI 版本号。
//
// Version 由构建期通过 -ldflags 注入，与 npm 包 version 同源：
//
//	go build -ldflags "-X github.com/YoooClaw/cli/internal/version.Version=<v>" ./cmd/yc
//
// 本地直接 go run/build 时回退为 "dev"。
package version

// Version 是 CLI 版本号；构建期注入，默认 "dev"。
var Version = "dev"
