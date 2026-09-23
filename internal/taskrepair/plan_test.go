package taskrepair

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	sid, profile := "S-1-5-21-1-2-3-1001", `C:\Users\Ying`
	host := profile + `\AppData\Local\YoooClaw\bin\yoooclaw-repair-host.exe`
	raw := XML(sid, profile+`\.yoooclaw`, host)
	resolve := func(s string) (string, error) {
		if s == "Ying" {
			return sid, nil
		}
		return s, nil
	}
	legacy := strings.ReplaceAll(strings.ReplaceAll(raw, host, profile+`\.workbuddy\binaries\node\versions\22\node_modules\@yoooclaw\cli\node_modules\@yoooclaw\cli-win32-x64\bin\yc.exe`), "--host", `daemon run-service --root C:\Users\Ying\.yoooclaw --format json`)
	for _, input := range []string{raw, legacy, strings.ReplaceAll(raw, sid, "Ying")} {
		if err := Validate(input, sid, profile, resolve); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []string{
		strings.ReplaceAll(raw, sid, "S-1-5-21-1-2-3-1002"),
		strings.ReplaceAll(raw, "LeastPrivilege", "HighestAvailable"),
		strings.ReplaceAll(raw, "InteractiveToken", "Password"),
		strings.ReplaceAll(raw, "--host", "--host --anything"),
		strings.ReplaceAll(raw, host, `C:\malware.exe`),
		strings.ReplaceAll(raw, "</Actions>", "<ComHandler/></Actions>"),
		strings.ReplaceAll(legacy, `versions\22`, `versions\..\22`),
		strings.ReplaceAll(raw, `C:\Users\Ying\.yoooclaw`, `C:\Users\Other\.yoooclaw`),
	} {
		if err := Validate(input, sid, profile, resolve); err == nil {
			t.Fatalf("accepted unsafe task: %s", input)
		}
	}
}
