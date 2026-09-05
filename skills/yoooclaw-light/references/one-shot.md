# 一次性下发

## 参数选择

屏幕展示和一次性灯效都使用 `yoooclaw light send`。每次调用都必须填写 `--title` 和 `--reason`：

- `title` 是屏幕显示的通知标题，例如“任务完成”或“需要关注”。
- `reason` 是屏幕显示的通知正文；纯灯效请求用它说明亮灯原因，例如“代码修改与校验已完成”。
- 只根据用户原话和当前任务已核验的状态填写；现有信息足以忠实概括时直接生成，无法确定真实原因时询问用户。

用户要求把内容、文字或结果显示到硬件屏幕时，将简短标题放入 `--title`，正文压缩到 30 个字符以内后放入 `--reason`。这是一次性展示请求；命令成功表示请求已下发，最终回复使用“展示请求已下发”。

优先选择与请求匹配的内置预设：

| 预设 | 效果 |
|---|---|
| `green-steady` | 绿色常亮约 5 秒 |
| `green-breath` | 绿色呼吸约 8 秒 |
| `blue-wave` | 蓝色波浪约 5 秒 |
| `blue-breath` | 蓝色呼吸约 8 秒 |
| `red-steady` | 红色常亮约 5 秒 |
| `red-breath` | 红色呼吸约 8 秒 |
| `red-strobe-3` | 红色短促三闪 |
| `yellow-strobe-3` | 黄色短促三闪 |

用户没有指定颜色和效果时，使用 `green-steady`。用户指定颜色但没有指定效果时，优先使用该颜色的 `steady` 预设；没有对应预设时生成该颜色的 `steady` segment。普通任务状态不升级为红色或频闪。

用户要求的颜色、模式、时长或组合没有合适预设时，完整读取 [`segments.md`](segments.md)，根据用户要求生成协议兼容的 `segments` 并使用 `--segments`；缺少的非关键参数采用 reference 中的安全默认值，不只因为没有预设就降低或拒绝用户要求。用户要求播放某条已保存规则的灯效时可使用 `--rule <name>`。

## 执行

```bash
yoooclaw light send \
  --preset green-steady \
  --title "<亮灯标题>" \
  --reason "<亮灯原因>" \
  --format json
```

自定义灯效：

```bash
yoooclaw light send \
  --segments '[{"mode":"steady","duration_s":5,"brightness":192,"color":{"r":128,"g":0,"b":255}}]' \
  --title "<亮灯标题>" \
  --reason "<亮灯原因>" \
  --format json
```

长任务按用户明确约定的时点播放：开始/处理中提示使用用户指定效果，未指定时用 `blue-wave`；交付物完成并验证后再播放完成效果，未指定时用 `green-steady`。失败灯效仅在用户约定后播放。

用户直接要求一次性亮灯即构成该次下发的授权。`--repeat` 或 `--repeat-times 0` 会无限播放，只有用户明确要求并再次确认风险后才能使用。

## 核验

- 退出状态成功且顶层 `ok=true`：报告请求已下发，并带上实际 title、reason 和 preset/rule。
- 顶层 `ok=false`：转述脱敏后的业务 code/message，不声称硬件已亮。
- 设备离线：报告设备离线，请用户检查硬件与 App 连接；停止重试。
- 鉴权失败：停止下发，提示用户在 YoooClaw App 重新接入当前 Agent。
- 网络失败：最多重试一次，仍失败则报告网络问题。

除非用户明确进行技术排查，不展示 `bizUniqueId`、完整服务响应或内部 URL。
