# NanoShell 端侧能力一览（AiNote 真机）

本文是 **Guest / 壳侧能力的权威清单**：`NS_CAP_*` 以 `host/include/nanoshell/capability.h` + `plat_sys.c` 的 `cap_has` 为准；ABI 签名见 [host-api-v1.md](host-api-v1.md)；写 App 见 [guest-app-guide.md](guest-app-guide.md)；真机 RPC / 共存见 [architecture-ainote.md](architecture-ainote.md)。

**日期：2026-10-08**（与当前固件对齐）。

### 本文覆盖什么

| 层 | 内容 | 是否完整 |
|----|------|----------|
| Guest cap（`NS_CAP_*`） | ID 1–27 全部枚举；开/关以 `cap_has` 为准 | **是** — 头文件里定义的 ID 全在此 |
| 生命周期 / 工具 API | heartbeat、键、事件、launch args、RNG…（无独立 cap） | **是** — Guest 必用，见 §B |
| 壳 / 上位机 | RPC、SD 通道、Store、自启 | **是**（摘要）；细节在 architecture-ainote |
| 产品侧（录音机 UI、OTA、GATT…） | 不在 NanoShell 沙箱内 | **否** — 本文不列 |

探测约定：调用平台 API 前先 `api->cap_has(NS_CAP_*)`；为 `false` 时不要调用（或期望 `NS_ERR_NOT_SUPPORTED` / `-errno`）。

---

## A. Guest 能力总表（`NS_CAP_*`）

| ID | 宏 | 状态 | Guest 入口 |
|----|-----|------|------------|
| 1 | `NS_CAP_GFX` | ✅ | Draw：`gfx_*` / `ns_draw_*` / sprite / font |
| 2 | `NS_CAP_INPUT` | ✅ | `poll_key` / `event_poll`（DOWN/UP/REPEAT） |
| 3 | `NS_CAP_RECORDER` | ✅ | `rec_*`（原生 + **Wasm import 已开放**） |
| 10 | `NS_CAP_SYS_BATTERY` | ✅ | `sys_battery_percent` |
| 11 | `NS_CAP_SYS_CHARGE` | ✅ | `sys_charge_state` |
| 12 | `NS_CAP_SYS_TIME` | ✅ | `now_ms` / `delay_ms`（及会话看门狗） |
| 20 | `NS_CAP_BLE_DATA` | ✅ | `ble_send` / `ble_recv` |
| 21 | `NS_CAP_LED` | ✅ | `led_effect` / `led_off` |
| 22 | `NS_CAP_HAPTIC` | ✅ | `vibe` / `vibe_pulse` / `vibe_stop` |
| 23 | `NS_CAP_NOTIFY` | ✅ | `notify_recv`（占屏 divert） |
| 24 | `NS_CAP_UI` | ✅ | `ui_*` Widget |
| 25 | `NS_CAP_TIMER` | ✅ | `timer_create` / `cancel` / `restart` |
| 26 | （保留） | — | 原 PCM/喇叭；设备无喇叭，已删除 |
| 27 | `NS_CAP_KV` | ✅ | `kv_set` / `kv_get` / `kv_remove`（≤8×32B，按 app id） |
| 28 | `NS_CAP_SCREEN` | ✅ | `screen_is_on` / `on` / `off` / `brightness_*` |

未列出的 ID（4–9、13–19 等）**未分配**，`cap_has` 一律 `false`。

**明确不做（Guest 侧）：** 任意 GATT/HCI/连接管理、关机、OTA、全盘文件系统、喇叭/PCM/tone、Wi‑Fi AP 细节。

---

## A.1 显示 — GFX (1) + UI (24)

逻辑分辨率 **240×120** RGB565。Guest **不**持全屏 framebuffer；两种模式都进产品 LVGL Partial（~5–10KB 条带）。

| 模式 | Cap | API | 用途 / 限额 |
|------|-----|-----|-------------|
| Draw | GFX | `clear` / `fill_rect` / `fill_round_rect` / **`fill_circle`** / `draw_line` / `draw_text*` / `present`；另 `draw_sprite` / `text_measure` / `asset_info`（`blit` 仅 native） | 游戏 / 大厅；**≤192 cmd/帧** |
| Widget | UI | `ui_label/button_create` / `ui_delete` / `ui_set_text_id` / `ui_set_pos` / **`ui_set_size`** / `ui_set_visible` / `ui_set_fg/bg` / `ui_hit_test` / `ui_pointer` | 控件池 **≤16**；文字走 Host `text_id` 表 |

