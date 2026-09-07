package cli

import (
	"os"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/daemon"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
	"github.com/YoooClaw/cli/internal/prompt"
	"github.com/YoooClaw/cli/internal/version"
	"github.com/spf13/cobra"
)

func newUninstallCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "uninstall",
		Short: "卸载 yoooclaw：停 daemon、删二进制与配置（默认保留数据）🔴",
		Args:  cobra.NoArgs,
		RunE:  run(uninstall),
	}
	c.Flags().Bool("data", false, "连同通知/录音/图片等数据一起删除（清空 "+paths.RootDir()+"）")
	c.Flags().Bool("yes", false, "跳过确认")
	return c
}

func uninstall(_ *clictx.Context, cmd *cobra.Command, _ []string) (any, error) {
	withData := flagBool(cmd, "data")
	root := paths.RootDir()

	if !flagBool(cmd, "yes") {
		q := "确认卸载 yoooclaw？将停止 daemon 并删除二进制与配置（保留数据）"
		if withData {
			q = "确认卸载 yoooclaw？将停止 daemon，并删除二进制、配置与全部数据（" + root + "）"
		}
		ok, err := prompt.Confirm(q, false)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errs.New(errs.CodeConfirmationRequired, "已取消")
		}
	}

	// 1. 先卸载系统用户服务，避免删除二进制后留下失效的自启项；再清理
	// 可能由旧版本或 --no-autostart 启动的 detached daemon。
	autostartRemoved, err := removeAutostartForUninstall()
	if err != nil {
		return nil, err
	}
	stopped := stopAllDaemons()

	// 2. 先删二进制。配置与凭据必须等到安装路径确认移除后再清理，
	// 避免 Windows 安全软件拒绝自删时留下一个无法使用的半卸载状态。
	// npm 安装无法自删，给提示；Windows 使用同步可验证的
	// POSIX 删除语义移除正在运行的原生二进制路径）。
	binaryRemoval, err := removeSelfBinary()
	if err != nil {
		return nil, errs.New(errs.CodeStorageUnavailable, "删除 CLI 二进制失败："+err.Error())
	}

	// 3. 二进制路径已经移除，再删配置（默认保留数据）或整目录（--data）。
	var removed []string
	if withData {
		if err := os.RemoveAll(root); err != nil {
			return nil, errs.New(errs.CodeStorageUnavailable, "删除数据目录失败："+err.Error())
		}
		if root != "" {
			removed = append(removed, root)
		}
	} else {
		removed = removeConfigKeepData()
	}
	removed = append(autostartRemoved, removed...)

	result := map[string]any{
		"ok":             true,
		"daemonsStopped": stopped,
		"removed":        removed,
		"binaryRemoved":  binaryRemoval.Removed,
		"dataKept":       !withData,
	}
	if binaryRemoval.UserPathRemoved {
		result["userPathRemoved"] = true
	}
	if len(binaryRemoval.InstallDirsRemoved) > 0 {
		result["installDirsRemoved"] = binaryRemoval.InstallDirsRemoved
	}
	if len(binaryRemoval.Warnings) > 0 {
		result["warnings"] = binaryRemoval.Warnings
	}
	if binaryRemoval.Hint != "" {
		result["hint"] = binaryRemoval.Hint
	}
	return result, nil
}

// stopAllDaemons 停掉所有 profile 在跑的 daemon，返回被停的 PID 列表。
func stopAllDaemons() []int {
	names := paths.ListProfileNames()
	if len(names) == 0 {
		names = []string{paths.DefaultProfile}
	}
	stopped := []int{}
	for _, name := range names {
		p := paths.For(name)
		lock := daemon.ReadLock(p)
		if lock == nil || !daemon.IsProcessAlive(lock.PID) {
			daemon.RemoveLock(p)
			continue
		}
		if _, err := daemon.Stop(p); err == nil {
			stopped = append(stopped, lock.PID)
		}
	}
	return stopped
}

// removeConfigKeepData 删除账号级与各 profile 的配置文件，保留数据目录
// （notifications/recordings/images/light-rules/state/logs）。
func removeConfigKeepData() []string {
	removed := []string{}
	tryRemove := func(path string) {
		if _, err := os.Lstat(path); err != nil {
			return
		}
		if os.Remove(path) == nil {
			removed = append(removed, path)
		}
	}
	tryRemove(paths.SharedCredentialsPath())
	tryRemove(paths.ActiveProfilePath())
	for _, name := range paths.ListProfileNames() {
		p := paths.For(name)
		tryRemove(p.Config)
		tryRemove(p.Credentials)
		tryRemove(p.DaemonLock)
	}
	return removed
}

type binaryRemovalResult struct {
	Removed            []string
	InstallDirsRemoved []string
	UserPathRemoved    bool
	Warnings           []string
	Hint               string
}

// removeSelfBinary 删除当前可执行文件及同目录下的命令别名。
// npm 安装由 node_modules 托管，不自删；原生安装只有在二进制路径已被
// 同步移除后才返回成功。
func removeSelfBinary() (binaryRemovalResult, error) {
	if version.Dist() == "npm" {
		return binaryRemovalResult{
			Removed: []string{},
			Hint:    "npm 安装：请运行 `npm uninstall -g @yoooclaw/cli` 移除二进制",
		}, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return binaryRemovalResult{
			Removed: []string{},
			Hint:    "无法定位可执行文件，请手动删除二进制",
		}, nil
	}
	return removeNativeSelfBinary(exe)
}
