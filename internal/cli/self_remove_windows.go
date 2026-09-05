//go:build windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	windowsDeleteAccess               = 0x00010000
	windowsFileShareRead              = 0x00000001
	windowsFileShareWrite             = 0x00000002
	windowsFileShareDelete            = 0x00000004
	windowsOpenExisting               = 3
	windowsFileAttributeNormal        = 0x00000080
	windowsFileDispositionInfoExClass = 21
	windowsDispositionDelete          = 0x00000001
	windowsDispositionPOSIXSemantics  = 0x00000002
	windowsDispositionIgnoreReadonly  = 0x00000010
	windowsHWNDBroadcast              = 0xffff
	windowsWMSettingChange            = 0x001a
	windowsSMTOAbortIfHung            = 0x0002
)

var (
	windowsKernel32                   = syscall.NewLazyDLL("kernel32.dll")
	windowsCreateFileW                = windowsKernel32.NewProc("CreateFileW")
	windowsSetFileInformationByHandle = windowsKernel32.NewProc("SetFileInformationByHandle")
	windowsAdvapi32                   = syscall.NewLazyDLL("advapi32.dll")
	windowsRegSetValueExW             = windowsAdvapi32.NewProc("RegSetValueExW")
	windowsRegDeleteValueW            = windowsAdvapi32.NewProc("RegDeleteValueW")
	windowsUser32                     = syscall.NewLazyDLL("user32.dll")
	windowsSendMessageTimeoutW        = windowsUser32.NewProc("SendMessageTimeoutW")
)

type windowsFileDispositionInfoEx struct {
	Flags uint32
}

type windowsUserPathState struct {
	Exists    bool
	Value     string
	ValueType uint32
}

func removeNativeSelfBinary(exe string) (binaryRemovalResult, error) {
	result := binaryRemovalResult{Removed: []string{}}
	if !isWindowsNativeBinary(exe) {
		result.Hint = "当前可执行文件不是 yoooclaw.exe 或 yc.exe，请手动删除二进制"
		return result, nil
	}

	installDir := filepath.Dir(filepath.Clean(exe))
	pathBefore, pathRemoved, err := removeWindowsInstallDirFromUserPath(installDir)
	if err != nil {
		return result, fmt.Errorf("清理用户 PATH 中的安装目录失败：%w", err)
	}

	// 先删除没有在运行的命令副本，最后再移除当前进程对应的路径。若删除
	// 失败则恢复用户 PATH，让仍在磁盘上的当前命令保持可用。
	for _, candidate := range windowsRemovalCandidates(exe) {
		if _, err := os.Lstat(candidate); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return result, restoreWindowsPathAfterRemovalFailure(
				pathBefore, pathRemoved, fmt.Errorf("检查 %s 失败：%w", candidate, err),
			)
		}

		var removeErr error
		if strings.EqualFold(filepath.Clean(candidate), filepath.Clean(exe)) {
			removeErr = removeWindowsPathNow(candidate)
		} else {
			removeErr = os.Remove(candidate)
			if removeErr == nil {
				removeErr = verifyWindowsPathRemoved(candidate)
			}
		}
		if removeErr != nil {
			return result, restoreWindowsPathAfterRemovalFailure(
				pathBefore, pathRemoved, fmt.Errorf("删除 %s 失败：%w", candidate, removeErr),
			)
		}
		result.Removed = append(result.Removed, candidate)
	}

	result.UserPathRemoved = pathRemoved
	result.InstallDirsRemoved, err = removeEmptyWindowsInstallDirs(installDir)
	if err != nil {
		return result, err
	}
	if pathRemoved {
		if err := broadcastWindowsEnvironmentChange(); err != nil {
			result.Warnings = append(result.Warnings, "用户 PATH 已清理，但环境变更广播失败；请重新打开终端："+err.Error())
		}
	}
	return result, nil
}

func restoreWindowsPathAfterRemovalFailure(before windowsUserPathState, changed bool, removeErr error) error {
	if !changed {
		return removeErr
	}
	if restoreErr := writeWindowsUserPath(before); restoreErr != nil {
		return fmt.Errorf("%w；同时恢复用户 PATH 失败：%v", removeErr, restoreErr)
	}
	return removeErr
}

