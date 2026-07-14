package yclib

import (
	"context"

	"github.com/YoooClaw/cli/internal/daemon"
)

// DaemonClient 暴露 daemon 生命周期管理。经 Client.Daemon() 获取。
//
// arc §5 硬约束：library **绝不隐式 fork daemon**。Start/Stop 必须由调用方显式触发；
// 其它 daemon 依赖能力在 daemon 未运行时只报 CodeDaemonNotRunning，不自动拉起。
type DaemonClient struct {
	c *Client
}

// Daemon 返回 daemon 生命周期子 client。
func (c *Client) Daemon() *DaemonClient { return &DaemonClient{c: c} }

// DaemonStatus 是 daemon 的运行态快照。
type DaemonStatus struct {
	Running bool   `json:"running"`
	Stale   bool   `json:"stale"` // 锁存在但进程已死
	PID     int    `json:"pid,omitempty"`
	Bind    string `json:"bind,omitempty"`
	Port    int    `json:"port,omitempty"`
	Version string `json:"version,omitempty"`
	Profile string `json:"profile,omitempty"`
}

// Status 读取 daemon 运行态（基于 lock 文件 + 进程探活），不发起网络请求。
func (d *DaemonClient) Status(ctx context.Context) (DaemonStatus, error) {
	if err := ctx.Err(); err != nil {
		return DaemonStatus{}, err
	}
	return statusFrom(daemon.State(d.c.paths)), nil
}

// DaemonStartOpts 控制显式启动 daemon 的绑定参数（均可选）。
type DaemonStartOpts struct {
	Bind     string
	Port     int
	LogLevel string
}

// Start 显式 fork 并等待 daemon 起来（最多 ~3s）。已在运行则直接返回当前状态（幂等）。
// 这是 library 中**唯一**会创建 daemon 进程的入口，且必须由调用方主动调用。
func (d *DaemonClient) Start(ctx context.Context, opts DaemonStartOpts) (DaemonStatus, error) {
	if err := ctx.Err(); err != nil {
		return DaemonStatus{}, err
	}
	if st := daemon.State(d.c.paths); st.Running {
		return statusFrom(st), nil
	}
	startOpts := daemon.StartOpts{
		Bind: opts.Bind, Port: opts.Port, LogLevel: opts.LogLevel,
	}
	if err := daemon.PrecheckStartFor(d.c.paths, startOpts); err != nil {
		return DaemonStatus{}, err
	}
	lock, err := daemon.SpawnFor(d.c.paths, d.c.rootDir, d.c.profile, startOpts)
	if err != nil {
		return DaemonStatus{}, err
	}
	d.c.logf("yclib daemon started pid=%d port=%d", lock.PID, lock.Port)
	return statusFrom(daemon.State(d.c.paths)), nil
}

// Stop 停止 daemon（未运行则为 no-op，返回 nil）。
func (d *DaemonClient) Stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if st := daemon.State(d.c.paths); !st.Running {
		return nil
	}
	_, err := daemon.Stop(d.c.paths)
	return err
}

func statusFrom(st daemon.RunningState) DaemonStatus {
	out := DaemonStatus{Running: st.Running, Stale: st.Stale}
	if st.Lock != nil {
		out.PID = st.Lock.PID
		out.Bind = st.Lock.Bind
		out.Port = st.Lock.Port
		out.Version = st.Lock.Version
		out.Profile = st.Lock.Profile
	}
	return out
}
