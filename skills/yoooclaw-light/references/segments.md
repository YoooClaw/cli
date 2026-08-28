# 自定义灯效 segments

只在用户要求没有匹配预设时读取本 reference。目标是忠实生成最接近用户要求的有限灯效；`light send` 会先在本地校验 segments，通过后才下发。

## 共同结构

`--segments` 接收包含 1–12 个 segment 的 JSON 数组。多个 segment 按数组顺序播放。每段必须有 `mode` 和非负的 `duration_s`。

- 有限时长优先使用协议原生档位：`0.5`、`1`、`2`、`3`、`5`、`6`、`8`、`16`、`24`、`32`、`48` 秒；其他数值会被量化到最近档位。
- `duration_s: 0` 表示无限时长，只能在用户明确要求并再次确认后生成。
- `color` 使用 `{ "r": 0–255, "g": 0–255, "b": 0–255 }`。根据用户所说颜色选择合理 RGB；没有指定颜色时使用绿色 `{ "r": 0, "g": 255, "b": 0 }`。
- `brightness` 范围为 `0–255`。未指定时使用 `192`；除 `steady` 外必须大于 `0`。
- 用户未指定时，单段有限灯效默认持续 `5` 秒；呼吸灯默认 `8` 秒；频闪默认 `interval_ms: 300`；波浪默认 `interval_ms: 200`、`direction: "ltr"`、`window: 2`。

## 模式

### steady

常亮。字段：`mode`、`duration_s`、`brightness`、`color`。

```json
[{"mode":"steady","duration_s":5,"brightness":192,"color":{"r":128,"g":0,"b":255}}]
```

### breath

呼吸。除共同字段外可传 `breath_timing`；用户没有指定节奏时省略，让协议使用默认节奏。自定义节奏必须同时包含：`rise_ms > 0`、`hold_ms >= 0`、`fall_ms > 0`、`off_ms >= 0`。

```json
[{"mode":"breath","duration_s":8,"brightness":192,"color":{"r":255,"g":128,"b":0}}]
```

### strobe

频闪。增加非负的 `interval_ms`；优先使用协议档位 `50`、`100`、`200`、`300`、`500`、`600`、`800`、`1600`、`2400`、`3200`、`4800` 毫秒。

```json
[{"mode":"strobe","duration_s":2,"interval_ms":300,"brightness":192,"color":{"r":255,"g":0,"b":255}}]
```

### wave

单色波浪。增加 `interval_ms`、`direction`（`ltr` 或 `rtl`）和 `window`（`1`、`2` 或 `3`）；可选 `background`，其结构为 RGB 加 `brightness`。

```json
[{"mode":"wave","duration_s":5,"interval_ms":200,"brightness":192,"color":{"r":0,"g":255,"b":255},"direction":"ltr","window":2,"background":{"r":0,"g":0,"b":0,"brightness":0}}]
```

### color_flow

双色或多色流动。字段与 `wave` 相同，至少一个颜色锚点必须非黑色；表达多色流动时同时设置前景 `color` 和亮度大于 `0` 的 `background`。单色流动使用 `wave`。

```json
[{"mode":"color_flow","duration_s":8,"interval_ms":300,"brightness":192,"color":{"r":255,"g":0,"b":128},"direction":"ltr","window":2,"background":{"r":0,"g":64,"b":255,"brightness":192}}]
```

## 生成与校验

1. 把用户明确的颜色、模式、方向、时长、速度、亮度和播放顺序逐项映射到 segments；只为缺失的非关键参数使用上述默认值。
2. 默认生成一次有限播放。无限时长或无限重复需要单独确认。
3. 将紧凑 JSON 数组作为单个 `--segments` 参数传给 `light send`，并始终同时填写 `--title` 和 `--reason`。
4. CLI 返回 `VALIDATION_FAILED` 时，根据逐字段错误只修正无效字段并重试一次；其他失败按一次性灯效的核验规则处理。
