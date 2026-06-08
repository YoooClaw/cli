package errs

import "testing"

func TestNewDefaults(t *testing.T) {
	t.Parallel()
	e := New(CodeInvalidArgument, "bad arg")
	if e.Code != CodeInvalidArgument {
		t.Errorf("code = %q, want %q", e.Code, CodeInvalidArgument)
	}
	if e.Error() != "bad arg" {
		t.Errorf("Error() = %q, want %q", e.Error(), "bad arg")
	}
	if e.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", e.ExitCode)
	}
	if e.Details == nil {
		t.Error("Details should be non-nil empty map")
	}
}

func TestNewWithDetails(t *testing.T) {
	t.Parallel()
	e := New(CodeNotFound, "missing", map[string]any{"id": "rec_1"})
	if e.Details["id"] != "rec_1" {
		t.Errorf("Details[id] = %v, want rec_1", e.Details["id"])
	}
}

func TestNewf(t *testing.T) {
	t.Parallel()
	e := Newf(CodeInvalidArgument, "want %s got %d", "num", 7)
	if e.Message != "want num got 7" {
		t.Errorf("Message = %q", e.Message)
	}
}

func TestWithHint(t *testing.T) {
	t.Parallel()
	e := New(CodeInvalidArgument, "x").WithHint("try --help")
	if e.Details["hint"] != "try --help" {
		t.Errorf("hint = %v, want 'try --help'", e.Details["hint"])
	}
}

func TestPayloadFlattensDetails(t *testing.T) {
	t.Parallel()
	e := New(CodeNotFound, "missing", map[string]any{"id": "rec_1"}).WithHint("check id")
	p := e.Payload()
	if p["code"] != CodeNotFound || p["message"] != "missing" {
		t.Errorf("payload core mismatch: %+v", p)
	}
	if p["id"] != "rec_1" || p["hint"] != "check id" {
		t.Errorf("payload should flatten details: %+v", p)
	}
}

func TestNotImplemented(t *testing.T) {
	t.Parallel()
	e := NotImplemented("foo bar")
	if e.Code != CodeNotImplemented {
		t.Errorf("code = %q", e.Code)
	}
	if e.Details["hint"] == "" {
		t.Error("NotImplemented should carry a hint")
	}
}