func removeWindowsInstallDirFromUserPath(installDir string) (windowsUserPathState, bool, error) {
	before, err := readWindowsUserPath()
	if err != nil || !before.Exists {
		return before, false, err
	}
	updated, changed := removeWindowsPathEntry(before.Value, installDir)
	if !changed {
		return before, false, nil
	}
	after := windowsUserPathState{
		Exists:    updated != "",
		Value:     updated,
		ValueType: before.ValueType,
	}
	if err := writeWindowsUserPath(after); err != nil {
		return before, false, err
	}
	return before, true, nil
}

func removeWindowsPathEntry(pathValue, installDir string) (string, bool) {
	entries := strings.Split(pathValue, ";")
	kept := make([]string, 0, len(entries))
	removed := false
	for _, entry := range entries {
		if sameWindowsPathEntry(entry, installDir) {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !removed {
		return pathValue, false
	}
	return strings.Join(kept, ";"), true
}

func sameWindowsPathEntry(entry, target string) bool {
	entry = strings.Trim(strings.TrimSpace(entry), `"`)
	target = strings.Trim(strings.TrimSpace(target), `"`)
	if entry == "" || target == "" {
		return false
	}
	return strings.EqualFold(
		filepath.Clean(entry),
		filepath.Clean(target),
	)
}

func readWindowsUserPath() (windowsUserPathState, error) {
	subkey, err := syscall.UTF16PtrFromString(`Environment`)
	if err != nil {
		return windowsUserPathState{}, err
	}
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(
		syscall.HKEY_CURRENT_USER,
		subkey,
		0,
		syscall.KEY_QUERY_VALUE,
		&key,
	); err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return windowsUserPathState{}, nil
		}
		return windowsUserPathState{}, os.NewSyscallError("RegOpenKeyExW", err)
	}
	defer syscall.RegCloseKey(key)

	name, err := syscall.UTF16PtrFromString(`Path`)
	if err != nil {
		return windowsUserPathState{}, err
	}
	var valueType, size uint32
	if err := syscall.RegQueryValueEx(key, name, nil, &valueType, nil, &size); err != nil {
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return windowsUserPathState{}, nil
		}
		return windowsUserPathState{}, os.NewSyscallError("RegQueryValueExW", err)
	}
	if valueType != syscall.REG_SZ && valueType != syscall.REG_EXPAND_SZ {
		return windowsUserPathState{}, fmt.Errorf("用户 Path 注册表类型不受支持：%d", valueType)
	}
	if size == 0 {
		return windowsUserPathState{Exists: true, ValueType: valueType}, nil
	}

	data := make([]byte, size)
	dataSize := size
	if err := syscall.RegQueryValueEx(key, name, nil, &valueType, &data[0], &dataSize); err != nil {
		return windowsUserPathState{}, os.NewSyscallError("RegQueryValueExW", err)
	}
	units := unsafe.Slice((*uint16)(unsafe.Pointer(&data[0])), int(dataSize)/2)
	return windowsUserPathState{
		Exists:    true,
		Value:     syscall.UTF16ToString(units),
		ValueType: valueType,
	}, nil
}

func writeWindowsUserPath(state windowsUserPathState) error {
	subkey, err := syscall.UTF16PtrFromString(`Environment`)
	if err != nil {
		return err
	}
	var key syscall.Handle
	if err := syscall.RegOpenKeyEx(
		syscall.HKEY_CURRENT_USER,
		subkey,
		0,
		syscall.KEY_SET_VALUE,
		&key,
	); err != nil {
		return os.NewSyscallError("RegOpenKeyExW", err)
	}
	defer syscall.RegCloseKey(key)

	name, err := syscall.UTF16PtrFromString(`Path`)
	if err != nil {
		return err
	}
	if !state.Exists {
		result, _, _ := windowsRegDeleteValueW.Call(
			uintptr(key),
			uintptr(unsafe.Pointer(name)),
		)
		if result != 0 && syscall.Errno(result) != syscall.ERROR_FILE_NOT_FOUND {
			return os.NewSyscallError("RegDeleteValueW", syscall.Errno(result))
		}
		return nil
	}

	valueType := state.ValueType
	if valueType != syscall.REG_SZ && valueType != syscall.REG_EXPAND_SZ {
		valueType = syscall.REG_SZ
	}
	encoded, err := syscall.UTF16FromString(state.Value)
	if err != nil {
		return err
	}
	result, _, _ := windowsRegSetValueExW.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(name)),
		0,
		uintptr(valueType),
		uintptr(unsafe.Pointer(&encoded[0])),
		uintptr(len(encoded)*2),
	)
	if result != 0 {
		return os.NewSyscallError("RegSetValueExW", syscall.Errno(result))
	}
	return nil
}

