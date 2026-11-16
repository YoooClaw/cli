package cli

import "testing"

func TestParseRecordingDeleteSource(t *testing.T) {
	valid := map[string]string{
		"":               recordingSourceMobileApp,
		"mobile_app":     recordingSourceMobileApp,
		"MOBILE_APP":     recordingSourceMobileApp,
		"smart_hardware": recordingSourceMobileApp,
		"capture_app":    "capture_app",
	}
	for raw, want := range valid {
		got, err := parseRecordingDeleteSource(raw)
		if err != nil {
			t.Errorf("parseRecordingDeleteSource(%q) error: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("parseRecordingDeleteSource(%q) = %q, want %q", raw, got, want)
		}
	}
	for _, raw := range []string{"all", "unknown"} {
		if _, err := parseRecordingDeleteSource(raw); err == nil {
			t.Errorf("parseRecordingDeleteSource(%q) should fail", raw)
		}
	}
}
