# Windows 原生定向修复工具（待 Windows 实机验收）

v2 (20260923)：Connect 改用原生 ITaskService 的 VT_EMPTY 参数；每个 COM
读取阶段记录 BEGIN/OK 或完整 HRESULT/嵌套 SCODE。未定位的异常不再吞成
泛化的“发生意外”。旧任务不存在、已确认在线均按 NO_CHANGE 返回，不尝试修复。
旧版的失败现场不足以确定具体 COM 调用点，需 Windows 回测确认。

用途：旧 WorkBuddy npm 的 yc.exe 已失效、现有任务无法被同一用户更新，
且 CLI 已安装在 `%USERPROFILE%\AppData\Local\YoooClaw\bin\yoooclaw.exe`。
仅支持默认数据根、default profile、standalone + Relay。

## 用户操作

1. 普通双击 `YoooClaw-Repair.exe`，不要右键管理员运行。
2. 确认修复范围；需要时批准一次 Windows UAC 提示。
3. 等待约 1–2 分钟，以最终中文结果弹窗为准。
4. 失败时发送“文档”目录下 `YoooClaw-Repair-*` 文件夹。

不修改执行策略，不调用 PowerShell、schtasks 或 VBS，不删除数据或改密钥。
如果安全软件、SmartScreen 或组织策略阻止 EXE，请交管理员核验来源/批准，
不要关闭防护或绕过策略。此构建未签名，不保证所有环境都允许执行。

## 实现与范围

- 使用原生 Task Scheduler COM；校验固定旧任务的 SID、普通权限、动作、参数和根目录。
- 备份任务 XML、安全描述符以及 autostart.json；备份不得失败后继续。
- 普通权限尝试更新；只有明确 Access Denied 才启动自身的 UAC 子进程。
- 提权子进程重新校验身份和任务，只增加指定用户在**单个任务**上的管理权限，
  保留现有拒绝项、SYSTEM/管理员权限和继承控制，不修改任务目录 ACL。
- 回到普通进程更新同名任务。目录依然拒绝更新时停止，不自动扩大授权。
- 在 CLI bin 中保留 `yoooclaw-repair-host.exe`，以 GUI 子系统启动无控制台的
  `yoooclaw.exe daemon run-service` 并等待退出。它以普通用户运行。
- 原生查询任务状态，并通过本机 daemon RPC 校验 Relay，避免旧 CLI 的
  autostart/status 再调用 PowerShell。读入必要鉴权信息但不输出凭据或完整响应。
- 60 秒后再次验证同一 PID、任务运行与 Relay 在线才报告成功；不声称注销登录已测试。
- 不修改正常 CLI 安装/升级行为。旧 CLI 后续 enable/升级仍可能重新创建 VBS 启动入口；
  这不是完整的安装迁移修复，正式产品集成另行处理。

## 回滚

需管理员根据备份使用 Task Scheduler 的 RegisterTask/SetSecurityDescriptor API
恢复任务定义与权限；不要直接修改 System32/Tasks 文件。autostart-before.json
可在停止该任务后恢复。旧 XML 指向失效的 CLI 路径，恢复它不会恢复连接。
原生启动器必须在任务不再引用且退出后才能移除；如有旧启动器，备份在报告目录。
没有自动回滚，失败报告会保留已完成步骤，避免错误声称系统未变更。

## 构建与验证

```sh
go test ./internal/taskrepair
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -H=windowsgui" -o YoooClaw-Repair.exe ./cmd/yoooclaw-repair
```

macOS 上的规则单测和交叉编译不能代替 Windows 实机验收。发布前必须测：
旧 npm 路径失效、只读任务 ACL、UAC 取消、其他管理员授权、显式 deny、文件夹拒绝、
不同用户任务拒绝、中文/空格路径、无网络、任务结束后存活、注销登录、失败备份回滚。
