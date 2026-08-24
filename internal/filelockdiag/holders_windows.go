//go:build windows

package filelockdiag

import (
	"fmt"
	"sort"
	"syscall"
	"unsafe"
)

const (
	rmSessionKeyLen = 32
	rmMaxAppName    = 255
	rmMaxSvcName    = 63
	errorMoreData   = syscall.Errno(234)
)

var (
	restartManagerDLL  = syscall.NewLazyDLL("rstrtmgr.dll")
	rmStartSessionProc = restartManagerDLL.NewProc("RmStartSession")
	rmRegisterProc     = restartManagerDLL.NewProc("RmRegisterResources")
	rmGetListProc      = restartManagerDLL.NewProc("RmGetList")
	rmEndSessionProc   = restartManagerDLL.NewProc("RmEndSession")
)

type rmUniqueProcess struct {
	ProcessID        uint32
	ProcessStartTime syscall.Filetime
}

type rmProcessInfo struct {
	Process          rmUniqueProcess
	Application      [rmMaxAppName + 1]uint16
	Service          [rmMaxSvcName + 1]uint16
	ApplicationType  uint32
	ApplicationState uint32
	TerminalSession  uint32
	Restartable      int32
}

// Lookup 使用 Windows Restart Manager 查询当前占用 path 的进程。
// Restart Manager 只枚举占用者，不会关闭句柄、停止服务或结束进程。
func Lookup(path string) ([]Holder, bool, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, true, err
	}

	var session uint32
	key := make([]uint16, rmSessionKeyLen+1)
	code, _, _ := rmStartSessionProc.Call(
		uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0])),
	)
	if code != 0 {
		return nil, true, rmError("RmStartSession", code)
	}
	defer rmEndSessionProc.Call(uintptr(session))

	files := []*uint16{pathPtr}
	code, _, _ = rmRegisterProc.Call(
		uintptr(session), 1, uintptr(unsafe.Pointer(&files[0])),
		0, 0, 0, 0,
	)
	if code != 0 {
		return nil, true, rmError("RmRegisterResources", code)
	}

	// 占用者可能在两次 RmGetList 之间变化；最多重试三次重新分配。
	for attempt := 0; attempt < 3; attempt++ {
		var needed, count, rebootReasons uint32
		code, _, _ = rmGetListProc.Call(
			uintptr(session),
			uintptr(unsafe.Pointer(&needed)),
			uintptr(unsafe.Pointer(&count)),
			0,
			uintptr(unsafe.Pointer(&rebootReasons)),
		)
		if code == 0 && needed == 0 {
			return []Holder{}, true, nil
		}
		if syscall.Errno(code) != errorMoreData {
			return nil, true, rmError("RmGetList(size)", code)
		}
		if needed == 0 {
			return []Holder{}, true, nil
		}

		infos := make([]rmProcessInfo, needed)
		count = needed
		code, _, _ = rmGetListProc.Call(
			uintptr(session),
			uintptr(unsafe.Pointer(&needed)),
			uintptr(unsafe.Pointer(&count)),
			uintptr(unsafe.Pointer(&infos[0])),
			uintptr(unsafe.Pointer(&rebootReasons)),
		)
		if syscall.Errno(code) == errorMoreData {
			continue
		}
		if code != 0 {
			return nil, true, rmError("RmGetList", code)
		}

		holders := make([]Holder, 0, count)
		for i := uint32(0); i < count; i++ {
			info := infos[i]
			holders = append(holders, Holder{
				PID:             info.Process.ProcessID,
				Application:     syscall.UTF16ToString(info.Application[:]),
				Service:         syscall.UTF16ToString(info.Service[:]),
				ApplicationType: info.ApplicationType,
				Restartable:     info.Restartable != 0,
			})
		}
		sort.Slice(holders, func(i, j int) bool { return holders[i].PID < holders[j].PID })
		return holders, true, nil
	}
	return nil, true, fmt.Errorf("RmGetList: 占用进程列表持续变化")
}

func rmError(op string, code uintptr) error {
	if code == 0 {
		return nil
	}
	return fmt.Errorf("%s: %w", op, syscall.Errno(code))
}
