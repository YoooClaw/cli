# NanoShell 设计备忘（决策 / 反例 / 陷阱）

| 项 | 内容 |
|---|---|
| 用途 | 只记 **为什么** 和 **不要做什么**；实现细节以头文件 / 代码为准 |
| 架构 | [architecture-v2.md](architecture-v2.md) · [architecture-ainote.md](architecture-ainote.md) |
| ABI | [host-api-v1.md](host-api-v1.md) |
| 日期 | 2026-09-30（§10 异常点统计 2026-10-09 更新） |

---

## 1. 产品边界（铁律）

| 决策 | 理由 |
|------|------|
| Guest **不**碰硬件 / LVGL / 全屏 FB | RAM ~100KB；与产品 UI 共存 |
| 同时 **一个** 前台 App | 无多任务调度；壳看门狗可杀 |
| 逻辑分辨率 **240×120** 全端一致 | 避免「模拟器能跑、真机布局崩」 |
| **无喇叭** | 硬件无扬声器；**不做** PCM / tone API |
| Wasm 导入只允许 **`ns.*`** | 可审计；禁止 libc / `env.memmove` |
| 设备 `app.wasm` ≤ **12 KiB** | SD 装载缓冲；线性内存 Host 封顶 **8 KiB**（无整页 64KB）；guest `-z stack-size=3072` |
| Wasm3 FixedHeap **16 KiB** | 再大（如 24KB）会撑爆产品 SRAM；预算见 `wasm_limits.h` |
| 预算单源 | `wasm_limits.h` ↔ CMake `d_m3*` ↔ `pack_nsp.py`；偏离用 `#error` |
| trap 保留到下次 begin | `ns_trap_last()`；看门狗 / 装载阶段 / Guest 统一 `ns_trap_record` |

反例：Guest `lv_canvas` 再开 240×120×2、链 WASI libc、在模拟器用 320×240 调 UI。

---

## 2. 双模式显示

```text
普通 App → ui_*（Host Widget）→ Ainote ui_lvgl → 产品 Partial
游戏 / 大厅 → ns_draw_*（命令环 ≤192）→ draw_lvgl DRAW_MAIN → 产品 Partial
Sim                    → soft UI / soft Draw → Win32 DIB（逻辑仍 240×120，窗口 2×）
Web                    → Worker 实现 ns.* → OffscreenCanvas 240×120（CSS 2×）
```

| 决策 | 理由 |
|------|------|
| Draw 用 **命令环** 而非 Guest FB | 省 ~56KB BT1；与 LVGL Partial 同路刷屏 |
| 迁离 `lv_image` 全屏贴图 | 曾与 BLE ring 抢 BT1；已退役场景路径 |
| Widget dirty / generation | 少重绘；`ui_generation` 给 Guest 探测重建 |
| Draw 每帧上限 | **192** cmds（`NS_DRAW_MAX_CMDS_PER_FRAME`；旧文 96 已废） |

---

## 3. 按键：相位 vs 键码（重要）

### 3.1 相位（全端统一）

| 常量 | 值 | 谁用 |
|------|----|------|
| `NS_KEY_PHASE_DOWN` | 1 | `event_poll` KEY 的 `handle` |
| `NS_KEY_PHASE_UP` | 2 | 仅 `event_poll` |
| `NS_KEY_PHASE_REPEAT` | 3 | 仅 `event_poll`（长按游戏） |

- `poll_key()`：**只**返回 **DOWN** 的键码（不带相位）；**不吐 UP / REPEAT**
- 长按连发、蓄力松手：必须 `event_poll`（`handle` = `NS_KEY_PHASE_*`）
- **勿**把相位和 `NS_KEY_DOWN`（方向「下」）搞混——方向键只在 **C builtin / `types.h`** 里存在

### 3.2 键码（全端统一 = `types.h`）

| 键 | 值 |
|----|----|
| LEFT / RIGHT / UP / DOWN | 1 / 2 / 3 / 4 |
| OK / BACK | **5 / 6** |
| INSTALL / UNINSTALL | 7 / 8 |

`guest/wasm/ns_abi.h`、Web Preview、`key_bridge`、C builtin **同一套**。勿再使用旧 wasm 表（OK=1）。

AiNote 物理映射见 [architecture-ainote.md §7](architecture-ainote.md) / [guest-app-guide.md](guest-app-guide.md)。

---

## 4. 事件信封（有界、可合并）

