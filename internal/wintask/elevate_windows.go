//go:build windows

package wintask

import (
	"fmt"
	"golang.org/x/sys/windows"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var messageBox = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
var shellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

func message(body string, flags uintptr) uintptr {
	r, _, _ := messageBox.Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(body))), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("YoooClaw 连接修复"))), flags)
	return r
}

type shellInfo struct {
	Size, Mask                        uint32
	Window                            uintptr
	Verb, File, Parameters, Directory *uint16
	Show                              int32
	Instance, IDList                  uintptr
	Class                             *uint16
	ClassKey                          uintptr
	HotKey                            uint32
	Icon                              uintptr
	Process                           windows.Handle
}

func Elevate(exe string, args ...string) error {
	for i := range args {
		args[i] = syscall.EscapeArg(args[i])
	}
	info := shellInfo{Mask: 0x40 | 0x100, Verb: windows.StringToUTF16Ptr("runas"), File: windows.StringToUTF16Ptr(exe), Parameters: windows.StringToUTF16Ptr(strings.Join(args, " ")), Show: 1}
	info.Size = uint32(unsafe.Sizeof(info))
	ok, _, err := shellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	runtime.KeepAlive(info)
	if ok == 0 {
		return fmt.Errorf("管理员授权未完成：%w", err)
	}
	if info.Process == 0 {
		return fmt.Errorf("未获得修复进程句柄")
	}
	defer windows.CloseHandle(info.Process)
	if _, err = windows.WaitForSingleObject(info.Process, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err = windows.GetExitCodeProcess(info.Process, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("管理员修复阶段失败（%d），请查看该阶段提示；没有自动重试", code)
	}
	return nil
}
