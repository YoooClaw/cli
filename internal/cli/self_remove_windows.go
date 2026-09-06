//go:build windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	windowsCreateNewProcessGroup      = 0x00000200
	windowsCreateNoWindow             = 0x08000000
	windowsCreateBreakaway            = 0x01000000
	windowsMoveFileDelayUntilReboot   = 0x00000004
)

var (
	windowsKernel32                   = syscall.NewLazyDLL("kernel32.dll")
	windowsCreateFileW                = windowsKernel32.NewProc("CreateFileW")
	windowsSetFileInformationByHandle = windowsKernel32.NewProc("SetFileInformationByHandle")
	windowsMoveFileExW                = windowsKernel32.NewProc("MoveFileExW")
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
	result.Warnings = append(result.Warnings, cleanupStaleWindowsUninstallFiles(exe)...)

	installDir := filepath.Dir(filepath.Clean(exe))
	pathBefore, pathRemoved, err := removeWindowsInstallDirFromUserPath(installDir)
	if err != nil {
		return result, fmt.Errorf("清理用户 PATH 中的安装目录失败：%w", err)
	}

	// 先删除没有在运行的命令副本，最后再移除当前进程对应的路径。若删除
	// 失败则恢复用户 PATH，让仍在磁盘上的当前命令保持可用。
	removedAliases := []string{}
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
			var deferred bool
			var warning string
			deferred, warning, removeErr = removeWindowsExecutablePath(candidate)
			if warning != "" {
				result.Warnings = append(result.Warnings, warning)
			}
			if deferred {
				result.Hint = "Windows 已将运行中的 CLI 移出安装目录；临时文件将在当前命令退出后清理"
			}
		} else {
			removeErr = os.Remove(candidate)
			if removeErr == nil {
				removeErr = verifyWindowsPathRemoved(candidate)
			}
		}
		if removeErr != nil {
			if rollbackErr := restoreWindowsAliases(exe, removedAliases); rollbackErr != nil {
				removeErr = fmt.Errorf("%w；同时恢复命令别名失败：%v", removeErr, rollbackErr)
			}
			return result, restoreWindowsPathAfterRemovalFailure(
				pathBefore, pathRemoved, fmt.Errorf("删除 %s 失败：%w", candidate, removeErr),
			)
		}
		result.Removed = append(result.Removed, candidate)
		if !strings.EqualFold(filepath.Clean(candidate), filepath.Clean(exe)) {
			removedAliases = append(removedAliases, candidate)
		}
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

// removeWindowsExecutablePath first attempts a true POSIX-style unlink. Some
// endpoint-security drivers deny FileDispositionInfoEx for a mapped image even
// though Windows still permits that image to be renamed. In that case move the
// executable out of the install directory, verify the public path disappeared,
// and let a detached helper delete the private temporary path after this process
// exits.
func removeWindowsExecutablePath(path string) (bool, string, error) {
	posixErr := removeWindowsPathNow(path)
	if posixErr == nil {
		return false, "", nil
	}
	pending, err := moveWindowsExecutableForDeferredRemoval(path)
	if err != nil {
		return false, "", fmt.Errorf("POSIX 删除失败：%v；移出安装目录重试失败：%w", posixErr, err)
	}
	rebootErr := scheduleWindowsDeleteOnReboot(pending)
	if helperErr := startWindowsRemovalHelper(pending); helperErr != nil {
		if rebootErr == nil {
			return true, "退出后清理助手无法启动；临时文件已安排在下次重启时删除", nil
		}
		restoreErr := os.Rename(pending, path)
		if restoreErr != nil {
			return false, "", fmt.Errorf("启动退出后清理任务失败：%v；重启清理安排失败：%v；恢复原路径失败：%w", helperErr, rebootErr, restoreErr)
		}
		return false, "", fmt.Errorf("启动退出后清理任务失败：%v；重启清理安排失败：%w", helperErr, rebootErr)
	}
	// The detached helper is the primary cleanup path. MOVEFILE_DELAY_UNTIL_REBOOT
	// commonly requires elevated registry access, so its failure is not a user-
	// visible warning when the helper was successfully launched.
	return true, "", nil
}

func legacyWindowsUninstallTempRoot() string {
	return filepath.Join(os.TempDir(), "yoooclaw-uninstall")
}

// windowsUninstallTempRoot returns a writable sibling of the installation
// root. os.Rename cannot move a mapped executable across Windows volumes, so
// the deferred path must be on the same volume as the executable. Keeping the
// directory outside YoooClaw also allows the public installation root to be
// removed synchronously.
func windowsUninstallTempRoot(path string) string {
	installDir := filepath.Dir(filepath.Clean(path))
	installRoot := installDir
	parent := filepath.Dir(installDir)
	if strings.EqualFold(filepath.Base(installDir), "bin") &&
		strings.EqualFold(filepath.Base(parent), "YoooClaw") {
		installRoot = parent
	}
	return filepath.Join(filepath.Dir(installRoot), "yoooclaw-uninstall")
}

