package autostart

import (
	"strings"
	"testing"
	"time"
)

func TestDeferredTriggerPreservesTaskAndReplacesOnlyOwnedTrigger(t *testing.T) {
	raw := `<?xml version="1.0" encoding="UTF-16"?><Task xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><Triggers><LogonTrigger><UserId>sid</UserId><Enabled>true</Enabled></LogonTrigger><TimeTrigger id="other"><StartBoundary>2026-01-01T00:00:00Z</StartBoundary></TimeTrigger><TimeTrigger id="YoooClawAgentDeferredStart"><StartBoundary>2025-01-01T00:00:00Z</StartBoundary></TimeTrigger></Triggers><Actions><Exec><Command>C:\中文 &amp; user\host.exe</Command></Exec></Actions><Settings><Enabled>true</Enabled></Settings></Task>`
	at := time.Date(2026, 9, 8, 10, 20, 30, 0, time.FixedZone("CST", 8*3600))
	for range 2 {
		updated, err := withDeferredTrigger(raw, at)
		if err != nil {
			t.Fatal(err)
		}
		d, err := parseTaskDefinition(updated)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Triggers.Time) != 2 || len(d.Triggers.Logon) != 1 || !d.Settings.Enabled || d.Actions.Exec[0].Command != `C:\中文 & user\host.exe` {
			t.Fatalf("definition corrupted: %+v", d)
		}
		if !strings.Contains(updated, `<TimeTrigger id="other"><StartBoundary>2026-01-01T00:00:00Z</StartBoundary></TimeTrigger>`) {
			t.Fatal("unrelated trigger changed")
		}
		if strings.Count(updated, `id="YoooClawAgentDeferredStart"`) != 1 || !strings.Contains(updated, at.Format(time.RFC3339)) {
			t.Fatal("owned trigger not replaced exactly once")
		}
		raw = updated
	}
}

func TestDeferredTriggerRejectsInvalidXML(t *testing.T) {
	for _, raw := range []string{"<Task>", "<Task></Task>"} {
		if _, err := withDeferredTrigger(raw, time.Now()); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
