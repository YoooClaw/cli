---
name: yoooclaw-tunnel-debug
description: 用 yoooclaw CLI 排查手机端推送链路是否通。当用户说“手机推送收不到”“通知没同步过来”“检查一下隧道/连接”“daemon 还活着吗”“手机连不上”时激活。组合使用 auth status / daemon status / tunnel status / tunnel +test / gateway test / daemon logs 定位本地配置、daemon、本地 ingest 鉴权与 Relay WebSocket 状态。多数命令需要 daemon 在运行（🟡）。
---

# yoooclaw Relay / 接收链路排查

独立 daemon 默认用 account api-key 连接托管 Relay WebSocket，手机 App 绑定同一账号后经 Relay 收发。也可以在 Relay 未启用或不可用时，将 `POST /notifications` 通过用户自建 `cloudflared` / `tailscale serve` 暴露为直连 HTTP fallback。

## 排查顺序

```bash
# 1) 本地凭据是否存在；不调 daemon
yoooclaw auth status --format json

# 2) daemon 是否在跑、监听哪个地址端口
yoooclaw daemon status --format json
# 未运行 → yoooclaw daemon start --format json

# 3) Relay 模式、连接状态、URL、多隧道状态
yoooclaw tunnel status --format json

# 4) daemon 本地回环：验证本地 ingest + 鉴权
yoooclaw tunnel +test --format json

# 5) 模拟手机端直接调本地 /notifications
yoooclaw gateway test --format json

# 6) 看 Relay 连接与重连日志
yoooclaw daemon logs --lines 200 --format json
yoooclaw log +errors --format json
```

`tunnel +test` 会让 daemon 给本地 `/notifications` 发一条 echo 通知。它可以确认本地 ingest + 鉴权链路，但不会让流量真正绕行远端 Relay。`gateway test --via-relay` 当前也是复用这条本地回环路径，不应单独作为远端 Relay 可达性的证据。

## 判读

- `auth status` 中 api-key 不存在：先用 `yoooclaw auth set-api-key -` 从 stdin 设置，再启动或 reload daemon。
- `daemon status` 报 `YOOOCLAW_DAEMON_NOT_RUNNING`：运行 `yoooclaw daemon start`。
- `tunnel status` 的 `mode=relay` 且 `connected=true`：daemon 当前已连上 Relay WebSocket。
- `tunnel status` 的 `mode=relay` 但 `connected=false`：结合 `lastDisconnectReason`、`reconnectAttempt` 和 daemon 日志排查 api-key、网络与 Relay 服务。
- `tunnel status` 的 `mode=standalone-http`：当前没有 Relay 隧道；按返回的 `note` 检查 `relay.enabled` 和 api-key。只有明确采用直连 fallback 时才检查防火墙、反代和手机端地址。
- `tunnel +test` 或 `gateway test` 失败：先排查本地 gateway token。运行 `yoooclaw auth status`，必要时 `yoooclaw auth token-rotate`；daemon 已运行时随后执行 `yoooclaw daemon restart`。

## 多 clientLabel

多 api-key 模式下，每个 label 对应一条 Relay 隧道。按 label 缩小排查范围：

```bash
yoooclaw auth list-api-keys --format json
yoooclaw tunnel status --client work --format json
yoooclaw tunnel +test --client work --format json
yoooclaw tunnel reconnect --client work --format json
```

## 鉴权检查

```bash
yoooclaw auth status --format json
yoooclaw auth check --format json
```

`auth status` 只读本地凭据与 daemon lock。`auth check` 会用本地 gateway token 调 daemon `/daemon/status`，用于确认 CLI 和 daemon 的 token 是否一致。