func broadcastWindowsEnvironmentChange() error {
	environment, err := syscall.UTF16PtrFromString("Environment")
	if err != nil {
		return err
	}
	var messageResult uintptr
	result, _, callErr := windowsSendMessageTimeoutW.Call(
		windowsHWNDBroadcast,
		windowsWMSettingChange,
		0,
		uintptr(unsafe.Pointer(environment)),
		windowsSMTOAbortIfHung,
		1000,
		uintptr(unsafe.Pointer(&messageResult)),
	)
	if result == 0 {
		return windowsSyscallError("SendMessageTimeoutW", callErr)
	}
	return nil
}

func removeEmptyWindowsInstallDirs(installDir string) ([]string, error) {
	dirs := []string{filepath.Clean(installDir)}
	parent := filepath.Dir(installDir)
	if strings.EqualFold(filepath.Base(installDir), "bin") &&
		strings.EqualFold(filepath.Base(parent), "YoooClaw") {
		dirs = append(dirs, parent)
	}

	removed := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, fmt.Errorf("检查安装目录 %s 失败：%w", dir, err)
		}
		if len(entries) != 0 {
			continue
		}
		if err := os.Remove(dir); err != nil {
			return removed, fmt.Errorf("删除空安装目录 %s 失败：%w", dir, err)
		}
		removed = append(removed, dir)
	}
	return removed, nil
}

func windowsRemovalCandidates(exe string) []string {
	cleanedExe := filepath.Clean(exe)
	dir := filepath.Dir(cleanedExe)
	installed := []string{
		filepath.Join(dir, "yoooclaw.exe"),
		filepath.Join(dir, "yc.exe"),
	}

	result := make([]string, 0, len(installed))
	for _, candidate := range installed {
		if !strings.EqualFold(candidate, cleanedExe) {
			result = append(result, candidate)
		}
	}
	return append(result, cleanedExe)
}

func isWindowsNativeBinary(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == "yoooclaw.exe" || base == "yc.exe"
}

// removeWindowsPathNow 使用 FileDispositionInfoEx 的 POSIX 删除语义，将
// 正在运行的 exe 从文件系统命名空间中同步移除。映射到当前进程的文件数据
// 会保留到进程退出，但路径会立即消失，因此可以在返回 ok 前做真实验证。
func removeWindowsPathNow(path string) error {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handleValue, _, callErr := windowsCreateFileW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		windowsDeleteAccess,
		windowsFileShareRead|windowsFileShareWrite|windowsFileShareDelete,
		0,
		windowsOpenExisting,
		windowsFileAttributeNormal,
		0,
	)
	handle := syscall.Handle(handleValue)
	if handle == syscall.InvalidHandle {
		if verifyWindowsPathRemoved(path) == nil {
			return nil
		}
		return windowsSyscallError("CreateFileW", callErr)
	}

	disposition := windowsFileDispositionInfoEx{
		Flags: windowsDispositionDelete |
			windowsDispositionPOSIXSemantics |
			windowsDispositionIgnoreReadonly,
	}
	setResult, _, setErr := windowsSetFileInformationByHandle.Call(
		uintptr(handle),
		windowsFileDispositionInfoExClass,
		uintptr(unsafe.Pointer(&disposition)),
		unsafe.Sizeof(disposition),
	)
	closeErr := syscall.CloseHandle(handle)
	verifyErr := verifyWindowsPathRemoved(path)
	if verifyErr == nil {
		return nil
	}
	if setResult == 0 {
		return windowsSyscallError("SetFileInformationByHandle", setErr)
	}
	if closeErr != nil {
		return os.NewSyscallError("CloseHandle", closeErr)
	}
	return verifyErr
}

func verifyWindowsPathRemoved(path string) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("验证删除结果失败：%w", err)
	}
	return fmt.Errorf("删除调用返回后路径仍然存在")
}

func windowsSyscallError(name string, err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		err = syscall.EINVAL
	}
	return os.NewSyscallError(name, err)
}
