//go:build windows

package autostart

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

func nativeTaskSchedulerCOM(action string, args ...string) ([]byte, error) {
	if action == "identity" {
		token, err := syscall.OpenCurrentProcessToken()
		if err != nil {
			return nil, fmt.Errorf("OpenProcessToken: %w", err)
		}
		defer token.Close()
		user, err := token.GetTokenUser()
		if err != nil {
			return nil, fmt.Errorf("GetTokenInformation(TokenUser): %w", err)
		}
		sid, err := user.User.Sid.String()
		return []byte(sid), err
	}

	// COM interfaces and CoUninitialize must stay on the initializing OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	var oleErr *ole.OleError
	// go-ole returns S_FALSE as an error for an already initialized apartment;
	// it is still successful and requires the matching CoUninitialize.
	if err != nil && !(errors.As(err, &oleErr) && oleErr.Code() == 1) {
		return nil, nativeTaskSchedulerError("CoInitializeEx", err)
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("Schedule.Service")
	if err != nil {
		return nil, nativeTaskSchedulerError("CreateObject(Schedule.Service)", err)
	}
	defer unknown.Release()
	dispatch, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, nativeTaskSchedulerError("QueryInterface(IDispatch)", err)
	}
	service := &nativeTaskSchedulerObject{dispatch: dispatch}
	defer service.Release()
	return taskSchedulerAction(service, action, args...)
}

type nativeTaskSchedulerObject struct{ dispatch *ole.IDispatch }

func (o *nativeTaskSchedulerObject) Release() { o.dispatch.Release() }

func (o *nativeTaskSchedulerObject) Object(method string, args ...any) (taskSchedulerObject, error) {
	var value *ole.VARIANT
	var err error
	if method == "Item" {
		value, err = oleutil.GetProperty(o.dispatch, method, args...)
	} else {
		value, err = oleutil.CallMethod(o.dispatch, method, args...)
	}
	if value != nil {
		defer value.Clear()
	}
	if err != nil {
		return nil, nativeTaskSchedulerError(method, err)
	}
	if value == nil || value.ToIDispatch() == nil {
		return nil, fmt.Errorf("Task Scheduler %s did not return an IDispatch object", method)
	}
	// Take our own reference before VariantClear releases the result's reference.
	dispatch := value.ToIDispatch()
	dispatch.AddRef()
	return &nativeTaskSchedulerObject{dispatch: dispatch}, nil
}

func (o *nativeTaskSchedulerObject) Call(method string, args ...any) error {
	value, err := oleutil.CallMethod(o.dispatch, method, args...)
	if value != nil {
		defer value.Clear() // Also releases objects returned by RegisterTask/Run.
	}
	if err != nil {
		return nativeTaskSchedulerError(method, err)
	}
	return nil
}

func (o *nativeTaskSchedulerObject) Int(property string) (int, error) {
	value, err := oleutil.GetProperty(o.dispatch, property)
	if value != nil {
		defer value.Clear()
	}
	if err != nil {
		return 0, nativeTaskSchedulerError(property, err)
	}
	if value == nil || (value.VT != ole.VT_I4 && value.VT != ole.VT_INT && value.VT != ole.VT_UI4) {
		return 0, fmt.Errorf("Task Scheduler %s did not return an integer", property)
	}
	return int(int32(value.Val)), nil
}

func (o *nativeTaskSchedulerObject) String(property string) (string, error) {
	value, err := oleutil.GetProperty(o.dispatch, property)
	if value != nil {
		defer value.Clear()
	}
	if err != nil {
		return "", nativeTaskSchedulerError(property, err)
	}
	if value == nil || value.VT != ole.VT_BSTR {
		return "", fmt.Errorf("Task Scheduler %s did not return a string", property)
	}
	return value.ToString(), nil
}

func nativeTaskSchedulerError(operation string, err error) error {
	var oleErr *ole.OleError
	if !errors.As(err, &oleErr) {
		return fmt.Errorf("Task Scheduler %s: %w", operation, err)
	}
	hresult := uint32(oleErr.Code())
	if hresult == 0x80020009 { // DISP_E_EXCEPTION wraps the server's real HRESULT.
		if info, ok := oleErr.SubError().(ole.EXCEPINFO); ok && info.SCODE() != 0 {
			hresult = info.SCODE()
		}
	}
	return &taskSchedulerError{operation: operation, hresult: hresult, cause: err}
}
