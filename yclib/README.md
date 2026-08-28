# yclib — yc-cli 进程内 Go Library

`yclib` 是 `@yoooclaw/cli` 的**进程内公开 API 门面**，让 Go Agent 进程直接 `import` 调用 CLI 的业务能力，无需为每次调用 fork 子进程、无 IPC、类型安全。唯一例外是调用方显式触发的 `Daemon().Start`，它会创建长期运行的 daemon 子进程。

> 设计约束见 software 仓库 `architecture/arc-cli-library-integration.md`（路径 C）。

## 安装

```bash
go get github.com/YoooClaw/cli/yclib
```

## 快速开始

```go
import "github.com/YoooClaw/cli/yclib"

c, err := yclib.New(yclib.Config{Profile: "default"})
if err != nil { /* ... */ }

ctx := context.Background()
res, err := c.Notifications().Search(ctx, yclib.RawQueryOpts{Keyword: "会议", Limit: "50"})
for _, n := range res.Notifications {
    fmt.Printf("[%s] %s: %s\n", n.AppDisplayName, n.Title, n.Content)
}
```

消费方**只 import `yclib`**：入参/出参类型（`RawQueryOpts`、`StoredNotification`、`Count`、`ScanStats` …）都由 yclib re-export，不必触碰 `internal/...`。

## 配置（显式注入，无隐式 env）

```go
type Config struct {
    Profile string // 空 → "default"。不回退 $YOOOCLAW_PROFILE / active-profile 文件
    RootDir string // 空 → ~/.yoooclaw（不读 YOOOCLAW_HOME）。可用于多租户 / 测试隔离
    Logger  Logger // 可选；nil 静默。library 自身不向 stdout/stderr 打印

    // daemon 依赖能力（Light 等）所需凭证：
    Credentials              CredentialSource // 显式注入 gateway token；nil 表示不提供
    AllowImplicitCredentials bool             // 为真时允许回退读 profile 的 config/keychain（默认 false）
}
```

`New` 无任何 IO 副作用（不建目录、不写文件、不读环境变量）。`Client` 可并发只读使用。

## 已实现能力

所有公开方法首参均为 `context.Context`；返回具体导出 struct（非 `any`）；失败返回 `*yclib.Error`。

### 纯本地（不经 daemon）

| 方法 | 说明 | 对应 CLI |
|------|------|----------|
| `Notifications().Search/Summary/Stats(ctx, …)` | 查询 / 聚合摘要 / 维度统计 | `notification search/summary/stats` |
| `Images().List(ctx, ImageListOpts)` / `Get(ctx, id)` | 列出 / 取单张图片 | `image list` / `image status` |
| `Recordings().List(ctx, RecordingListOpts)` / `Get(ctx, id)` | 列出 / 取单条录音 | `recording list` / `recording status` |

### daemon 依赖

| 方法 | 说明 | 对应 CLI |
|------|------|----------|
| `Daemon().Status(ctx)` | 读取 daemon 运行态（lock + 探活，无网络） | `daemon status` |
| `Daemon().Start(ctx, DaemonStartOpts)` | **显式**启动 daemon（幂等） | `daemon start` |
| `Daemon().Stop(ctx)` | 停止 daemon（未运行为 no-op） | `daemon stop` |
| `Light().Send(ctx, LightSendOpts)` | 下发一次性灯效 | `light send` |
| `LightRules().List/Get/Create/Update/Delete/SetEnabled(ctx, …)` | 灯效规则增删改查与启停 | `lightrule …` |
| `Tunnel().Status/Reconnect/Test(ctx, client)` | Relay 隧道状态 / 重连 / 回环自检 | `tunnel …` |

> **daemon 生命周期（arc §5 硬约束）**：library **绝不隐式 fork daemon**。`Light()` 等
> daemon 依赖方法在 daemon 未运行时只返回 `CodeDaemonNotRunning`，由调用方决定是否
> `Daemon().Start(...)`。显式启动会 re-exec 当前 Go 宿主，并由 yclib 在宿主 `main`
> 运行前接管子进程作为 daemon；宿主不需要实现 yc 的命令行入口。`Config.RootDir`
> 会显式传入 daemon 子进程，不受宿主的 `YOOOCLAW_HOME` 影响。

```go
c, _ := yclib.New(yclib.Config{
    Credentials: yclib.StaticCredentials{Token: gatewayToken}, // 连 daemon 的鉴权
})
if st, _ := c.Daemon().Status(ctx); !st.Running {
    c.Daemon().Start(ctx, yclib.DaemonStartOpts{}) // 显式拉起
}
res, err := c.Light().Send(ctx, yclib.LightSendOpts{
    Preset: "green-steady",
    Title:  "任务完成",
    Reason: "任务已完成并通过校验",
})
```

## 错误处理

错误是结构化的，按错误码分支而非解析字符串：

```go
res, err := c.Notifications().Search(ctx, opts)
if err != nil {
    switch yclib.ErrorCode(err) {
    case yclib.CodeInvalidArgument:
        // 参数非法
    case yclib.CodeStorageUnavailable:
        // 存储不可用
    }
}
```

通知目录不存在（无 daemon / 尚无数据）视为正常态，返回空结果而非错误。

## 尚未覆盖（路线图）

- **录音/图片的写侧 ingestion 与录音 ASR 工作流**：当前只覆盖读侧（List/Get）；写入（App 推送）属插件侧职责。
- **Phase 4**：统一对外错误、`context.Context` 透传至底层 IO。
- **暂不纳入**：`sync` 系列（底层返回 `map[string]any`，违反类型化约束，且属插件侧 ingestion 游标协议）。

详见 `arc-cli-library-integration.md` §5（daemon 边界裁决）与 §7（落地阶段）。
