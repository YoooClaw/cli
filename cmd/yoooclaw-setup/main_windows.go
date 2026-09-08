//go:build windows

// Native setup has its own entry point, so browser-renamed downloads still
// install correctly. Its release build embeds the matching CLI and native host.
package main

import "github.com/YoooClaw/cli/internal/cli"

func main() { cli.ExecuteSetup() }
