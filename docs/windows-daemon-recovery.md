# Windows 更新后恢复 daemon

基于 release/0.15.0。任务被禁用、启动失败或就绪超时时，daemon start 记录原始错误并尝试独立后台进程恢复。不改写计划任务或自启偏好；Linux/macOS 保持原流程。恢复前重新检查所有权，子进程继续使用现有互斥锁。

本地 daemon-supervisor.log 记录 service.start_failed（任务名、管理器、脱敏系统错误及 HRESULT）、service.detached_recovery（PID）或 service.fallback_failed。

匹配待生效更新的版本、路径、profile、环境和凭证摘要后，以原 updateId 上报 restart failed / AUTOSTART_START_FAILED。它不终结更新；后续仍由已有运行版本及 Relay 连接确认生效。旧更新器调用新二进制时也可触发，错误在写队列前脱敏。

同一 updateId 可以先报告自启失败，再报告 activate succeeded；监控需区分自启异常与最终更新失败。

本地验证：CLI/update 单元测试、go vet、Windows amd64 交叉编译。真实 Windows 禁用任务及 Relay 恢复仍需 Windows 环境验证。
