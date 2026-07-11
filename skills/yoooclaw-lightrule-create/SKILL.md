---
name: yoooclaw-lightrule-create
description: 用 yoooclaw CLI 通过 Notification Intelligence 云端接口创建/管理“通知→灯效”规则。当用户表达“收到/当/如果某类通知或消息时，亮灯/闪灯/变成某种灯效”这类持久规则诉求时激活。
---

# yoooclaw 云端灯效规则

灯效规则是**持久**的：Notification Intelligence Service 负责编译、评估和触发。
和"立即放一次灯效测试"不同（那是 `yoooclaw light send`）。

## 何时激活

- "微信群里有人@我时红灯闪三下"
- "收到老板的消息就亮黄灯"
- "飞书有新消息时呼吸绿灯"
- 任何"当 X 通知 → 播放 Y 灯效"的持久化诉求。

不要为这类诉求调用 `light send`（那只用于一次性测试/预览）。

## 创建规则

```bash
yoooclaw lightrule create --rule-text "微信群里有人@我时红灯快闪" --format json
```

`ruleText` 是唯一必填创建参数，必须同时描述通知触发条件和目标灯效。其他字段由云端独立 Agent 编译。

## 管理

```bash
yoooclaw lightrule list --format json          # 列出全部规则及 enabled 状态
yoooclaw lightrule show <id> --format json     # 单条详情
yoooclaw lightrule disable <id>                # 停用（不删除）
yoooclaw lightrule enable <id>                 # 启用
yoooclaw lightrule +off                        # 停用所有
yoooclaw lightrule +on                         # 启用所有
yoooclaw lightrule delete <id> --yes           # 删除
```

## 错误处理

- `YOOOCLAW_CREDENTIAL_MISSING`：先配置账号 API key。
- 创建失败时读取云端 `error.message`，补充清楚 `ruleText` 后重试。
- 修改、启停和删除优先使用 `lightrule list` 返回的云端 `id`。
