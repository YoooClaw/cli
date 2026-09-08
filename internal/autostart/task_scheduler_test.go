package autostart

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type schedulerStep struct {
	method string
	args   []any
	object taskSchedulerObject
	state  int
	text   string
	err    error
}

type scriptedSchedulerObject struct {
	t        *testing.T
	steps    []schedulerStep
	releases int
}

func schedulerObject(t *testing.T, releases int, steps ...schedulerStep) *scriptedSchedulerObject {
	t.Helper()
	o := &scriptedSchedulerObject{t: t, steps: steps}
	t.Cleanup(func() {
		if len(o.steps) != 0 || o.releases != releases {
			t.Errorf("unconsumed calls = %v, releases = %d, want %d", o.steps, o.releases, releases)
		}
	})
	return o
}

func (o *scriptedSchedulerObject) next(method string, args ...any) schedulerStep {
	o.t.Helper()
	if len(o.steps) == 0 {
		o.t.Fatalf("unexpected COM call %s(%v)", method, args)
	}
	step := o.steps[0]
	o.steps = o.steps[1:]
	if step.method != method || !reflect.DeepEqual(step.args, args) {
		o.t.Fatalf("COM call %s(%v), want %s(%v)", method, args, step.method, step.args)
	}
	return step
}

func (o *scriptedSchedulerObject) Object(method string, args ...any) (taskSchedulerObject, error) {
	step := o.next(method, args...)
	return step.object, step.err
}
func (o *scriptedSchedulerObject) Call(method string, args ...any) error {
	return o.next(method, args...).err
}
func (o *scriptedSchedulerObject) Int(property string) (int, error) {
	step := o.next(property)
	return step.state, step.err
}
func (o *scriptedSchedulerObject) Release() { o.releases++ }
func (o *scriptedSchedulerObject) String(property string) (string, error) {
	step := o.next(property)
	return step.text, step.err
}

func TestTaskSchedulerUpdateDoesNotCreateOrRunTask(t *testing.T) {
	folder := schedulerObject(t, 1, schedulerStep{method: "RegisterTask", args: []any{"daemon", "<Task/>", int32(36), "sid", nil, int32(3), nil}})
	service := schedulerObject(t, 0, schedulerStep{method: "Connect"}, schedulerStep{method: "GetFolder", args: []any{`\YoooClaw`}, object: folder})
	if _, err := taskSchedulerAction(service, "update", `\YoooClaw`, "daemon", "<Task/>", "sid"); err != nil {
		t.Fatal(err)
	}
	missing := &taskSchedulerError{hresult: 0x80070003, cause: errors.New("missing")}
	service = schedulerObject(t, 0, schedulerStep{method: "Connect"}, schedulerStep{method: "GetFolder", args: []any{`\YoooClaw`}, err: missing})
	if _, err := taskSchedulerAction(service, "update", `\YoooClaw`, "daemon", "<Task/>", "sid"); !errors.Is(err, missing) {
		t.Fatal("update must not create missing folder")
	}
}

func TestTaskSchedulerReadsXMLAndNativeInstances(t *testing.T) {
	for _, action := range []string{"xml", "instances"} {
		t.Run(action, func(t *testing.T) {
			step := schedulerStep{method: "Xml", text: "<Task/>"}
			if action == "instances" {
				instance := schedulerObject(t, 1, schedulerStep{method: "EnginePID", state: 123})
				instances := schedulerObject(t, 1, schedulerStep{method: "Count", state: 1}, schedulerStep{method: "Item", args: []any{int32(1)}, object: instance})
				step = schedulerStep{method: "GetInstances", args: []any{int32(0)}, object: instances}
			}
			task := schedulerObject(t, 1, step)
			folder := schedulerObject(t, 1, schedulerStep{method: "GetTask", args: []any{"daemon"}, object: task})
			service := schedulerObject(t, 0, schedulerStep{method: "Connect"}, schedulerStep{method: "GetFolder", args: []any{`\YoooClaw`}, object: folder})
			out, err := taskSchedulerAction(service, action, `\YoooClaw`, "daemon")
			want := "<Task/>"
			if action == "instances" {
				want = "123"
			}
			if err != nil || string(out) != want {
				t.Fatalf("out=%q err=%v", out, err)
			}
		})
	}
}

func TestTaskSchedulerStatusDoesNotMaskErrors(t *testing.T) {
	for _, atFolder := range []bool{true, false} {
		for _, code := range []uint32{0x80070002, 0x80070003, 0x80070005, 0x800704EC, 0x800706BA} {
			t.Run(fmt.Sprintf("folder=%v/%08X", atFolder, code), func(t *testing.T) {
				failure := &taskSchedulerError{operation: "GetTask", hresult: code, cause: errors.New("localized error")}
				step := schedulerStep{method: "GetFolder", args: []any{`\YoooClaw`}, err: failure}
				if !atFolder {
					step.err = nil
					step.object = schedulerObject(t, 1, schedulerStep{method: "GetTask", args: []any{"daemon"}, err: failure})
				}
				service := schedulerObject(t, 0, schedulerStep{method: "Connect"}, step)
				out, err := taskSchedulerAction(service, "status", `\YoooClaw`, "daemon")
				if code == 0x80070002 || code == 0x80070003 {
					if err != nil || string(out) != "missing" {
						t.Fatalf("missing result = %q, %v", out, err)
					}
				} else if !errors.Is(err, failure) || len(out) != 0 {
					t.Fatalf("error was masked: result = %q, %v", out, err)
				}
			})
		}
	}
}

