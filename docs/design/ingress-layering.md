# Ingress 分层设计：让 daemon 连接可选、可代理

状态：yc-cli 侧已实现（standalone / proxied / direct 三模式 + Egress 端口）· hermes-plugin 侧待接入 · 目标读者：yc-cli + hermes-plugin 维护者

## 1. 背景与问题

手机端的数据（通知 / 录音 / 图片）要进入本机，今天有两条独立的「到手机」的连接实现：

- **yc-cli（Go）**：daemon 内置 `relay.Supervisor`，每个 api-key 一条到云端 Relay 的 WebSocket 隧道，收到 frame 后 `Dispatcher` loopback 回 daemon 自己的 HTTP ingest 端点落盘。
- **hermes-plugin（Python）**：`yoooclaw_app/app_transport/`（`relay.py` + `rpc.py`）是**另一套**完整的 Relay/RPC 客户端，同样直连手机端。同时它又通过 `DaemonSupervisor` spawn `yoooclaw daemon run-foreground` 并 `tunnel reconnect`。

当 cli 内嵌进插件时，这两条连接可能**同时**打开 → 对手机端是双连接、对落盘是双重 ingest、对生命周期是两套重连/心跳。这正是要消除的层次混乱。

## 2. 核心洞察（决定了设计可以很轻）

阅读现有代码得到三个关键事实：

1. **存储是文件化的，读路径不经过 daemon。** `notification list/today/recent`、recording、image 等查询命令直接 `notif.Query(ctx.Paths.Notifications, …)` 读文件。只有 `cmd_auth` / `cmd_daemon` / `cmd_daemon_services` 用 `daemon.Client`。
2. **daemon 实际只负责三件事**：① ingress（Relay 隧道）② ingest HTTP API ③ 生命周期/状态。它是「传输 + HTTP 包装」，不是数据中枢。
3. **ingest API 已经支持外部推送。** `isIngestPath` + `http-api-key` 鉴权让任何持有 api-key 的客户端都能 `POST /notifications`；`relay.enabled=false`（server.go）已经能关掉隧道而保留 ingest API。

结论：要的分层**大部分已经存在**。这份设计是把它**显式化**，补齐「出站」与「模式选择」两块，并规定「**每种模式下到手机的连接有且只有一个 owner**」。

## 3. 分层模型

```
            ┌──────────────────────────────────────────────┐
  L3 Ingress │  传输层（可插拔，单一 owner）                  │
   (transport)│  · relay   —— Go daemon 跑隧道（standalone）   │
            │  · proxy   —— host 代理（embedded / 插件）      │
            │  · direct  —— LAN/dev 直接 POST                │
            └───────────────┬──────────────────────────────┘
                            │  入站：HTTP POST ingest
                            │  出站：Egress 端口
            ┌───────────────▼──────────────────────────────┐
  L2 Edge    │  Ingest / Egress API（稳定契约 = 「cli 暴露的 API」）│
            │  POST /notifications /recordings /images       │
            │  + Egress: 把出站事件交给当前传输层            │
            └───────────────┬──────────────────────────────┘
            ┌───────────────▼──────────────────────────────┐
  L1 Core    │  Ingest Core（纯库，无网络）                   │
            │  notif / recording / image：dedup + 落盘 + 查询 │
            └───────────────────────────────────────────────┘
```

两个**端口（port）**把核心和传输解耦：

- **Ingress（入站，host → core）**：就是 L2 的 HTTP ingest 端点。谁来「喂」它由模式决定，core 侧不需要知道。
- **Egress（出站，core → 手机）**：新增一个接口，替换今天散落的 `s.tunnelSupervisor.PushEvent(...)`。
  ```go
  type Egress interface {
      PushEvent(event string, payload any) error
  }
  ```
  实现：`RelayEgress`（走隧道，= 现状）、`ProxyEgress`（POST 到 host 回调 URL）、`NoopEgress`（落盘即止）。

> 出站今天很轻：只有 `recording.status` 一种事件 + 手机→daemon 的 RPC（查询/灯控）由 dispatcher loopback。所以 Egress 接口落地成本低。

## 4. 三种运行模式

由 `ingress.mode` 选择（config 字段 + `--ingress` 启动 flag + `YOOOCLAW_INGRESS` 环境变量，优先级 flag > env > config）：

| 模式 | 到手机的连接 owner | Go relay.Supervisor | ingest 鉴权 | 出站 Egress |
|------|------------------|--------------------|------------|------------|
| `standalone`（默认，= 现状） | Go daemon 的隧道 | 启用 | gateway-token / 本机 | `RelayEgress` |
| `proxied`（嵌入 hermes-plugin） | **host 插件**的 app_transport | **关闭** | **必须 api-key** | `ProxyEgress` → host 回调 |
| `direct`（LAN / 测试） | 调用方直接 POST | 关闭 | api-key / token | `NoopEgress` |

`proxied` 在语义上 ≈ 今天的 `relay.enabled=false` + 一个给插件用的 api-key，但显式成一个命名模式后：
- daemon 启动时**不再**尝试连隧道（消除双连接）；
- 校验 ingest 必须带 api-key（loopback/本机豁免不再适用于外部 host）；
- 注册一个 Egress 回调，使出站事件能回到插件。

## 5. 嵌入契约（proxied 模式）

插件与 cli 在同一台机器、localhost 通信。契约是**双向**的：

### 5.1 入站：插件 → cli（喂数据）

插件的 `app_transport` 收到手机 frame 后，按映射 POST 到 cli ingest API（已存在的端点）：

```
POST http://127.0.0.1:<port>/notifications      Authorization: Bearer <hermes-api-key>
POST http://127.0.0.1:<port>/recordings         （含 /gateway/recordings.* 分片/异步变体）
POST http://127.0.0.1:<port>/images
```

