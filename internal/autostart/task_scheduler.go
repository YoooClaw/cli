package autostart

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// This small boundary keeps the Task Scheduler operation flow testable without
// a Windows service. The Windows adapter owns all COM/VARIANT lifetimes.
type taskSchedulerObject interface {
	Object(method string, args ...any) (taskSchedulerObject, error)
	Call(method string, args ...any) error
	Int(property string) (int, error)
	String(property string) (string, error)
	Release()
}

type taskSchedulerError struct {
	operation string
	hresult   uint32
	cause     error
}

func (e *taskSchedulerError) Error() string {
	return fmt.Sprintf("Task Scheduler %s failed (HRESULT 0x%08X): %v", e.operation, e.hresult, e.cause)
}

func (e *taskSchedulerError) Unwrap() error { return e.cause }

func taskSchedulerNotFound(err error) bool {
	var comErr *taskSchedulerError
	if !errors.As(err, &comErr) {
		return false
	}
	// HRESULT_FROM_WIN32(ERROR_FILE_NOT_FOUND / ERROR_PATH_NOT_FOUND).
	// Access denied, policy blocks and RPC errors must never mean "missing".
	return comErr.hresult == 0x80070002 || comErr.hresult == 0x80070003
}

func taskSchedulerAction(service taskSchedulerObject, action string, args ...string) ([]byte, error) {
	wantArgs := 2
	switch action {
	case "available":
		wantArgs = 0
	case "install", "update":
		wantArgs = 4 // folder, task name, XML, current user SID
	case "status", "start", "stop", "delete", "xml", "instances":
	default:
		return nil, fmt.Errorf("未知 Task Scheduler COM 操作: %s", action)
	}
	if len(args) != wantArgs {
		return nil, fmt.Errorf("Task Scheduler %s: 需要 %d 个参数，实际 %d 个", action, wantArgs, len(args))
	}
	if err := service.Call("Connect"); err != nil {
		return nil, err
	}
	if action == "available" {
		root, err := service.Object("GetFolder", `\`)
		if err != nil {
			return nil, err
		}
		root.Release()
		return []byte("ok"), nil
	}

	folder, err := service.Object("GetFolder", args[0])
	if err != nil {
		if !taskSchedulerNotFound(err) {
			return nil, err
		}
		switch action {
		case "status":
			return []byte("missing"), nil
		case "install":
			root, rootErr := service.Object("GetFolder", `\`)
			if rootErr != nil {
				return nil, rootErr
			}
			folder, err = root.Object("CreateFolder", strings.Trim(args[0], `\`))
			root.Release()
		default:
			return nil, err
		}
		if err != nil {
			return nil, err
		}
	}
	defer folder.Release()

	switch action {
	case "install", "update":
		// TASK_CREATE_OR_UPDATE, current identity, no stored password,
		// TASK_LOGON_INTERACTIVE_TOKEN, default security descriptor.
		flags := int32(6)
		if action == "update" {
			flags = 4 | 32
		} // Update only; ignore registration triggers.
		err = folder.Call("RegisterTask", args[1], args[2], flags, args[3], nil, int32(3), nil)
	case "delete":
		err = folder.Call("DeleteTask", args[1], int32(0))
	default:
		task, taskErr := folder.Object("GetTask", args[1])
		if taskErr != nil {
			if action == "status" && taskSchedulerNotFound(taskErr) {
				return []byte("missing"), nil
			}
			return nil, taskErr
		}
		defer task.Release()
		switch action {
		case "xml":
			value, err := task.String("Xml")
			return []byte(value), err
		case "instances":
			instances, err := task.Object("GetInstances", int32(0))
			if err != nil {
				return nil, err
			}
			defer instances.Release()
			count, err := instances.Int("Count")
			if err != nil {
				return nil, err
			}
			var pids []string
			for i := 1; i <= count; i++ {
				instance, err := instances.Object("Item", int32(i))
				if err != nil {
					return nil, err
				}
				pid, err := instance.Int("EnginePID")
				instance.Release()
				if err != nil {
					return nil, err
				}
				pids = append(pids, strconv.Itoa(pid))
			}
			return []byte(strings.Join(pids, ",")), nil
		case "status":
			state, stateErr := task.Int("State")
			if stateErr != nil {
				return nil, stateErr
			}
			return []byte(strconv.Itoa(state)), nil
		case "start":
			err = task.Call("Run", nil)
		case "stop":
			err = task.Call("Stop", int32(0))
		}
	}
	if err != nil {
		return nil, err
	}
	return []byte("ok"), nil
}
