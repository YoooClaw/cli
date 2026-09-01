package cli

import (
	"encoding/base64"
	"encoding/binary"
	"strconv"
	"strings"
	"unicode/utf16"
)

// windowsRemovalScript 等待当前 CLI 退出，再重试删除 yoooclaw.exe 和
// yc.exe。使用 LiteralPath 和单引号转义，避免安装路径中的 PowerShell
// 特殊字符被解释。
func windowsRemovalScript(parentPID int, paths []string) string {
	quoted := make([]string, 0, len(paths))
	for _, path := range paths {
		quoted = append(quoted, "'"+strings.ReplaceAll(path, "'", "''")+"'")
	}
	return strings.Join([]string{
		"$ErrorActionPreference = 'SilentlyContinue'",
		"$paths = @(" + strings.Join(quoted, ", ") + ")",
		"try { Wait-Process -Id " + strconv.Itoa(parentPID) + " -ErrorAction SilentlyContinue } catch {}",
		"$deadline = [DateTime]::UtcNow.AddSeconds(30)",
		"do {",
		"  $remaining = $false",
		"  foreach ($path in $paths) {",
		"    if (Test-Path -LiteralPath $path) {",
		"      Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue",
		"      if (Test-Path -LiteralPath $path) { $remaining = $true }",
		"    }",
		"  }",
		"  if ($remaining) { Start-Sleep -Milliseconds 200 }",
		"} while ($remaining -and [DateTime]::UtcNow -lt $deadline)",
	}, "\r\n")
}

// encodePowerShellCommand 按 powershell.exe -EncodedCommand 要求编码为
// UTF-16LE 后再做 Base64。
func encodePowerShellCommand(script string) string {
	encoded := utf16.Encode([]rune(script))
	bytes := make([]byte, len(encoded)*2)
	for i, value := range encoded {
		binary.LittleEndian.PutUint16(bytes[i*2:], value)
	}
	return base64.StdEncoding.EncodeToString(bytes)
}
