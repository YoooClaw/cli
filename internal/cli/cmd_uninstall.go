package cli

import (
	"os"
	"path/filepath"

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

	// 2. 删配置（默认保留数据）或整目录（--data）。
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

	// 3. 删二进制（npm 安装无法自删，给提示）。
	binRemoved, hint := removeSelfBinary()

	result := map[string]any{
		"ok":             true,
		"daemonsStopped": stopped,
		"removed":        removed,
		"binaryRemoved":  binRemoved,
		"dataKept":       !withData,
	}
	if hint != "" {
		result["hint"] = hint
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

// removeSelfBinary 删除当前可执行文件及同目录下的 yc / yoooclaw 软链。
// npm 安装由 node_modules 托管，不自删，返回卸载提示。
func removeSelfBinary() (removed []string, hint string) {
	removed = []string{}
	if version.Dist() == "npm" {
		return removed, "npm 安装：请运行 `npm uninstall -g @yoooclaw/cli` 移除二进制"
	}
	exe, err := os.Executable()
	if err != nil {
		return removed, "无法定位可执行文件，请手动删除二进制"
	}
	real := exe
	if r, e := filepath.EvalSymlinks(exe); e == nil {
		real = r
	}
	dir := filepath.Dir(real)
	candidates := []string{real, exe, filepath.Join(dir, "yc"), filepath.Join(dir, "yoooclaw")}
	seen := map[string]bool{}
	for _, c := range candidates {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		// 只删我们认得的文件名（native 安装为 yoooclaw + yc 软链），
		// 避免可执行文件被改名/嵌套时误删无关文件。
		if base := filepath.Base(c); base != "yc" && base != "yoooclaw" {
			continue
		}
		if _, e := os.Lstat(c); e != nil {
			continue
		}
		if os.Remove(c) == nil {
			removed = append(removed, c)
		}
	}
	return removed, ""
}