`clientLabel` 由 api-key 映射得到（server.go `labelForAPIKey`），用于多租户区分，无需新协议。

### 5.2 出站：cli → 插件（回事件）

已采用 **回调 webhook**（与入站对称）：daemon 启动时由 `--egress-callback-url` + `--egress-callback-token`（或 `config.ingress.egressCallback`）拿到 host 回调地址；`ProxyEgress.PushEvent` 即 `POST callbackUrl {event, payload}`（带 `Authorization: Bearer <token>`）。插件收到后用自己的 app_transport relay 转发给手机。未配置回调时退化为 `NoopEgress`（出站丢弃，仅告警）。

> 备选未采用：拉取队列 `GET /egress/events`（SSE / long-poll）。无需 host 开端口，但插件要常驻订阅循环；如宿主不便监听端口可再切换。

手机 → cli 的 RPC（如「列通知」「发灯」）在 proxied 模式下由**插件**接收：因为读路径是直读文件，插件可直接调用 cli 查询命令或 L2 只读查询，不必经隧道。

### 5.3 进程模型

保持 **sidecar**：插件仍 spawn `yoooclaw daemon run-foreground --ingress=proxied`（Go/Python 无法同进程链接）。相对现状仅多两个动作：传 `--ingress=proxied`、provision 一个 `hermes` api-key + egress 回调。

> 备选（更激进的简化，列为 future）：**daemon-less one-shot**。因为读直读文件、core 是纯库，插件可对每批数据执行 `yoooclaw ingest --from-file -` 短命令落盘，彻底去掉常驻端口 / gateway-token / daemon 存活监控。代价是出站事件失去常驻端点，需改成插件侧 app_transport 全权负责出站。建议先做 sidecar，验证后再评估。

## 6. 代码改动点（最小化）

L1/L2 几乎不动，主要是把传输从 server 里抽出去：

1. **`internal/daemon/egress.go`（新）**：定义 `Egress` 接口 + `RelayEgress`/`ProxyEgress`/`NoopEgress`。`server` 持有 `egress Egress` 取代直接引用 `tunnelSupervisor.PushEvent`（改 `server_ingest.go`）。
2. **`internal/config`**：新增 `Ingress.Mode`（`standalone|proxied|direct`，默认 `standalone`）与 `Ingress.EgressCallback{URL,Token}`。
3. **`internal/daemon/server.go` `RunForeground`**：按 `mode` 装配——
   - `standalone`：现状（relay.Supervisor + RelayEgress）。
   - `proxied`：跳过 Supervisor；要求 `credentialSet` 非空否则报错（没有 api-key 插件无法鉴权）；装配 `ProxyEgress`。
   - `direct`：跳过 Supervisor + `NoopEgress`。
4. **`cmd_daemon.go`**：`run-foreground` / `start` / `restart` 增加 `--ingress` / `--egress-callback-url` / `--egress-callback-token` flag，透传到 `StartOpts` 并经 `Spawn` 传给 detach 子进程。
5. **hermes-plugin 侧**：`DaemonSupervisor._spawn_run_foreground` 加 `--ingress=proxied`；启动时 provision `hermes` api-key 并把 app_transport 的入站改成 POST cli ingest（而非自己落盘/双轨）；注册 egress 回调。

## 6.5 实现状态（yc-cli 侧）

已落地：

- `config.IngressSection`（`ingress.mode` + `ingress.egressCallback{url,token}`，默认 `standalone`）。
- `daemon.Egress` 端口 + `RelayEgress` / `ProxyEgress` / `NoopEgress`（`internal/daemon/egress.go`）。
- `RunForeground` 按模式装配：`proxied` 跳过 Supervisor 并强制要求 api-key（否则 `YOOOCLAW_UNAUTHORIZED`）、装配 `ProxyEgress`；`direct` 跳过 Supervisor + `NoopEgress`；`standalone` 维持原 Relay 行为。`/daemon/reload` 仅在 `standalone` 重建隧道。
- `recording.status` 出站改走 `s.egress.PushEvent`（`server_ingest.go`）。
- `daemon run-foreground|start|restart` 新增 `--ingress` / `--egress-callback-url` / `--egress-callback-token`，经 `Spawn` 透传给 detach 子进程。
- `/daemon/status` 新增 `ingressMode` 字段。
- 单测 `internal/daemon/egress_test.go`；三模式手工冒烟通过。

待办（hermes-plugin 侧，见第 6 节第 5 项）：`DaemonSupervisor` spawn 传 `--ingress=proxied` + egress 回调；provision `hermes` api-key；app_transport 入站改为 POST cli ingest API、订阅 egress 回调转发。

> 注意：`config init` 收尾会自动 `Spawn` 一个 **standalone** daemon（`startDaemonForInit`）。嵌入流程里插件应在 init 后先 `daemon stop`，再以 `--ingress=proxied` 拉起，避免起到 standalone 实例。

## 7. 兼容与迁移

- 默认 `standalone` = 完全现状，老用户零感知。
- `relay.enabled` 保留：`proxied`/`direct` 下强制关闭隧道（模式优先于 `relay.enabled`），不再因 `relay.enabled` 默认 true 而误连隧道。
- ingest 端点路径、鉴权、frame 格式都不变 → 插件迁移可灰度：先让 app_transport 改走 ingest API，再关 Go 隧道。

## 8. 一句话总结

把 daemon 拆成「**可插拔传输层 + 稳定 ingest/egress 契约 + 文件化核心**」，用 `ingress.mode` 选择**唯一**的传输 owner：独立运行时 Go 自己连隧道，嵌入插件时让插件代理、cli 只暴露 ingest API 收数据——消除双连接、双 ingest，且 90% 复用现有代码。