NSP1：`need_gfx` / `need_ui` 位；装载时若缺对应 cap → 拒绝。  
颜色：`NS_RGB565(r,g,b)`。Sprite：`NS_SPRITE_BALL/PLAYER/ENEMY/COIN`（1..4）；字体：`NS_FONT_SMALL/MEDIUM/LARGE`（16..18）。

---

## A.2 输入 — INPUT (2)

| API | 行为 |
|-----|------|
| `poll_key()` | 仅 **DOWN** 键码；无相位；无 UP/REPEAT |
| `event_poll()` | 完整事件：KEY（相位在 handle）/ TIMER / UI / STOP |

键码（全端统一，`types.h` / `ns_abi.h`）：

| 值 | 名 |
|----|-----|
| 1–4 | LEFT / RIGHT / UP / DOWN |
| 5 | OK |
| 6 | BACK |
| 7–8 | INSTALL / UNINSTALL（壳用） |

相位：`NS_KEY_PHASE_DOWN=1` / `UP=2` / `REPEAT=3`。

**AiNote 占屏物理映射：**

| 物理 | 大厅 | App 内 |
|------|------|--------|
| 语音短按 | RIGHT DOWN | OK DOWN |
| 语音长按/按住 | OK DOWN（启动） | OK REPEAT |
| 语音抬起 | UP | UP |
| 电源短按 | BACK → 退产品 | BACK → `exit_app` |

Wasm 打包：`u32 = type \| (handle<<8) \| ((value&0xffff)<<16)`（见 host-api）。

---

## A.3 录音 — RECORDER (3)

桥接产品麦克风管线（Sim / Web 为假计时）。**Wasm import 与原生 builtin 均可用**（`allowed_imports.txt` / `wasm_runner.c`）。

| Import / `ns_*` | 签名 | 说明 |
|-----------------|------|------|
| `rec_get_state` | `i()` | 0 idle / 1 recording / 2 paused |
| `rec_is_starting` | `i()` | 启动中 |
| `rec_start` / `stop` / `pause` / `resume` | `i()` | 控制；忙/非法时负错误码 |
| `rec_elapsed_ms` | `i()` | 已录时长 ms |
| `rec_filename` | `i(*i)` | **拷进 Guest 缓冲**，返回字节数（不含 NUL）；原生 `api->rec_filename()` 仍是 `const char*` |
| `rec_add_marker` | `i(*)` | 打点（`source`；空则 `"NS"`） |

Guest 用法：先 `ns_cap_has(NS_CAP_RECORDER)`，再 `ns_rec_start()` 等。一般玩法 App 不必用。

---

## A.3b 亮灭屏 — SCREEN (28)

产品无单独 `screen_on()` API，实际是 **NORMAL wakelock 脉冲 + `display_composer` 亮度**。NS 占屏期间自己持有 NORMAL lock，故 Guest **灭屏 = 亮度置 0**（软灭），不拆 NS 的 wakelock。

| Import / `ns_*` | 签名 | 说明 |
|-----------------|------|------|
| `screen_is_on` | `i()` | 1=可见（亮度>0 且 disp active）；0=灭 |
| `screen_on` | `i()` | 恢复上次非 0 亮度 + wakelock 脉冲 |
| `screen_off` | `i()` | 记住当前亮度，设为 0 |
| `screen_brightness_get` | `i()` | 0..255 |
| `screen_brightness_set` | `i(i)` | 0..255；`0` 等价软灭 |

先 `ns_cap_has(NS_CAP_SCREEN)`。Sim / Web 为内存假状态。

---

## A.4 系统只读 — BATTERY (10) / CHARGE (11) / TIME (12)

| Cap | API | 返回 |
|-----|-----|------|
| 10 | `sys_battery_percent()` | 0–100；未知 **-1** |
| 11 | `sys_charge_state()` | 0 未充 / 1 充电中 / 2 充满；不可用 **-1** |
| 12 | `now_ms()` / `delay_ms(ms)` | Host 单调时间；阻塞延时（须配合 `heartbeat`） |

看门狗默认约 **3s**（NSP1 `wd_ms` 可改；0=Host 默认）。超时 → trap / 杀 App 回大厅。

---

## A.5 BLE 数据 — BLE_DATA (20)

Guest **不管**连接、UUID、HCI，只收发数据报。

```c
int api->ble_send(const void *data, int len);           /* ≤180B；成功=len */
int api->ble_recv(void *buf, int cap, int timeout_ms); /* 一包；超时=0 */
```

| 方向 | JSON-RPC | 载荷 |
|------|----------|------|
| 手机 → 设备 | `nanoshellData` | `{ "hex": "…" }` |
| 设备 → 手机 | `reportNanoshellData` | `{ "hex": "…" }` |

