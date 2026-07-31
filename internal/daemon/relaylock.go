package daemon

import (
	"errors"
	"path/filepath"

	"github.com/YoooClaw/cli/internal/config"
	"github.com/YoooClaw/cli/internal/errs"
	"github.com/YoooClaw/cli/internal/paths"
)

// relayConsumerSingletonName 是 account 级 standalone Relay 消费者锁。
// API key 存在 ~/.yoooclaw/credentials.json，天然跨 profile 共享；对应的消费者锁也
// 必须位于 profile 目录之外，否则 test/default 两个 daemon 会同时消费同一账号消息。
const relayConsumerSingletonName = "standalone-relay.flock"

func needsRelayConsumerLock(mode string, cfg config.Config) bool {
	return mode == config.IngressStandalone && cfg.Relay.Enabled
}

func relayConsumerLockPath(p paths.Paths) string {
	// 标准布局是 <root>/profiles/<profile>。Paths 也允许 yclib 用 ForRoot 显式
	// 构造，因此不能回读全局 YOOOCLAW_HOME。
	return filepath.Join(filepath.Dir(filepath.Dir(p.Dir)), relayConsumerSingletonName)
}

func relayConsumerConflict(p paths.Paths) error {
	return errs.New(errs.CodeDaemonAlreadyRunning,
		"另一个 profile 的 standalone daemon 正在消费账号 Relay，拒绝重复启动",
		map[string]any{"scope": "account-relay", "profile": p.Profile}).
		WithHint("先停止旧 profile 的 daemon，或运行 `yoooclaw profile use <目标 profile>` 完成切换")
}

// checkRelayConsumerLock 在父进程 spawn 前快速探测，避免子进程因全局锁冲突后让
// 调用方等待 daemon ready 超时。真正的竞态互斥仍由 RunForeground 获取锁保证。
func checkRelayConsumerLock(p paths.Paths) error {
	held, err := probeFileLock(relayConsumerLockPath(p))
	if err != nil {
		return errs.New(errs.CodeStorageUnavailable, "无法检查账号级 Relay 消费者锁："+err.Error())
	}
	if held {
		return relayConsumerConflict(p)
	}
	return nil
}

// acquireRelayConsumerLock 获取并持有账号级 Relay 消费者锁直到 daemon 退出。
func acquireRelayConsumerLock(p paths.Paths) (func(), error) {
	release, err := acquireProcessLock(relayConsumerLockPath(p))
	if errors.Is(err, errProcessLockHeld) {
		return nil, relayConsumerConflict(p)
	}
	if err != nil {
		return nil, errs.New(errs.CodeStorageUnavailable, "无法获取账号级 Relay 消费者锁："+err.Error())
	}
	return release, nil
}
