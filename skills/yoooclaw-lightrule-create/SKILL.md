---
name: yoooclaw-lightrule-create
description: 用 yoooclaw CLI 创建/管理"通知→灯效"规则。当用户表达"收到/当/如果某类通知或消息时，亮灯/闪灯/变成某种灯效"这类**持久规则**诉求时激活。规则保存在云端 Notification Intelligence Service，由云端评估命中并触发灯效。只需把用户的自然语言诉求原样传给 --intent。
---

# yoooclaw 灯效规则创建（云端）

灯效规则是**持久**的：保存在云端 Notification Intelligence Service，通知到达后由云端评估是否命中，命中则播放灯效。
和"立即放一次灯效测试"不同（那是 `yoooclaw light send`）。

> 不需要 daemon。规则 CRUD 直接打云端 API（X-Api-Key-Id 鉴权），需要先配好 api-key（`yoooclaw auth set-api-key -` 从 stdin 读）。
> 云端主机按 `env > cloud.host > relay.url > 内置默认` 解析，与灯效 / ASR 共用同一套环境。

## 何时激活

- "微信群里有人@我时红灯闪三下"
- "收到老板的消息就亮黄灯"
- "飞书有新消息时呼吸绿灯"
- 任何"当 X 通知 → 播放 Y 灯效"的持久化诉求。

不要为这类诉求调用 `light send`（那只用于一次性测试/预览）。

## 创建规则（自然语言 --intent）

把用户的原始自然语言诉求整理成一句话传给 `--intent`，由云端独立 Agent
编译出 name/title/description/segments/repeat_times 并保存：

```bash
yoooclaw lightrule create --intent "微信群里有人@我时红灯快闪" --format json
```

返回 `{ok, id, name}`（可能带 `warning`）。**不需要**自己构造 segments。

## 管理

```bash
yoooclaw lightrule list --format json          # 列出云端全部规则及 enabled 状态
yoooclaw lightrule show <id> --format json     # 单条详情（id 或 name 均可）
yoooclaw lightrule disable <id>                # 停用（不删除）
yoooclaw lightrule enable <id>                 # 启用
yoooclaw lightrule +off                        # 停用所有
yoooclaw lightrule +on                         # 启用所有
yoooclaw lightrule delete <id> --yes           # 删除（云端软删除）
```

## 更新

两种互斥形态：

```bash
# 语义重编译：传新的自然语言，云端 Agent 重新生成灯效
yoooclaw lightrule update <id> --intent "改成绿灯呼吸"

# 普通字段局部更新（不重编译）
yoooclaw lightrule update <id> --title "老板微信" --repeat-times 3
yoooclaw lightrule update <id> --segments '[{"mode":"strobe","duration_s":2,"brightness":255,"color":{"r":255,"g":0,"b":0},"interval_ms":200}]'
```

`--intent` 不能与 `--title/--description/--segments/--repeat-times` 混用。

## 错误处理

- `AUTH_REQUIRED`：api-key 未配置，先 `yoooclaw auth set-api-key -`（从 stdin 读取，不要把 key 写进命令行）；`yoooclaw auth status --format json` 可确认当前凭据。
- `NOT_FOUND`：id/name 不存在，先 `lightrule list` 确认。
- `VALIDATION_FAILED`：segments 不符合 light protocol，`error.message` 内有逐字段错误。
- 规则创建成功但灯不亮：先 `yoooclaw light +blink` 验证硬件链路，再 `yoooclaw doctor --format json` 看本地环境；灯效走云端下发，规则命中与硬件连通是两件事。
