# CLI 0.14.1 发布验证

2026-10-27，release/0.14.1 基于 origin/master e258dd5。

## 发布范围

- 恢复 0.14.0 发布时遗漏的 master 修复，包括独立 systemd 更新进程、daemon 重启、录音空闲判断、Relay 诊断及日志关闭。
- 保留正式版与 beta 同一制品登记测试区和生产区 OTA 草稿的流程，不启用灰度投放。
- 不包含 release/0.15.0 的迁移实现，也不包含工作区尚未提交的迁移日志与手动更新冷却调整。

## 验证

- go test ./...、go vet ./...、OTA 草稿登记的 5 项 Node 测试通过。
- Linux amd64 更新模块测试程序在无影通过 TestDaemonServiceCgroup 和 TestSystemdWorkerCommand。
- 独立临时目录 /tmp/yc-0141-probe，源服务 yoooclaw-daemon-6c907923fb.service，运行真实 startWorker；更新子服务停止源服务后仍写出 survived 标记。源服务 Result=success、inactive/dead；原 yoooclaw-daemon.service 保持 active。
- 此测试验证 systemd 服务组隔离，不等同于真实灰度升级、Relay 重连及 Skill 更新的全链路验收。

## 升级注意

0.14.0-alpha.471 和 cli-v0.14.0 均缺少 30dd26b。更新由旧版本启动，因此这些版本首次升级修复版应使用终端手动安装，不依赖旧版后台更新器。
