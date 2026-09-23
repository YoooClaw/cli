package taskrepair

import (
	"errors"
	"fmt"
	"strings"
)

type StepError struct {
	Step string
	Err  error
}

func (e *StepError) Error() string { return e.Step + ": " + DescribeError(e.Err) }
func (e *StepError) Unwrap() error { return e.Err }

func codes(err error) []uint32 {
	var result []uint32
	for depth := 0; err != nil && depth < 8; depth++ {
		if e, ok := err.(interface{ Code() uintptr }); ok {
			result = append(result, uint32(e.Code()))
		}
		if e, ok := err.(interface{ SCODE() uint32 }); ok && e.SCODE() != 0 {
			result = append(result, e.SCODE())
		}
		if e, ok := err.(interface{ SubError() error }); ok {
			err = e.SubError()
			continue
		}
		if e, ok := err.(interface{ Unwrap() error }); ok {
			err = e.Unwrap()
			continue
		}
		break
	}
	return result
}

func DescribeError(err error) string {
	if err == nil {
		return "success"
	}
	var parts []string
	for _, code := range codes(err) {
		parts = append(parts, fmt.Sprintf("HRESULT=0x%08X", code))
	}
	if len(parts) == 0 {
		parts = append(parts, "HRESULT=unavailable")
	}
	return strings.Join(parts, "; ") + "; " + err.Error()
}

// An inaccessible or unavailable service is never treated as a missing task.
func MissingTask(err error) bool {
	var step *StepError
	if !errors.As(err, &step) || (step.Step != "GetFolder" && step.Step != "GetTask") {
		return false
	}
	for _, code := range codes(step.Err) {
		if code == 0x80070002 || code == 0x80070003 {
			return true
		}
	}
	return false
}

// AccessDenied recognizes the actual HRESULT, including Automation's nested
// EXCEPINFO. It does not infer permission errors from translated error text.
func AccessDenied(err error) bool {
	for depth := 0; err != nil && depth < 8; depth++ {
		if e, ok := err.(interface{ Code() uintptr }); ok && uint32(e.Code()) == 0x80070005 {
			return true
		}
		if e, ok := err.(interface{ SCODE() uint32 }); ok && e.SCODE() == 0x80070005 {
			return true
		}
		if e, ok := err.(interface{ SubError() error }); ok {
			err = e.SubError()
			continue
		}
		if e, ok := err.(interface{ Unwrap() error }); ok {
			err = e.Unwrap()
			continue
		}
		break
	}
	return false
}
