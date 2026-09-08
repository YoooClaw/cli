# 测试体系

本仓库的单元测试**只用 Go 标准库 `testing`**（零第三方断言/mock 依赖），统一采用
表驱动 + `t.Parallel()` + 沙箱隔离 + golden 锁契约的风格。

## 快速命令

```bash
make test          # go test -race ./...
make cover         # 生成 coverage.out + 打印总覆盖率
make cover-html    # 生成 coverage.html 报告
make cover-gate    # 覆盖率门禁（低于 MIN_COVERAGE 失败）
make test-int      # 跑带 integration tag 的破坏性用例（真实钥匙串等）
make update-golden # 刷新 golden 期望文件
make ci            # 本地复刻 CI：vet + race + 覆盖率门禁
```

CI（`.github/workflows/ci.yml`）跑 `go test -race -coverprofile` + 覆盖率汇总 + 门禁。

## Windows 原生自启动回归

`internal/autostart/task_scheduler_test.go` 在各平台验证 COM 操作流程：只把明确的
文件/路径不存在当成缺失；保留访问拒绝、策略阻止和 RPC 错误；核对注册参数、
启动/停止/删除以及对象释放。Windows 专属测试还通过真实 COM 查询验证 HRESULT。

真实注册/运行测试需要显式启用，CI 的临时 Windows runner 已启用：

```bat
go build -trimpath -ldflags "-s -w -H=windowsgui" -o internal/winhost/host.bin ./cmd/yoooclaw-host
set YOOOCLAW_TASK_SCHEDULER_INTEGRATION=1
go test -tags nativehost ./internal/autostart -run "^TestWindowsTaskSchedulerNative" -count=1 -v
set YOOOCLAW_TASK_SCHEDULER_INTEGRATION=
```

该测试创建唯一命名的当前用户任务及临时文件，完成创建、更新、隐藏启动、停止、
删除和不存在回读；清理仅针对测试任务，不接触用户已有的 YoooClaw 任务或数据。
测试内部清空 PATH，验证管理过程不再寻找 PowerShell/schtasks，不改系统安全策略。
原生 GUI host 还验证任务实例与 daemon PID 的关联，以及停止任务时子进程确实退出。
清空 PATH 不等于企业组策略实测；发布前仍应在目标受限环境验收。
macOS/Linux 上交叉编译 Windows 测试，只能确认可编译，不能代替 Windows 实机通过。

`internal/installer` 的跨平台测试覆盖覆盖保护、停止/验证/PATH 写入失败的回滚、
无关文件保留、下载失败、清单缺失/重复和 SHA-256 不匹配拒绝。
Windows CI 另以 `YOOOCLAW_NATIVE_INSTALLER_TEST_BINARY` 指向内置 CLI/host 的原生 setup，
在空 PATH、隔离数据目录和带中文/空格/特殊字符的安装路径中完成原生安装、覆盖、
配置/数据保留及完整卸载，并恢复 runner 的原用户 PATH。开发环境不带 `nativehost`
标签时，需把同架构 `yoooclaw-host.exe` 放在测试/CLI 二进制旁；官方构建脚本自动嵌入。

发布验收还须覆盖：标准用户真实登录启动、不出现黑框、退出 Agent 后持续运行、
旧 0.10.x 升级、显式关闭自启不被升级覆盖，以及禁用脚本宿主但允许原生 CLI/计划任务的企业策略环境。
当前没有 Windows Authenticode 签名流水线；SHA-256 校验不等于发布者签名或企业应用白名单批准。

## 公共测试设施：`internal/testutil`

仅供 `*_test.go` 使用（依赖 `testing`，不要被生产代码 import）。

- `testutil.Sandbox(t)` —— 把 `YOOOCLAW_HOME` 指向临时目录并预建 default profile，返回 `paths.Paths`。
  每个测试自带隔离环境，绝不触碰真实 `~/.yoooclaw`。
- `testutil.Logger{T: t}` —— 把日志转发到 `t.Log` 的假 logger，满足各包 `Info/Warn/Error(string)` 接口。
- `testutil.Golden(t, name, got)` + 全局 `-update` —— golden 比对/刷新。
- `testutil.WriteFile(t, path, data)` —— 摆放 fixture。

## 隔离与确定性约定

1. **文件系统**：一律 `t.TempDir()` / `YOOOCLAW_HOME` 沙箱，不写真实数据目录。
2. **网络**：用 `net/http/httptest` 模拟上游 / Relay / OSS / daemon，不连真实网络。
3. **系统钥匙串**：`internal/creds` 通过 `keychain_seam.go` 的函数变量
   （`keychainAvailableFn/GetFn/SetFn`）注入假实现，测试**绝不**读写真实钥匙串；
   真实钥匙串往返放在 `//go:build keychain_integration` 下，默认不跑。
4. **并行**：纯函数测试默认 `t.Parallel()`；改写包级变量（creds seam、cli 的
   `os.Stdout`/`exitCode`）的测试**不可并行**。

## 各层策略

| 层 | 包 | 做法 |
|---|---|---|
| 纯函数 | errs, output, config/dotpath, light/validators, paths, fsutil, logread, version, prompt | 表驱动穷尽 + golden |
| 状态/存储 | config, creds, lightrule, monitor, notif, skills, image | `Sandbox` + 临时目录往返读写 |
| 集成 | recording, relay, light/sender, daemon/server | `httptest` 模拟上游；构造 `server{}` 直接打 `ServeHTTP` |
| 命令编排 | cli | 构造 `newRootCmd()`、`SetArgs`、捕获 `os.Stdout`、断言 JSON 输出契约与退出码 |

## 结构性无法覆盖的部分（非缺陷）

以下代码路径依赖进程外部状态，单平台、进程内无法确定性触达，已知并接受：

- **`internal/keychain`**（~29%）：`exec.Command` 调 `security`/`secret-tool` 的写入分支与
  非当前平台分支。真实往返见 `-tags=keychain_integration`，但 Linux/Windows 分支在 macOS 上仍不可达。
- **`internal/prompt`**（~82%）：TTY 检测（`isTTY` 的 `os.Stat` 错误分支、交互式真实读写）需真实终端。
- **`internal/version`**（~86%）：`Dist()` 中 `os.Executable()` 出错分支不可强制触发（纯逻辑已抽到 `distFor` 全覆盖）。
- **`internal/daemon`**（~21%）：进程 spawn/detach（`proc_unix.go`/`proc_windows.go`/`spawn.go`）、
  信号处理与主循环 `RunForeground` 的端口监听/优雅退出，属于进程级集成，留待端到端测试；
  HTTP handler 已用 `httptest` 覆盖核心路由。
- **`cmd/yc/main.go`**：仅 `cli.Execute()` 入口（含 `os.Exit`），无逻辑。
- 各包深层 I/O 错误分支（`os.Rename`/`Chmod` 失败等）不易在测试中可靠制造。

## 覆盖率门禁

`scripts/coverage-gate.sh` 校验总覆盖率不低于 `MIN_COVERAGE`（默认 55）。
**只许涨不许跌**：补完一波测试后把阈值抬到略低于当前实际值，逐步收紧。
