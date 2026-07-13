package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
)

// writerLockFileName 是 hermes 插件的 profile 写者锁文件名。
// 插件模式下插件直写通知存储并全程持有该锁（OS 建议锁，进程退出自动释放）；
// standalone daemon 与插件互斥，启动前必须探测。
const writerLockFileName = "writer.lock"

// PrecheckStart 在 spawn / 前台运行前做启动前校验：standalone（非 proxied）
// 模式与 hermes 插件的写者锁互斥。proxied 模式豁免——那是插件自己托管的
// daemon，不写通知、不拨隧道。
func PrecheckStart(ctx *clictx.Context, opts StartOpts) error {
	cfg, err := config.Load(ctx.Paths)
	if err != nil {
		return err
	}
	if resolveIngressMode(opts, cfg) == config.IngressProxied {
		return nil
	}
	return checkWriterLock(ctx.Paths)
}

// checkWriterLock 探测写者锁；被其他进程持有时返回结构化错误。
// 探测本身出错（权限等）按未持有处理——诊断逻辑不能把 daemon 卡死。
func checkWriterLock(p paths.Paths) error {
	path := filepath.Join(p.Dir, writerLockFileName)
	held, err := probeFileLock(path)
	if err != nil || !held {
		return nil
	}
	return errs.New(errs.CodeDaemonDisabledByPlugin,
		"通知存储由 "+writerLockOwnerHint(path)+" 独占，standalone daemon 已禁用").
		WithHint("停用 hermes 插件后重试；插件运行期间的通知采集由插件直写完成")
}

// writerLockOwnerHint 读取锁文件元数据拼诊断信息；内容仅供展示，
// 互斥语义完全由 OS 锁保证。
func writerLockOwnerHint(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "hermes 插件"
	}
	var meta struct {
		Owner string `json:"owner"`
		PID   int    `json:"pid"`
	}
	if json.Unmarshal(raw, &meta) != nil || meta.Owner == "" {
		return "hermes 插件"
	}
	hint := meta.Owner
	if meta.PID > 0 {
		hint += " (pid " + strconv.Itoa(meta.PID) + ")"
	}
	return hint
}
