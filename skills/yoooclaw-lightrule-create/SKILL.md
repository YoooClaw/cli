---
name: yoooclaw-lightrule-create
description: 用 yoooclaw CLI 创建/管理"通知→灯效"规则。当用户表达"收到/当/如果某类通知或消息时，亮灯/闪灯/变成某种灯效"这类**持久规则**诉求时激活。规则由 daemon 在通知 ingest 后评估命中并触发灯效。需要 daemon 在运行（🟡）。从 stdin 用 --from-file - 提交规则定义最稳妥。
---

# yoooclaw 灯效规则创建（从 stdin）

灯效规则是**持久**的：通知到达后 daemon 评估是否命中，命中则播放灯效。
和"立即放一次灯效测试"不同（那是 `yoooclaw light send`）。

> 需要 daemon 在跑：先 `yoooclaw daemon status`，未运行则 `yoooclaw daemon start`。

## 何时激活

- "微信群里有人@我时红灯闪三下"
- "收到老板的消息就亮黄灯"
- "飞书有新消息时呼吸绿灯"
- 任何"当 X 通知 → 播放 Y 灯效"的持久化诉求。

不要为这类诉求调用 `light send`（那只用于一次性测试/预览）。

## 创建规则（首选 --from-file - 从 stdin）

```bash
cat <<'JSON' | yoooclaw lightrule create --from-file - --format json
{
  "name": "wechat-at-me",
  "title": "微信@我",
  "description": "微信群里有人@我时红灯快闪",
  "segments": [
    { "mode": "strobe", "duration_s": 2, "brightness": 255,
      "color": { "r": 255, "g": 0, "b": 0 }, "interval_ms": 200 }
  ]
}
JSON
```

字段说明：

- `name`（必填）：规则唯一标识。
- `description`（必填）：自然语言意图，daemon 的 webhook 评估器据此判断通知是否命中。
- `segments`（必填）：命中后播放的灯效，遵循 light protocol（mode/duration_s/brightness/color/interval_ms 等）。
- 也可用 flag 形式：`--name --intent <描述> --light-action <segments JSON> --match-rules <硬过滤 JSON>`。

## 管理

```bash
yoooclaw lightrule list --format json          # 列出全部规则及 enabled 状态
yoooclaw lightrule show <name> --format json   # 单条详情
yoooclaw lightrule disable <name>              # 停用（不删除）
yoooclaw lightrule enable <name>               # 启用
yoooclaw lightrule +off                        # 停用所有
yoooclaw lightrule +on                         # 启用所有
yoooclaw lightrule delete <name> --yes         # 删除
```

## 错误处理

- `YOOOCLAW_DAEMON_NOT_RUNNING`：先 `yoooclaw daemon start` 再重试。
- 创建失败常见为 `VALIDATION_FAILED`（segments 不合法）或 `INVALID_PARAMS`（缺 name/description）：
  读 `error.message`（含具体校验项）后修正 JSON 重新提交。
- 规则名重复：换 `name` 或先 `delete`。