| 条件 | 结果 |
|------|------|
| 未连接 | `ble_send` → `-ENOTCONN` |
| `len` 非法 / >180 | `-EINVAL` |
| RX 队列满（4 包） | 新下行丢弃；上层可见 `-ENOBUFS` |

---

## A.6 灯效 — LED (21)

走产品 **通知灯最高优先级**栈（可盖过录音灯）。离开 NS / `led_off` 后交回产品。

```c
int api->led_effect(int mode, int group_mask, uint32_t color, int brightness,
                    int on_ms, int off_ms);
int api->led_off(void);
```

| mode | 宏 | 含义 |
|------|-----|------|
| 0 | `NS_LED_OFF` | 关 |
| 1 | `NS_LED_STEADY` | 常亮 |
| 2 | `NS_LED_BLINK` | 闪烁（`on_ms`/`off_ms`，默认约 200/800） |
| 3 | `NS_LED_BREATH` | 呼吸（直到 `led_off`） |

- `group_mask`：bit0..2 三组 RGB；`0` 或 `0xFF` = 全亮  
- `color`：`0x00RRGGBB`；`brightness`：0..255  
- 返回 0 或 `-errno`

---

## A.7 震动 — HAPTIC (22)

```c
int api->vibe(int pattern);                 /* NS_VIBE_* */
int api->vibe_pulse(int on_ms, int off_ms, int repeat); /* repeat=0 无限 */
int api->vibe_stop(void);
```

| pattern | 宏 | 效果 |
|---------|-----|------|
| 0 | `NS_VIBE_NOTIFY` | 通知双震 ~50-100-50ms |
| 1 | `NS_VIBE_KEY` | 按键短震 ~30ms |

---

## A.8 重点消息 — NOTIFY (23)

**仅当 NanoShell 占屏时** divert：系统不弹通知 UI、不播系统灯/震；标题+正文进 Guest 队列。

```c
int api->notify_recv(void *buf, int cap, int timeout_ms);
/* 线格式: [u8 title_len][title][u8 body_len][body]，总长 ≤200B */
```

| 项 | 值 |
|----|-----|
| 队列深度 | 4；满丢最旧 |
| 超时 | 返回 0 |
| leave / 退出 NS | `notify_flush` 清空 |
| 非占屏 | 仍走产品通知（本 API 无包） |

---

## A.9 Timer — TIMER (25)

| API | 说明 |
|-----|------|
| `timer_create(delay_ms, period_ms)` | → id **1..N**；失败 **0**；N≤**6**（NSP1 `max_timers` 可更小） |
| `timer_cancel(id)` | `NS_OK` / `NS_ERR_NOT_FOUND` |
| `timer_restart(id, interval_ms)` | 重置周期 |

到期经 `event_poll` → `NS_EVT_TIMER`（`handle`=id，`value`=missed_count；同 handle 合并）。

---

## A.10 KV — KV (27) ✅ 已开放

按 **app id** 命名空间落盘（进 App 时 `ns_kv_bind`；退时 flush）。

| 项 | 值 |
|----|-----|
| keys / app | ≤ **8** |
| key / val 长 | 16 / 32 B（含 NUL 策略见 `kv.h`） |
| 写/会话 | ≤ **64**（超限 `NS_ERR_BUSY`） |

| Import | 签名 |
|--------|------|
| `kv_set` | `i(**)` |
| `kv_get` | `i(**i)` → 字节数（不含 NUL）或错误 |
| `kv_remove` | `i(*)` |

先 `ns_cap_has(NS_CAP_KV)`。样例：Snake / Hop / Counter 记最佳分。

---

## B. 生命周期与工具 API（无独立 cap，但端侧提供）

| API | 作用 |
|-----|------|
| `version()` | Host 版本字符串 |
| `heartbeat()` | 喂狗；主循环必调 |
| `should_exit()` | Host 要求退出？ |
| `exit_app()` | Guest 主动退回大厅 |
| `host_info(out)` | 分辨率、max_timers、draw cmds、ui_generation、kv_max_keys… |
| `random_u32()` | RNG |
| `launch_arg_count` / `launch_arg_get` | ≤**4** × **32B**（`Enter`/`LaunchApp` JSON `args`） |
| `event_poll` | 见 A.2；另有 `NS_EVT_STOP`（Host 杀进程） |

装载失败 / 看门狗 / Guest trap：`ns_trap_last()`（串口 `[ns] trap app=… reason=…`）。

---

## C. 壳 / 上位机（非 Guest cap）

### C.1 Wasm 运行时与 Store

