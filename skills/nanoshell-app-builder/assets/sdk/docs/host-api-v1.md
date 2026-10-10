# NanoShell Host API — 现行 ABI

| 状态 | soft + Ainote LVGL Widget；无场景 BT1 FB；Web Preview；BLE/PC 推包 |
|---|---|
| 日期 | 2026-09-30 |
| 总架构 | [architecture-v2.md](architecture-v2.md) |
| 设计决策 | [design-notes.md](design-notes.md) |
| 抄谁 | [borrow-from-peers.md](borrow-from-peers.md) |

Guest 只发 **handle / draw / timer / event**（无音频）。Widget 与 Draw/大厅均走产品 LVGL Partial（~5–10KB）；**场景路径无 BT1 56KB FB**。设备无喇叭，**不做 PCM / tone**。

**分辨率（全端一致）**：**240×120** RGB565。Win 模拟器 / Web Preview 逻辑缓冲同尺寸（窗口可 2× 放大显示，坐标仍按 240×120）。

Runtime：**Wasm3**（真机）/ 浏览器 WebAssembly（`tools/web-preview`）。Wasm 导入模块必须是 **`ns`**；链接 `guest/wasm/minic.c`，不要产生 `env.*`。  
权威头文件：`host/include/nanoshell/*.h`（C）、`guest/wasm/ns_abi.h`（wasm）。Allowlist：`guest/wasm/allowed_imports.txt`。

---

## 双模式显示

| 模式 | API | 典型 |
|---|---|---|
| Widget | `ui_*` | Record / Settings |
| Draw | `ns_draw_*` / `draw_*` | Snake / Jump / 大厅 |

```text
Widget: ui_* → ui_lvgl → 产品 Partial
Draw/大厅: ns_draw_* → cmd ring ≤192 → DRAW_MAIN → 产品 Partial
（sim: ns_draw_* → ns_gfx → Win32；web: ns.* → Canvas）
```

禁止 Guest / `lv_canvas` 全屏 `240×120×RGB565`。

---

## 状态码（`status.h` / `ns_abi.h`）

| 码 | 名 |
|----|-----|
| 0 | `NS_OK` |
| -1 | `NS_ERR_INVALID` |
| -2 | `NS_ERR_NOT_FOUND` |
| -3 | `NS_ERR_RESOURCE_EXHAUSTED` |
| -4 | `NS_ERR_WOULD_BLOCK` |
| -5 | `NS_ERR_NOT_SUPPORTED` |
| -6 | `NS_ERR_VERSION_MISMATCH` |
| -7 | `NS_ERR_BUSY` |
| -8 | `NS_ERR_IO` |

创建类 API（`ui_*_create` / `timer_create`）：失败仍返回 handle **0**。

---

## UI

| Import | 签名 | 说明 |
|---|---|---|
| `ui_label_create` / `ui_button_create` | `i(iiii)` | x,y,w,h → handle；失败 0 |
| `ui_delete` | `i(i)` | |
| `ui_set_text_id` | `i(ii)` | handle, text_id（Host 字符串表） |
| `ui_set_pos` / `ui_set_size` / `ui_set_visible` | `i(iii)` / `i(iii)` / `i(ii)` | |
| `ui_set_fg` / `ui_set_bg` | `i(ii)` | RGB565 |
| `ui_hit_test` | `i(ii)` | x,y → handle 或 0 |
| `ui_pointer` | `i(iii)` | x,y,pressed；1=CLICK，2=MOVE（合并） |

Cap：`NS_CAP_UI=24`。池 ≤16。示例：`builtin:ui_soft`。

---

## Draw

命令先入环（≤192/帧），`present` 时：Ainote 发布到 `draw_surf`（`DRAW_MAIN` → Partial）；sim flush → gfx。超限丢弃（`ns_draw_frame_stats`）。

| Import | 签名 |
|---|---|
| `draw_clear` / `clear` | `v(i)` color |
| `draw_present` / `present` | `v()` |
| `draw_rect` / `fill_rect` | `v(iiiii)` x,y,w,h,color |
| `draw_round_rect` / `fill_round_rect` | `v(iiiiii)` + radius |
| `draw_circle` / `fill_circle` | `v(iiii)` cx,cy,r,color |
| `draw_line` | `v(iiiii)` |
| `draw_text_id` | `v(iiiii)` id,x,y,color,scale |
| `draw_text_font` | `i(iiiii)` font_id, text_id, x, y, color |
| `text_measure` | `i(ii)` font_id, text_id → packed `w\|(h<<16)` |
| `draw_sprite` | `i(iii)` sprite_id,x,y |
| `asset_info` | `i(i)` → packed kind\|scale\|w\|h |

Sprite id：`NS_SPRITE_BALL/PLAYER/ENEMY/COIN`（1..4）。字体：`NS_FONT_SMALL/MEDIUM/LARGE`（16..18）。

