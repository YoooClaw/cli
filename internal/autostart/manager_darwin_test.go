//go:build darwin

package autostart

import (
	"strings"
	"testing"
)

func TestPlistUsesArgumentArrayAndEscapesValues(t *testing.T) {
	spec := Spec{
		RootDir:       "/tmp/root & profile",
		Executable:    "/tmp/Yooo Claw/yoooclaw",
		Arguments:     []string{"daemon", "run-service", "--root", "/tmp/root & profile"},
		SupervisorLog: "/tmp/root & profile/logs/supervisor.log",
	}
	xml := plistXML("com.yoooclaw.test", spec)
	for _, want := range []string{
		"<key>ProgramArguments</key>",
		"<string>/tmp/Yooo Claw/yoooclaw</string>",
		"<string>/tmp/root &amp; profile</string>",
		"<key>SuccessfulExit</key><false/>",
	} {
		if !strings.Contains(xml, want) {
			t.Fatalf("plist missing %q:\n%s", want, xml)
		}
	}
	if strings.Contains(xml, "sh -c") {
		t.Fatal("plist must not execute through a shell")
	}
}
