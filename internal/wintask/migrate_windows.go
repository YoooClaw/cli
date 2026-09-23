//go:build windows

package wintask

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/YoooClaw/cli/internal/diagnostics"
	"github.com/YoooClaw/cli/internal/taskrepair"
	"golang.org/x/sys/windows"
)

// Migrate retries registration natively for a strictly validated legacy task.
// The elevated child may only repair its DACL; it never registers or runs a task.
func Migrate(root, executable, replacement string) (resultErr error) {
	defer func() {
		diagnostics.Write(root, "info", "autostart.legacy_migration_result", map[string]any{"ok": resultErr == nil, "error": diagnostics.SafeError(resultErr)})
	}()
	if windows.GetCurrentProcessToken().IsElevated() {
		return fmt.Errorf("请以原用户普通权限执行迁移")
	}
	sid, err := CurrentSID()
	if err != nil {
		return err
	}
	profile, err := ProfileFor(sid)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(root), filepath.Join(profile, ".yoooclaw")) {
		return fmt.Errorf("自定义数据目录不自动修复权限")
	}
	expected := filepath.Join(profile, "AppData", "Local", "YoooClaw", "bin", "yoooclaw.exe")
	if !strings.EqualFold(filepath.Clean(executable), expected) {
		return fmt.Errorf("非标准原生 CLI 路径，不自动提权")
	}
	s, err := Open()
	if err != nil {
		return err
	}
	defer s.Close()
	raw, err := s.XML()
	if err != nil {
		return err
	}
	if err = taskrepair.Validate(raw, sid, profile, ResolveSID); err != nil {
		return err
	}
	sd, err := s.SD()
	if err != nil {
		return err
	}
	backup, err := os.MkdirTemp(root, "task-migration-backup-")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(backup, "task.xml"), []byte(strings.Replace(raw, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)), 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(backup, "task.sddl"), []byte(sd), 0600); err != nil {
		return err
	}
	diagnostics.Write(root, "info", "autostart.legacy_task_backup", map[string]any{"directory": backup})
	register := func() error {
		return call(s.folder, "RegisterTask", "yoooclaw-daemon", replacement, 4|16, sid, nil, 3, nil)
	}
	err = register()
	if !taskrepair.AccessDenied(err) {
		return err
	}
	// This is an authorization request, not a bypass of a host program policy.
	if message("旧 YoooClaw 任务拒绝更新。是否请求管理员授权，仅修复该任务的访问权限？不会修改文件夹权限、密钥或用户数据。\n备份："+backup, 0x24) != 6 {
		return fmt.Errorf("用户取消权限修复；备份：%s", backup)
	}
	diagnostics.Write(root, "info", "autostart.task_permission_authorization_requested", nil)
	if err = Elevate(executable, "daemon", "autostart", "repair-task-permissions", sid); err != nil {
		return err
	}
	diagnostics.Write(root, "info", "autostart.task_permission_repair_completed", map[string]any{"folderPermissionsChanged": false})
	if err = register(); err != nil {
		return fmt.Errorf("权限修复后更新仍失败，不扩大权限：%w", err)
	}
	return nil
}
