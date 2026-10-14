# Linux systemd 静默更新中断修复验证

## 原问题

无影 Ubuntu 24.04：0.13.0-alpha.426 于 2026-10-13 18:04 自动更新到 alpha.428，updateId 为 `7b662b9aded6d40513c9a96dedf0b644`。包已下载且 SHA-256 与灰度制品一致；停止旧 daemon 后更新进程中断，磁盘仍为 426，服务处于 inactive。

更新进程原来只有 setsid，仍在 daemon 的 systemd cgroup 内。服务默认 KillMode=control-group，停止服务同时杀掉更新进程。未发现文件替换已发生的证据。

## 修复

- Linux 从本 CLI 的 systemd 服务组内发起更新时，使用独立 transient user service；创建失败不退回原服务组运行。
- 非 Linux 及非该服务组的进程保留原 detached 路径。不修改 daemon 的 KillMode、自启或 owner。
- 无影重装遇到旧程序无法停服务时，仅允许确认属于当前安装的标准服务停止且运行锁空闲后继续。不删除凭证、数据或锁文件。

## 已验证

- macOS 执行 `go test ./internal/update ./internal/cli ./scripts` 通过，含旧程序不可执行的完整安装器模拟、占用运行锁拒绝重装、既有凭证保留。
- Windows 更新模块交叉编译通过；未做 Windows 实机测试。
- 在原云电脑编译目标为 Linux/x64 的测试程序，cgroup 判断和 transient service 参数测试通过。
- 2026-10-14 10:12:59（北京时间），隔离源服务 `yoooclaw-daemon-96dee91a7f.service` 启动测试中的真实 startWorker。独立服务 `yoooclaw-update-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.service` 成功停止源服务，并继续写出 `survived` 标记。systemd journal 确认先启动独立服务，再停止源服务。
- 此隔离验证没有使用用户凭证，没有启动或更新用户的实际 CLI daemon。

## 尚未验证

修复版首次安装之后，经真实灰度平台完成下一跳静默升级、Relay 恢复、Claude Skill 更新及 SLS 完整事件链，仍需修复版制品发布后验证。不能用隔离进程存活测试代替全链路结果。
