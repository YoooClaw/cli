package daemon

import "testing"

func TestLinuxProcessIdentityParsing(t *testing.T) {
	if got := linuxProcessState([]byte("123 (daemon worker) Z 1 2 3")); got != 'Z' {
		t.Fatalf("state = %q", got)
	}
	if !isDaemonCommandLine([]byte("/usr/bin/yc\x00daemon\x00run-foreground\x00--profile\x00default\x00")) {
		t.Fatal("daemon run-foreground command line was not recognized")
	}
	if !isDaemonCommandLine([]byte("/usr/local/bin/yoooclaw\x00daemon\x00run-service\x00--root\x00/root/.yoooclaw\x00")) {
		t.Fatal("systemd managed daemon command line was not recognized")
	}
	if !isDaemonCommandLine([]byte("/tmp/host\x00--yclib-daemon-bootstrap\x00")) {
		t.Fatal("yclib bootstrap command line was not recognized")
	}
	if isDaemonCommandLine([]byte("/usr/bin/yc\x00daemon\x00status\x00")) {
		t.Fatal("normal CLI process must not satisfy daemon identity")
	}
}
