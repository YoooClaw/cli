package cli

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestWindowsRemovalScriptWaitsAndRemovesBothBinaries(t *testing.T) {
	t.Parallel()

	script := windowsRemovalScript(1234, []string{
		`C:\Users\O'Brien\YoooClaw\bin\yoooclaw.exe`,
		`C:\Users\O'Brien\YoooClaw\bin\yc.exe`,
	})
	for _, want := range []string{
		"Wait-Process -Id 1234",
		`'C:\Users\O''Brien\YoooClaw\bin\yoooclaw.exe'`,
		`'C:\Users\O''Brien\YoooClaw\bin\yc.exe'`,
		"Remove-Item -LiteralPath $path -Force",
		"AddSeconds(30)",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("windowsRemovalScript() missing %q\n%s", want, script)
		}
	}
}

func TestEncodePowerShellCommandUsesUTF16LE(t *testing.T) {
	t.Parallel()

	want := "路径 with spaces"
	raw, err := base64.StdEncoding.DecodeString(encodePowerShellCommand(want))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("encoded command has odd UTF-16 byte count: %d", len(raw))
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	if got := string(utf16.Decode(units)); got != want {
		t.Fatalf("decoded command = %q, want %q", got, want)
	}
}
