//go:build windows

// Package winproc contains native process queries shared by the CLI and its
// GUI host. It never executes a shell or reads unrelated process arguments.
package winproc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Identity struct {
	PID        uint32 `json:"pid"`
	ParentPID  uint32 `json:"parentPid"`
	Created    uint64 `json:"created"`
	Executable string `json:"executable"`
}

func Inspect(pid uint32) (Identity, error) {
	p := Identity{PID: pid}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return p, err
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return p, err
	}
	if code != 259 {
		return p, fmt.Errorf("process %d has exited", pid)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return p, err
	}
	p.Created = uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	buf := make([]uint16, 32768)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return p, err
	}
	p.Executable = windows.UTF16ToString(buf[:n])
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return p, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID == pid {
			p.ParentPID = entry.ParentProcessID
			return p, nil
		}
	}
	return p, fmt.Errorf("process %d disappeared from snapshot", pid)
}

func Same(a, b Identity) bool {
	return a.PID == b.PID && a.Created == b.Created && strings.EqualFold(filepath.Clean(a.Executable), filepath.Clean(b.Executable))
}

// OwnJob associates the host before it creates any child, eliminating the
// start/AssignProcess race. Closing the host's last handle kills its children.
// The handle is deliberately kept until process exit (the host is also a member).
func OwnJob() (windows.Handle, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err == nil {
		err = windows.AssignProcessToJobObject(h, windows.CurrentProcess())
	}
	if err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

// Unlink removes a mapped executable's public name when the filesystem supports
// POSIX deletion. Failure is reported; it never means a scheduled delete worked.
func Unlink(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	flags := uint32(0x1 | 0x2 | 0x10)
	err = windows.SetFileInformationByHandle(h, 21, (*byte)(unsafe.Pointer(&flags)), 4)
	windows.CloseHandle(h)
	if _, check := os.Lstat(path); os.IsNotExist(check) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("path still exists: %s", path)
}