func TestTaskSchedulerInstallOnlyCreatesMissingFolder(t *testing.T) {
	for _, code := range []uint32{0, 0x80070002, 0x80070003, 0x80070005, 0x800704EC} {
		t.Run(fmt.Sprintf("%08X", code), func(t *testing.T) {
			xml := `<Task>中文 &amp; O'Brien 😀</Task>`
			sid := "S-1-5-21-test"
			var failure error
			step := schedulerStep{method: "GetFolder", args: []any{`\YoooClaw`}}
			steps := []schedulerStep{{method: "Connect"}}
			if code != 0 {
				failure = &taskSchedulerError{operation: "GetFolder", hresult: code, cause: errors.New("denied or missing")}
				step.err = failure
			}
			if code == 0 || taskSchedulerNotFound(failure) {
				folder := schedulerObject(t, 1, schedulerStep{
					method: "RegisterTask", args: []any{"daemon", xml, int32(6), sid, nil, int32(3), nil},
				})
				if code == 0 {
					step.object = folder
					steps = append(steps, step)
				} else {
					root := schedulerObject(t, 1, schedulerStep{method: "CreateFolder", args: []any{"YoooClaw"}, object: folder})
					steps = append(steps, step, schedulerStep{method: "GetFolder", args: []any{`\`}, object: root})
				}
			} else {
				steps = append(steps, step) // No CreateFolder/RegisterTask on access or policy errors.
			}
			service := schedulerObject(t, 0, steps...)
			out, err := taskSchedulerAction(service, "install", `\YoooClaw`, "daemon", xml, sid)
			if code == 0 || taskSchedulerNotFound(failure) {
				if err != nil || string(out) != "ok" {
					t.Fatalf("install = %q, %v", out, err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("install lost original error: %v", err)
			}
		})
	}
}

func TestTaskSchedulerOperations(t *testing.T) {
	for _, action := range []string{"status", "start", "stop", "delete", "install"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%v", action, fail), func(t *testing.T) {
				var failure error
				if fail {
					failure = &taskSchedulerError{operation: action, hresult: 0x80070005, cause: errors.New("access denied")}
				}
				args := []string{`\YoooClaw`, "daemon"}
				var folder taskSchedulerObject
				switch action {
				case "delete":
					folder = schedulerObject(t, 1, schedulerStep{method: "DeleteTask", args: []any{"daemon", int32(0)}, err: failure})
				case "install":
					args = append(args, "<Task/>", "SID")
					folder = schedulerObject(t, 1, schedulerStep{
						method: "RegisterTask", args: []any{"daemon", "<Task/>", int32(6), "SID", nil, int32(3), nil}, err: failure,
					})
				default:
					step := schedulerStep{method: "State", state: 4, err: failure}
					if action == "start" {
						step.method, step.args = "Run", []any{nil}
					} else if action == "stop" {
						step.method, step.args = "Stop", []any{int32(0)}
					}
					task := schedulerObject(t, 1, step)
					folder = schedulerObject(t, 1, schedulerStep{method: "GetTask", args: []any{"daemon"}, object: task})
				}
				service := schedulerObject(t, 0, schedulerStep{method: "Connect"}, schedulerStep{
					method: "GetFolder", args: []any{`\YoooClaw`}, object: folder,
				})
				out, err := taskSchedulerAction(service, action, args...)
				if fail {
					if !errors.Is(err, failure) {
						t.Fatalf("lost operation error: %v", err)
					}
					return
				}
				want := "ok"
				if action == "status" {
					want = "4"
				}
				if err != nil || string(out) != want {
					t.Fatalf("%s = %q, %v", action, out, err)
				}
			})
		}
	}
}

func TestTaskSchedulerAvailableAndConnectionFailure(t *testing.T) {
	t.Run("available", func(t *testing.T) {
		root := schedulerObject(t, 1)
		service := schedulerObject(t, 0, schedulerStep{method: "Connect"}, schedulerStep{method: "GetFolder", args: []any{`\`}, object: root})
		if _, err := taskSchedulerAction(service, "available"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("Connect denied", func(t *testing.T) {
		failure := &taskSchedulerError{operation: "Connect", hresult: 0x80070005, cause: errors.New("access denied")}
		service := schedulerObject(t, 0, schedulerStep{method: "Connect", err: failure})
		if _, err := taskSchedulerAction(service, "available"); !errors.Is(err, failure) {
			t.Fatalf("connection error = %v", err)
		}
	})
}

func TestTaskSchedulerRejectsInvalidArgumentsWithoutCOMCalls(t *testing.T) {
	for _, action := range []string{"unknown", "install", "status", "start", "stop", "delete"} {
		t.Run(action, func(t *testing.T) {
			service := schedulerObject(t, 0)
			if _, err := taskSchedulerAction(service, action); err == nil {
				t.Fatal("invalid call accepted")
			}
		})
	}
}

func TestTaskSchedulerErrorPreservesOperationAndHRESULT(t *testing.T) {
	err := &taskSchedulerError{operation: "RegisterTask", hresult: 0x80070005, cause: errors.New("访问被拒绝")}
	for _, want := range []string{"RegisterTask", "0x80070005", "访问被拒绝"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error omits %q: %v", want, err)
		}
	}
	if !taskSchedulerNotFound(fmt.Errorf("wrapped: %w", &taskSchedulerError{hresult: 0x80070002})) {
		t.Fatal("wrapped file-not-found was not recognized")
	}
	if taskSchedulerNotFound(errors.New("file not found")) {
		t.Fatal("unstructured error was interpreted as missing")
	}
}