func cleanupStaleWindowsUninstallFiles(path string) []string {
	roots := []string{
		legacyWindowsUninstallTempRoot(),
		windowsUninstallTempRoot(path),
	}
	seen := map[string]struct{}{}
	warnings := []string{}
	for _, root := range roots {
		key := strings.ToLower(filepath.Clean(root))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		warnings = append(warnings, cleanupStaleWindowsUninstallFilesIn(root)...)
	}
	return warnings
}

func cleanupStaleWindowsUninstallFilesIn(root string) []string {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []string{"无法检查历史卸载临时文件：" + err.Error()}
	}
	warnings := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "yoooclaw-") || !strings.HasSuffix(entry.Name(), ".exe.pending") {
			continue
		}
		if err := os.Remove(filepath.Join(root, entry.Name())); err != nil {
			warnings = append(warnings, "无法清理历史卸载临时文件 "+entry.Name()+"："+err.Error())
		}
	}
	if remaining, err := os.ReadDir(root); err == nil && len(remaining) == 0 {
		_ = os.Remove(root)
	}
	return warnings
}

func moveWindowsExecutableForDeferredRemoval(path string) (string, error) {
	root := windowsUninstallTempRoot(path)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	placeholder, err := os.CreateTemp(root, "yoooclaw-*.exe.pending")
	if err != nil {
		return "", err
	}
	pending := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(pending); err != nil {
		return "", err
	}
	if err := os.Rename(path, pending); err != nil {
		return "", err
	}
	if err := verifyWindowsPathRemoved(path); err != nil {
		_ = os.Rename(pending, path)
		return "", err
	}
	return pending, nil
}

func scheduleWindowsDeleteOnReboot(path string) error {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	result, _, callErr := windowsMoveFileExW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		0,
		windowsMoveFileDelayUntilReboot,
	)
	if result == 0 {
		return windowsSyscallError("MoveFileExW", callErr)
	}
	return nil
}

func restoreWindowsAliases(exe string, aliases []string) error {
	if len(aliases) == 0 {
		return nil
	}
	info, err := os.Stat(exe)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	for _, alias := range aliases {
		if err := os.WriteFile(alias, data, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func windowsDeferredRemovalCommand() string {
	return `ping.exe -n 2 127.0.0.1 >nul & for /L %i in (1,1,30) do @(del /F /Q "%YOOOCLAW_UNINSTALL_PENDING_PATH%" >nul 2>&1 & rd /Q "%YOOOCLAW_UNINSTALL_PENDING_ROOT%" >nul 2>&1 & if exist "%YOOOCLAW_UNINSTALL_PENDING_PATH%" ping.exe -n 2 127.0.0.1 >nul)`
}

func newWindowsRemovalHelperCommand(path string, breakaway bool) *exec.Cmd {
	flags := uint32(windowsCreateNewProcessGroup | windowsCreateNoWindow)
	if breakaway {
		flags |= windowsCreateBreakaway
	}
	cmd := exec.Command("cmd.exe")
	cmd.Env = append(os.Environ(),
		"YOOOCLAW_UNINSTALL_PENDING_PATH="+path,
		"YOOOCLAW_UNINSTALL_PENDING_ROOT="+filepath.Dir(path),
	)
	// cmd.exe does not use the CommandLineToArgvW parsing convention assumed by
	// os/exec. Pass its command line verbatim so the quoted environment-variable
	// paths stay part of the command string instead of becoming separate argv
	// tokens. The executable name is argv[0] in the CreateProcess command line.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: flags,
		CmdLine:       `cmd.exe /d /s /c "` + windowsDeferredRemovalCommand() + `"`,
	}
	return cmd
}

func startWindowsRemovalHelperMode(path string, breakaway bool) error {
	cmd := newWindowsRemovalHelperCommand(path, breakaway)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func startWindowsRemovalHelperWith(path string, start func(string, bool) error) error {
	breakawayErr := start(path, true)
	if breakawayErr == nil {
		return nil
	}
	if fallbackErr := start(path, false); fallbackErr != nil {
		return fmt.Errorf("breakaway 模式失败：%v；普通脱离模式失败：%w", breakawayErr, fallbackErr)
	}
	return nil
}

// startWindowsRemovalHelper prefers a process outside the caller's Job Object
// so managed shells cannot reap it with the CLI. Some hosts, including GitHub
// Actions, prohibit CREATE_BREAKAWAY_FROM_JOB; retry without that flag there.
func startWindowsRemovalHelper(path string) error {
	return startWindowsRemovalHelperWith(path, startWindowsRemovalHelperMode)
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
