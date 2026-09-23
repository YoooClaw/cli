package taskrepair

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

type automationError struct {
	code uintptr
	sub  error
}

func TestStepDiagnostics(t *testing.T) {
	for _, step := range []string{"CoInitializeEx", "CreateObject", "QueryInterface", "Connect", "GetFolder", "GetTask"} {
		err := &StepError{Step: step, Err: automationError{0x80020009, exception(0x80070002)}}
		if !strings.Contains(err.Error(), step) || !strings.Contains(err.Error(), "0x80070002") || !strings.Contains(err.Error(), "0x80020009") {
			t.Fatal(err)
		}
		if MissingTask(err) != (step == "GetFolder" || step == "GetTask") {
			t.Fatal(err)
		}
	}
	for _, code := range []uintptr{0x80070005, 0x8000FFFF, 0x80041315} {
		if MissingTask(&StepError{Step: "GetTask", Err: automationError{code, nil}}) {
			t.Fatalf("misclassified %x", code)
		}
	}
}

func (e automationError) Error() string   { return "COM error" }
func (e automationError) Code() uintptr   { return e.code }
func (e automationError) SubError() error { return e.sub }

type exception uint32

func (e exception) Error() string { return "exception" }
func (e exception) SCODE() uint32 { return uint32(e) }
func TestAccessDenied(t *testing.T) {
	for _, e := range []error{automationError{0x80070005, nil}, automationError{0x80020009, exception(0x80070005)}, fmt.Errorf("wrapped: %w", automationError{0x80070005, nil})} {
		if !AccessDenied(e) {
			t.Fatal(e)
		}
	}
	for _, e := range []error{nil, errors.New("Access is denied"), automationError{0x80070002, nil}, automationError{0x80020009, exception(0x800704ec)}} {
		if AccessDenied(e) {
			t.Fatalf("must not elevate for %v", e)
		}
	}
}
