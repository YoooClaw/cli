//go:build windows

package wintask

import (
	"fmt"
	"github.com/go-ole/go-ole"
	"runtime"
	"syscall"
	"unsafe"
)

// Native ITaskService::Connect avoids IDispatch optional-argument coercion.
// Layout and IID are defined by Microsoft's taskschd.h. On Windows x64/ARM64
// these 24-byte VARIANT arguments are passed indirectly by the native ABI.
type taskServiceVtbl struct {
	ole.IDispatchVtbl
	GetFolder, GetRunningTasks, NewTask, Connect uintptr
}

func connectLocal(service *ole.IDispatch) error {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return fmt.Errorf("原生修复工具仅支持 Windows 64 位")
	}
	native, err := service.QueryInterface(ole.NewGUID("{2FABA4C7-4DA9-4013-9697-20CC3FD40F85}"))
	if err != nil {
		return &nativeConnectError{phase: "QueryInterface(ITaskService)", err: err}
	}
	defer native.Release()
	vtable := (*taskServiceVtbl)(unsafe.Pointer(native.RawVTable))
	var args [4]ole.VARIANT // VT_EMPTY: local computer, current token, no credentials.
	hr, _, _ := syscall.SyscallN(vtable.Connect, uintptr(unsafe.Pointer(native)),
		uintptr(unsafe.Pointer(&args[0])), uintptr(unsafe.Pointer(&args[1])),
		uintptr(unsafe.Pointer(&args[2])), uintptr(unsafe.Pointer(&args[3])))
	runtime.KeepAlive(args)
	runtime.KeepAlive(native)
	if uint32(hr)&0x80000000 != 0 {
		return ole.NewError(uintptr(uint32(hr)))
	}
	return nil
}

type nativeConnectError struct {
	phase string
	err   error
}

func (e *nativeConnectError) Error() string { return e.phase + ": " + e.err.Error() }
func (e *nativeConnectError) Unwrap() error { return e.err }