打包（wasm `event_poll` 返回的 i32）：

```text
type | (handle << 8) | ((value & 0xffff) << 16)
```

| type | handle | value |
|------|--------|-------|
| KEY=1 | 相位 | `NS_KEY_*` |
| TIMER=2 | timer id | missed_count |
| UI=3 | widget | CLICK=1 / MOVE=2 |
| STOP=4 | 0 | 0 |

策略：STOP/TIMER **必达**；同 handle TIMER 合并 missed；UI MOVE 合并；KEY REPEAT 合并。队列 ≤16。  
Draw 命令环每帧 ≤ **192**（见 `draw.h`；与事件队列 16 分开计）。

---

## 5. 生命周期与看门狗

```text
装载 → (wasm) on_init? → start / guest_app_* → on_destroy?
         ↑ heartbeat 每帧          ↑ 超时 NS_WATCHDOG_MS（默认 3s，NSP1 wd_ms 可改）
session_begin / ns_session_reset 清 UI·Timer·Event·Draw
```

| 决策 | 理由 |
|------|------|
| Guest 线程优先级 **低于** 壳 | 忙等也不能饿死看门狗 |
| 看门狗清 `bootEnter` | 防开机自启死循环 |
| 干净 `exit_app` **不清** bootEnter | 下次仍可自启 |

---

## 6. 包与 Store

| 决策 | 理由 |
|------|------|
| NSP1 16B 薄头 | 只要 timers / caps / min 分辨率 / wd；不要完整 Manifest |
| 设备 store ≤ **8** | SRAM；C builtin 占满常用槽 → Hop/Snake/Counter 走 **SD wasm** |
| Sim store ≤ 32 + 直接装 `dist/*.nsp` | 开发体验；不再同步到 `packages/` |
| `minic.c` 链进 wasm | 消灭 `env.memcpy/memmove`，真机 Wasm3 无 env |

反例：把 Hop 再做成固件 builtin 却不腾槽；wasm 链 wasi-libc。

---

## 7. 预览 Host 选型

| 候选 | 结论 |
|------|------|
| Emscripten + LVGL web | **不做**（重、与 Partial 路径不一致） |
| 自研 `tools/web-preview` Worker | **采用**：只 mock `ns` 导入 + Canvas |

同构目标：同一 `guest/wasm/*.c` → Web / SD /（将来映射修好后）真机 Wasm3。

---

## 8. 从 peer 抄 / 不抄

详见 [borrow-from-peers.md](borrow-from-peers.md)。摘要：

- **抄**：配额单源（`wasm_limits`）、session begin/end、trap 诊断、事件信封、Draw 命令限额、NSP1、host-tool 形状  
- **不抄**：多 App 线程、Guest LVGL、整棵 WAMR/AOT/BundleFS、音频播放、Mode7  

---

## 9. 文档分工（防双份漂移）

| 写这里 | 不写这里 |
|--------|----------|
| 约束、限额、打包约定、决策理由 | 函数体内控制流 |
| ABI 签名表 / 状态码 | 随改的局部算法 |
| 端侧时序 / 闩锁 | 第三方 wasm3 手册 |
| **异常点统计（§10）** | 产品 UI / 非 NS 固件体积 |

代码源：`host/include/nanoshell/*.h`、`guest/wasm/ns_abi.h`、`allowed_imports.txt`、`wasm_limits.h`、`tools/pack_nsp.py`。

---

## 10. 异常点统计（装包 / 真机 / 文档漂移）

> **权威表在本文 §10。** Guest 写法摘要见 [guest-app-guide.md §常见坑](guest-app-guide.md)；限额表见 [guest-capabilities.md §D](guest-capabilities.md) / [host-api-v1.md](host-api-v1.md)；端侧内存见 [architecture-ainote.md §10](architecture-ainote.md)。  
> 预算单源：`host/include/nanoshell/wasm_limits.h`（**12 / 8 / 3 / 16** KiB = wasm / linear / guest-stack / FixedHeap）。

### 10.1 预算与现象（现行）

