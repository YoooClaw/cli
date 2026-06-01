---
name: yoooclaw-tunnel-debug
description: 用 yoooclaw CLI 排查手机端推送链路是否通。当用户说"手机推送收不到""通知没同步过来""检查一下隧道/连接""daemon 还活着吗""手机连不上"时激活。组合使用 daemon status / tunnel status / tunnel +test / gateway test 自检整条接收链路。多数命令需要 daemon 在运行（🟡）。
---

# yoooclaw 隧道 / 接收链路排查

独立 daemon 当前以**直连 HTTP** 接收手机推送（`POST /notifications`），
配合用户自建 `cloudflared` / `tailscale serve` 反代到 daemon 的本地地址即可让手机端推达。

## 何时激活

- "手机推送收不到 / 通知没同步过来"
- "检查隧道 / 连接 / daemon 状态"
- "手机连不上 / 一直没新消息"

## 排查顺序

```bash
# 1) daemon 是否在跑、监听哪个地址端口
yoooclaw daemon status --format json
#   未运行 → {"ok":false,"error":{"code":"YOOOCLAW_DAEMON_NOT_RUNNING",...}} → yoooclaw daemon start

# 2) 隧道状态（relay 模式 / 是否连接 / relay URL）
yoooclaw tunnel status --format json
#   → {"ok":true,"mode":"standalone-http","connected":false,"relayUrl":"...","note":"..."}

# 3) 端到端回环自检：daemon 通过本地 HTTP 给自己发一条 echo 通知，验证 ingest+鉴权链路
yoooclaw tunnel +test --format json
#   → {"ok":true,"loopback":{"ok":true,"status":200}}  表示本机接收链路 OK

# 4) 模拟手机端调 /notifications，验证鉴权与连通
yoooclaw gateway test --format json
#   → {"ok":true,"status":200,"response":{"ok":true,"ingested":1,...}}
#   要强制走 relay 旁路：yoooclaw gateway test --via-relay
```

## 判读

- `daemon status` 报 `YOOOCLAW_DAEMON_NOT_RUNNING` → daemon 没起，先 `yoooclaw daemon start`。
- `tunnel +test` 的 `loopback.ok=true` 且 `gateway test` `ok=true`：**本机接收链路正常**。
  手机仍收不到 → 问题在网络可达性：检查防火墙、反代（cloudflared/tailscale）、手机端填的地址是否指向 daemon 的对外地址。
- `gateway test` 返回 `401 / YOOOCLAW_UNAUTHORIZED`：token 不一致。
  用 `yoooclaw auth status` 看 token 来源，必要时 `yoooclaw auth token-rotate` 后重启 daemon、并更新手机端 token。
- 想看 daemon 自身日志定位 ingest 失败：`yoooclaw daemon logs --lines 200`（或 `yoooclaw log +errors`）。

## 鉴权前置检查（不调 daemon）

```bash
yoooclaw auth status --format json   # api-key / gateway token 是否存在、来源、daemon 是否可达
yoooclaw auth check --format json    # 端到端：用本地 token 调 daemon /daemon/status 验证一致性
```
