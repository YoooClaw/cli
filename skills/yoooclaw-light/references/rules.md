# 通知灯效持久规则

## 创建

用户表达“当 X 通知出现时播放 Y 灯效”后，把触发条件和灯效要求整理成一句忠实、完整的自然语言 `--intent`，交给云端 Agent 编译并保存：

```bash
yoooclaw lightrule create \
  --intent "微信群里有人@我时红灯快闪" \
  --format json
```

用户原话缺少触发条件或灯效要求，且无法从上下文唯一确定时，询问缺失项。不得擅自添加 App、发送人、关键词、颜色、模式或次数，也不得自行构造 `segments`。用户已给出完整规则时直接创建；出现“一直、持续、不停、无限”等可能生成无限灯效的表达时，先确认风险。

创建响应须同时满足退出状态成功和顶层 `ok=true`。随后用返回的 ID 执行 `lightrule show <id> --format json` 核实保存结果；报告实际规则名称/ID、标题、启用状态和 warning。详情核验失败时说明“创建已返回成功，详情核验失败”，不再次创建。规则已保存不等于已经命中过通知。

## 查看和管理

```bash
yoooclaw lightrule list --format json
yoooclaw lightrule show <id-or-name> --format json
yoooclaw lightrule disable <id-or-name> --format json
yoooclaw lightrule enable <id-or-name> --format json
yoooclaw lightrule +off --format json
yoooclaw lightrule +on --format json
```

- 用户按标题或自然语言指代规则且没有准确 ID/name 时，先 `list` 找候选；唯一匹配后使用返回的 ID，多个候选时请用户选择。
- “暂停/暂时关闭”使用 `disable`，保留规则定义。
- `+off` 和 `+on` 修改全部规则，只响应用户明确的“全部停用/全部启用”。批量结果除顶层 `ok` 外还要逐项检查 `results[].ok`，如实报告部分失败。

## 更新

```bash
# 根据新的自然语言要求重新编译整条规则
yoooclaw lightrule update <id-or-name> \
  --intent "改成绿灯呼吸" --format json

# 只修改指定字段
yoooclaw lightrule update <id-or-name> \
  --title "老板微信" --repeat-times 3 --format json
```

`--intent` 与 `--title`、`--description`、`--segments`、`--repeat-times` 互斥。自然语言修改触发条件或灯效时使用 `--intent`；只有用户明确给出结构化灯效段时才传 `--segments`。`--repeat` 或 `--repeat-times 0` 表示无限循环，执行前必须确认风险。

更新成功后用 `show` 核验目标字段和启用状态；核验失败时不重复更新。

## 删除

删除是云端软删除。先用 `show` 核对目标；只有用户明确要求删除该规则时才执行：

```bash
yoooclaw lightrule delete <id-or-name> --yes --format json
```

成功结果须同时满足顶层 `ok=true` 和 `deleted=true`。目标不唯一或用户只是说“不想让它触发”时，澄清目标或使用 `disable`。

用户明确要求删除全部持久规则时：

1. 先 `list` 获取全部未删除规则的 ID、名称和数量，展示影响范围并再次确认具体集合。
2. 确认后严格串行执行单条 `delete <id> --yes --format json`，逐条核验。
3. 任一删除失败或结果不确定时停止后续删除，报告已删除项和剩余项。

CLI 没有批量删除命令。一次性播放和内置预设也不是可删除对象。

## 失败处理

- `AUTH_REQUIRED`：使用 stdin 方式重新配置 API key。
- `NOT_FOUND`：先 `lightrule list` 重新定位 id/name。
- `VALIDATION_FAILED`：转述脱敏后的字段错误；自然语言编译失败时不退化为手写 `segments`。
- 网络失败：写操作最多重试一次；结果不确定时先 `list/show` 核实。
- 云端返回 `warning`：规则可能已成功创建或更新，先报告 warning，不无条件重试。
- 规则已保存但未按预期触发：用 `lightrule show` 核对定义和启用状态，再运行 `yoooclaw doctor --format json` 检查环境；只有用户明确要求立即播放时才进入一次性灯效分支。