| ID | 异常 / 约束 | 典型现象 | 装包门禁 `pack_nsp` | 真机仍可能炸 | 处置 |
|----|-------------|----------|---------------------|--------------|------|
| A1 | `app.wasm` > **12 KiB** | `MODULE_TOO_LARGE` / 装载拒 | **ERROR 退出** | 有（若绕过 pack） | 减代码、禁 libc |
| A2 | 线性 `max(data_end, stack)` > **8 KiB** | `data segment out of bounds` | **ERROR 退出** | 有 | 减全局 / 勿加大 stack |
| A3 | Guest C-stack **3 KiB**（`--stack-first`） | 栈与 `.data` 争线性低端 | 计入 A2 | 运行时栈溢出难静态查全 | 少递归 / 大局部数组 |
| A4 | FixedHeap **16 KiB**（勿 24） | parse/runtime/code page 失败 `NS_TRAP_RUNTIME` | 否 | **是**（模块复杂度） | 简化 wasm；**禁止**抬 FixedHeap |
| A5 | 装载缓冲放主 SRAM `.bss` | hp4570n **noinit 溢出** ~6 KiB | 否（固件链接） | 链阶段 | 装载缓冲在 **BT1 heap**（已改） |
| A6 | 抬产品 `HEAP_MEM_POOL` 给 NS | 产品堆被掏空 | 否 | 是 | **禁止**；用 BT1 / 既有池 |
| A7 | Widget / Timer 超配额 | create 失败 / 行为异常 | 否（运行时才知） | **是** | ≤24 widget / ≤6 timer |
| A8 | 看门狗未 `heartbeat` | `NS_TRAP_WATCHDOG`；清 `bootEnter` | 否 | **是** | 每帧喂狗；默认 ~3s |
| A9 | 非法 `env.*` 导入（libc） | Link 失败 / Preview `Import env` | 链 `minic.c` 规避 | 是 | 勿 `#include <string.h>` |
| A10 | BT1 `soc_bt1_ram_alloc` 失败 | `[wasm] BT1 alloc … failed` | 否 | **是**（会话池争用） | 勿占场景 56KB FB；查 BT1 占用 |
| A11 | NSP1 requirements 不符 | `NS_TRAP_REQUIREMENTS` | 部分（头字段） | 是 | `min_w/h` / caps / timers |
| A12 | 键相位误用 | 松手/长按无响应 | 否 | 逻辑错 | `poll_key` 仅 DOWN；UP/REPEAT 用 `event_poll` |
| A13 | 分辨率按模拟器大屏布 | 真机布局崩 | 否 | UX | 一律 **240×120** |
| A14 | 文档限额过时（曾写 8/4/2） | 开发按错预算 | — | 误导 | 以 `wasm_limits.h` / 本表为准 |

### 10.2 装包门禁 vs 真机（对照）

| 类别 | 数量（上表） | 说明 |
|------|-------------|------|
| `pack-nsp` **硬拦** | **2**（A1、A2） | 像 APK 体积检查；红则 `exit != 0`，不合格包 |
| 仅固件/链接约束 | **2**（A5、A6） | 改 Host 布局时炸，不靠 pack |
| **装包拦不住**、要真机/模拟 | **7**（A3 运行时栈、A4、A7、A8、A9 漏网、A10、A11） | FixedHeap / widget·timer / 看门狗 / BT1 |
| 逻辑 / 文档类 | **3**（A12–A14） | 不表现为 trap，但高频踩坑 |

装载阶段 trap 枚举（`trap.h`）：`BAD_PACKAGE` / `REQUIREMENTS` / `MODULE_TOO_LARGE` / `ENV` / `RUNTIME` / `PARSE` / `LOAD` / `LINK` / `NO_START` / `GUEST` / `WATCHDOG`（**11** 种有效原因 + `NONE`）。

### 10.3 日常用法

1. 改完 Guest → 必跑 `tools/pack-nsp.bat <name>`（或 `build.bat`）  
2. 成功应见：`app.wasm = … (device max 12288…)` 与 `linear footprint: … need=… / limit=8192`  
3. 红了先减代码或全局变量，再上设备  
4. Widget/Timer/看门狗/FixedHeap 仍需真机或 Sim 跑一轮  

### 10.4 文档同步清单（防再漂）

| 文档 | 应写预算 |
|------|----------|
| **本文 §10 / §1** | 12 / 8 / 3 / 16 + 异常表 |
| `guest-capabilities.md` §C.1 与 §D | 必须与 `wasm_limits.h` 一致（勿再写 8/4/2） |
| `guest-app-guide.md` 预算表 + 常见坑 | 指回本文 §10 |
| `host/platform/ainote/README.md` Memory | 线性 **8** KiB（非 4） |
| `architecture-ainote.md` §10 | FixedHeap 16 + linear 8；装载缓冲 **BT1** |