---

## Timer / Event / 键 / KV / Launch

| Import | 签名 |
|---|---|
| `timer_create` | `i(ii)` delay_ms, period_ms → id；失败 0；≤6 |
| `timer_cancel` / `timer_restart` | `i(i)` / `i(ii)` |
| `event_poll` | `i()` 见下方打包；0=空 |
| `poll_key` | `i()` 键码；仅 DOWN；无相位；无 UP/REPEAT |
| `random_u32` | `i()` |
| `kv_set` / `kv_get` / `kv_remove` | ✅ 开放；≤8 keys × 32B；写/会话 ≤64；按 app id |
| `launch_arg_count` / `launch_arg_get` | ≤4 × 32B |
| `now_ms` / `delay_ms` / `heartbeat` / `should_exit` / `exit` | 生命周期 |

### `event_poll` 打包

```text
u32 = type | (handle << 8) | ((value & 0xffff) << 16)
```

KEY：`handle` = `NS_KEY_PHASE_*`，`value` = 键码。详见 [design-notes.md §3–4](design-notes.md)。

### 键码（全端统一）

与 `types.h` / `ns_abi.h` 相同：LEFT=1 RIGHT=2 UP=3 DOWN=4 **OK=5 BACK=6**。

---

## 平台可选（真机）

| Import | 签名 | Cap |
|---|---|---|
| `cap_has` | `i(i)` | — |
| `rec_get_state` / `rec_is_starting` | `i()` | 3 |
| `rec_start` / `stop` / `pause` / `resume` | `i()` | 3 |
| `rec_elapsed_ms` | `i()` | 3 |
| `rec_filename` | `i(*i)` | 3 — 拷进 Guest 缓冲，返回字节数（不含 NUL） |
| `rec_add_marker` | `i(*)` | 3 |
| `screen_is_on` / `screen_on` / `screen_off` | `i()` | 28 |
| `screen_brightness_get` / `set` | `i()` / `i(i)` | 28 — 亮度 0..255；0=软灭 |
| `sys_battery` / `sys_charge` | `i()` | 10 / 11 |
| `ble_send` / `ble_recv` | `i(*i)` / `i(*ii)` | 20 |
| `led_effect` / `led_off` | `i(iiiiii)` / `i()` | 21 |
| `vibe` / `vibe_pulse` / `vibe_stop` | … | 22 |
| `notify_recv` | `i(*ii)` | 23 |

原生 C `api->rec_filename()` 返回 `const char*`；Wasm / `ns_guest` 统一为缓冲拷贝。  
细节：[guest-capabilities.md](guest-capabilities.md)。

---

## NSP1 装包头（16 字节 LE）

```text
0..3  magic 'NSP1'
4..5  flags: bit0 has_ui,1 has_draw,2 needs_timer,3 need_ui,4 reserved,5 need_gfx
6     max_timers (1..6)
7     cap_mask_lo
8..11 wasm_len
12    min_w (0=any, ≤255)
13    min_h
14..15 wd_ms (0 = Host 默认 3000)
+ raw app.wasm
```

`ns_pkg_unwrap` + `ns_pkg_check_requirements`；`pack_nsp.py --nsp1 --need-ui --min-w 240 --wd-ms 3000`。  
Launch args：`nanoshellEnter` / `nanoshellLaunchApp` JSON `args`（≤4×31 字符）。  
Backend：sim=`soft`/`ui_lvgl_stub`；Ainote `ui_root` 后切 `ui_lvgl`。  
Host-tool：`tools/host_tool.py`（`list/enter/exit/install/push/…`）。  
推包：`nanoshellPushBegin` → `PushChunk` → `PushCommit`（BLE/PC；见 host_tool `push`）。

---

## 限额

| 项 | 值 |
|---|---|
| 前台 App | 1 |
| 逻辑分辨率 | **240×120** |
| Widgets / Timers / Events / Sprites / Draw cmds | 24 / 6 / 16 / 16 / **192** |
| `app.wasm` / linear / guest stack / fixed heap | **12** / **8** / **3** / **16** KiB（`wasm_limits.h`） |
| Store apps（设备 / Sim） | 8 / 32 |
| KV | 8 keys × 32B，写/会话 64 |
| Launch args | 4 × 32B |
| BLE datagram / Notify pkt | 180 / 200 B |

装载阶段失败与 Guest trap：`ns_trap_last()`（`trap.h`）；看门狗同记 `NS_TRAP_WATCHDOG`。  
进/出：`on_init` / `start` / `on_destroy`（wasm 可选）→ `ns_session_end()`（trap 保留到下次 `begin`）。  
事件：STOP/TIMER 必达并合并 missed；UI MOVE 合并；KEY REPEAT 合并。
