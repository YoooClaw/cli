// Command yc 是 yoooclaw CLI 的 Go 实现入口。
//
// 真实命令树与业务实现在 internal/cli 下逐步补齐（见 Go 重写计划 Phase 1+）；
// 本文件只负责装配 root 命令并执行。
package main

import "github.com/YoooClaw/cli/internal/cli"

func main() {
	cli.Execute()
}