| 项 | 限额（`wasm_limits.h`） |
|----|------------------------|
| `app.wasm` | ≤ **12** KiB |
| 线性内存 | ≤ **8** KiB |
| Guest C-stack | **3** KiB（`-z stack-size=3072`，`--stack-first`） |
| Wasm3 FixedHeap | **16** KiB（勿再加大） |
| Store | ≤ **8** App；路径 `/SD:/ns_data/installed/` |
| Builtin | **无**（ROM 预算；均 SD 安装） |

包格式 NSP1（16B 头 + wasm）：flags / max_timers / cap_mask / min_w·h / wd_ms。工具：`pack-nsp` / `pack_nsp.py --nsp1`（**装包门禁**：wasm 体积 + linear footprint，见 [design-notes.md §10](design-notes.md)）。

### C.2 JSON-RPC（Capability **6001**）

| Method | 作用 |
|--------|------|
| `nanoshellEnter` / `Exit` | 进退 NS；Enter 可选 `appId` + `args` |
| `nanoshellListApps` | 列表（含 `version` / `running`） |
| `nanoshellLaunchApp` | 进 NS（如未在）并启动 |
| `nanoshellGetConfig` / `SetConfig` | `bootEnter` / `autoAppId` |
| `nanoshellInstallPackages` | 批量 packages→installed；可选 `{force}` |
| `nanoshellUpdateApp` | `{id, force?}` 单 App 更新；默认拒同版/降级，`force` 允许 |
| `nanoshellUninstallApp` | 卸载 |
| `nanoshellData` | 下行 hex → Guest `ble_recv` |
| `sdPutBegin` / `Chunk` / `Commit` / `Abort` | BLE→SD 写文件 |
| `sdList` | 列目录 |
| `sdDelete` | 删文件或目录 |

上报：`reportNanoshellConfig` / `Apps` / `Data` / **`Update`**；`reportSdPut` / `SdList` / `SdDelete`。  
更新路径：`installed/<id>.stg` 校验 → 停同 id 运行中 App → 替换；非固件 OTA。

**联调：** `nanoshell*` + `sdPut*` + `sdList` / `sdDelete` 免会话鉴权；**量产应收紧**。

### C.3 SD 通道细则

| | Put | List / Delete |
|--|-----|---------------|
| 根路径 | `/SD:/ns_data/packages/`、`inbox/` | 另含 `installed/` |
| 单文件 | ≤ **256** KiB；先写 `.tmp` 再原子提交 | — |
| 组件名 | `[A-Za-z0-9._-]`；禁 `..` / `//` | 同左；**不可删三个根目录本身** |
| 典型用途 | 推 `.nsp` / 投放文件 | 文件工具列目录、删半包/旧包 |

PC：`ainote_client`「推送 .nsp」= `sdPut`×N + **`UpdateApp{force:true}`**；「推文件到SD」= packages/inbox；文件工具 = `sdList`/`sdDelete`。

### C.4 开机自启

`SetConfig { bootEnter, autoAppId }` → 开机约 2.5s 后 `Enter`；成功才武装 NVRAM；干净 leave 清 armed；带着 armed 再开机或 Guest 看门狗 → **清 bootEnter**（防启动砖）。

---

## D. 限额速查

| 项 | 值 |
|----|-----|
| 前台 App | 1 |
| 分辨率 | 240×120 RGB565 |
| Widgets / Timers / Events / Sprites / Draw cmds | 24 / 6 / 16 / 16 / **192**（环见 host-api） |
| `app.wasm` / linear / guest stack / FixedHeap | 12 / 8 / 3 / 16 KiB |
| Store | 8（设备） |
| BLE / Notify 包 | 180 / 200 B |
| Launch args | 4 × 32 B |
| KV | 8×32B |
| 看门狗默认 | 3000 ms |

---

## E. Guest 探测示例

```c
if (api->cap_has(NS_CAP_BLE_DATA)) {
    api->ble_send(buf, len);
}
if (api->cap_has(NS_CAP_LED)) {
    api->led_effect(NS_LED_BLINK, 0xFF, 0x00FF0000, 128, 200, 800);
}
if (api->cap_has(NS_CAP_KV)) {
    api->kv_set("hi", "1");
}
```

---

## F. 相关文档

| 文档 | 内容 |
|------|------|
| [host-api-v1.md](host-api-v1.md) | Import 签名、状态码、NSP1、事件打包 |
| [guest-app-guide.md](guest-app-guide.md) | 怎么写 / 打包 / 样例 |
| [architecture-ainote.md](architecture-ainote.md) | 场景、键桥、RPC 绕过、自启闩锁 |
| [design-notes.md](design-notes.md) | 决策与键码陷阱 |
| `host/include/nanoshell/capability.h` | Cap ID 源 |
| `host/platform/ainote/plat_sys.c` | `cap_has` 实现 |
