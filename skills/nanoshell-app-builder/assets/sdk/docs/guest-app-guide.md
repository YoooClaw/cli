> **便携 Wasm 快照说明：** 下文保留上游指南，包含原生 builtin、固件接线和完整仓库的 Windows 构建流程；这些不属于本 skill 的交付路径。使用 skill 的 `scripts/pipeline.py init/build/serve/release` 与 `scripts/test_app.mjs`，不要执行本快照未附带的批处理或修改固件。最小 Wasm 模板见 `guest/wasm/hello_tiny.c`；下文 `api->` 示例为原生接口，Wasm 应包含 `ns_guest.h` 并调用 `ns_*`。

# NanoShell Guest App 开发指南

给人类或 AI Agent（龙虾 / workbuddy / Cursor 等）写内置 App 用。
仓库根：`c:\work\nanoshell`（设备侧由 `sdk-6095/application/ai_note` 链接进来）。

## 一句话

**一份源**：`guest/wasm/<name>.c` + `#include "ns_guest.h"` → 同时打成 `.nsp`（Wasm）和固件 builtin（`-DNS_GUEST_NATIVE`）。

## 约束（必守）

| 项 | 值 |
|----|-----|
| 屏 | **240×120** RGB565（模拟器 / 网页 / 真机同一逻辑分辨率；窗口可 2× 放大显示） |
| 入口（推荐） | `NS_GUEST_EXPORT void start(void)` + `NS_GUEST_BUILTIN(name)` |
| 头文件（推荐） | `#include "ns_guest.h"`（`guest/sdk`，`-I guest/sdk`） |
| 堆 | **极紧**，少 malloc；静态缓冲优先 |
| Store | 设备 `NS_STORE_MAX_APPS=8`，加 builtin 前先数现有项 |
| 循环 | 每帧 `heartbeat()`；退出查 `should_exit()`；BACK 调 `exit_app()` |
| 状态码 | 操作用 `NS_OK` / `NS_ERR_*`（创建类 API 仍以 handle `0` 表示失败） |
| wasm 生命周期 | 可选导出 `on_init` / `on_destroy`（Host 在 `start` 前后调用） |
| 帧间隔 | `delay_ms(16)`～`33` |
| IP | 不要抄任天堂等版权素材；可做原创同类型玩法 |

## 设备按键映射（AiNote）

NS 在屏时（`key_bridge.c`）：**只映射为 `NS_KEY_*`**，不直接退产品场景。

| 物理操作 | 大厅 | App 内 |
|----------|------|--------|
| 语音键 **按下** SHORT_DOWN | `RIGHT` + **DOWN**（下一个） | `OK` + **DOWN** |
| 语音键 **长按** LONG_DOWN / HOLD | `OK` + **DOWN**（启动） | `OK` + **REPEAT**（游戏长按） |
| 语音键 **抬起** | 短点=`RIGHT` UP；长按=`OK` UP | `OK` **UP** |
| 电源短按 SHORT_UP | `BACK` DOWN → 回产品 | `BACK` DOWN → `exit_app()` |
| 电源长按 | 不消费（产品关机） | 同左 |

`poll_key()` 给出 **DOWN**；`event_poll()` 的 KEY 事件 `handle` 为 `NS_KEY_PHASE_*`（含 UP / REPEAT）。
单击类 App 用 `poll_key`；蓄力 / 连发必须看 `event_poll` 相位。

进入 NS：RPC `nanoshellEnter`（可选 `appId` + `args` ≤4×32B）。退出产品：大厅再按一次电源。

模拟器：方向键 / Enter=OK / Esc=BACK；keydown=DOWN、按住=REPEAT、keyup=UP。

App 内应处理：`OK`、`BACK`；蓄力/连发用 `event_poll` 看 `REPEAT`/`UP`。无喇叭，不要做播放 API。

## 最小 App 模板

```c
#include "nanoshell/api.h"
#include <stdio.h>

void guest_app_foo(const nanoshell_api_t *api)
{
	ns_gfx_info_t info;
	int compact;

	api->gfx_get_info(&info);
	compact = (info.height <= 140);
	printf("[foo] start\n");

	while (!api->should_exit()) {
		api->heartbeat();

		api->gfx_clear(NS_RGB565(18, 22, 28));
		api->gfx_draw_text(8, 4, "FOO", NS_RGB565(240, 240, 240),
				   compact ? 1 : 2);
		api->gfx_draw_text(8, info.height - 16, "OK act  PWR back",
				   NS_RGB565(120, 130, 140), 1);
		api->gfx_present();

		{
			int key = api->poll_key();
			if (key == NS_KEY_OK) {
				/* primary action */
			} else if (key == NS_KEY_RIGHT) {
				/* secondary */
			} else if (key == NS_KEY_BACK) {
				api->exit_app();
				break;
			}
		}
		api->delay_ms(33);
	}
	printf("[foo] exit\n");
}
```

参考实现：`guest/wasm/chrono.c` / `snake.c` / `hop.c`。

## 接线清单（新双后端 App）

假设 App 名 `foo`，id `com.example.foo`：

1. **新建** `guest/wasm/foo.c`（`ns_guest.h`，`start` + `NS_GUEST_BUILTIN(foo)`）
2. **设备** CMake：`guest/wasm/foo.c`（全局已有 `NS_GUEST_NATIVE=1` + `ns_guest_native.c`）
3. **模拟器**（可选）`tools/build-sim.bat` `SRCS` 加上同一 `.c`
4. **注册** builtin：`store_builtin.c` / `store_zephyr.c` 的 `add_builtin(..., guest_app_foo)`
5. **包**：`tools/pack-nsp.bat foo` → `dist/foo.nsp/`
6. **README** 表格加一行

`add_builtin` 示例：

```c
add_builtin("com.example.foo", "Foo", "builtin:foo", guest_app_foo);
```

## API 速查

```text
api->version / exit_app / should_exit / heartbeat
api->now_ms / delay_ms
api->poll_key                    → NS_KEY_*
api->gfx_get_info / clear / present
api->gfx_fill_rect / draw_rect / draw_line
api->gfx_fill_circle / fill_round_rect
api->gfx_draw_text(x,y,str,color,scale)   scale=1→约 6x8
api->gfx_blit(...)
api->rec_*                       录音桥（Wasm：`ns_rec_*`；一般 App 不用）
api->cap_has / sys_battery_percent / sys_charge_state
api->screen_is_on / screen_on / screen_off / screen_brightness_*
api->ble_send / ble_recv
api->led_effect / led_off
api->vibe / vibe_pulse / vibe_stop
api->notify_recv                 NS 占屏时重点消息
```

颜色：`NS_RGB565(r,g,b)`。文字无中文点阵（ASCII）。

系统 / 录音 / 蓝牙 / 灯效 / 震动 / 重点消息见 [guest-capabilities.md](guest-capabilities.md)。  
录音 Wasm：`ns_rec_filename(buf, cap)`（缓冲拷贝）。KV / 圆 / `ui_set_size` 真机与 Wasm 均可用。

## Wasm / 双后端 Guest（推荐）

玩法小 App 写 `guest/wasm/<name>.c`（`#include "ns_guest.h"`），**不要** `#include <string.h>` / 链 libc。  
- Wasm：`pack-nsp` / web-preview（工具链入 `minic.c`）；导入只允许 `ns.*`（`allowed_imports.txt`）。  
- Native：`NS_GUEST_NATIVE=1` 时 `NS_GUEST_BUILTIN(name)` 生成 `guest_app_<name>`。

设备预算（单源 `host/include/nanoshell/wasm_limits.h`，`pack_nsp.py` 同步）：

| 项 | 上限 |
|----|------|
| `app.wasm` | ≤ **12288** B |
| Guest C-stack（`-z stack-size` + `--stack-first`） | **3072** B（落在 8 KiB 线性内存低地址；勿把 .data 链到高偏移） |
| Host 线性内存 | **8192** B（Wasm3 侧缓冲；须 ≥ stack + `.data`） |
| FixedHeap | **16** KiB（固件；勿再加大） |

`pack-nsp` **装包门禁**（不过真机也会挂）：`app.wasm` 超 12 KiB、或 linear `need` 超 8 KiB → 直接 `ERROR` 退出。  
**拦不住**的：Widget/Timer 个数、FixedHeap 撑爆、运行时栈溢出、看门狗 —— 见 **[design-notes.md §10 异常点统计](design-notes.md)**。

失败诊断：串口 `[ns] trap app=… reason=…`；Host 可查 `ns_trap_last()`。

| 样例 | 源码 | 说明 |
|------|------|------|
| Hop | `guest/wasm/hop.c` | 蓄力跳；OK DOWN/UP |
| Snake | `guest/wasm/snake.c` | 转向；死后 OK 重开；KV 记最佳 |
| Counter | `guest/wasm/counter.c` | +1/−1；KV；UI stub + Draw HUD |
| Hello | `guest/wasm/hello_tiny.c` | 最小循环 |

```bat
rem 网页自测（不强制 12KiB）
python tools\build-web-wasm.py hop
cd tools\web-preview && serve.bat

rem 真机 SD 包（必须 ≤12288）
tools\setup-env.bat
tools\pack-nsp.bat hop
```

Web Preview 操作说明：[tools/web-preview/README.md](../tools/web-preview/README.md)。

## 构建与交付（给人 / 外包）

**一键（Win）**：仓库根目录 `build.bat` → 模拟器 + 网页 wasm + `dist\*.nsp`。  
**最终要装到真机：** `tools/pack-nsp` → `dist/<name>.nsp/`（SD 安装）。  
合入固件的 builtin 用 `pack-device-app`。详见 [contrib-build.md](contrib-build.md)、[tools/README-TOOLS.md](../tools/README-TOOLS.md)。

```bat
tools\setup-env.bat
build.bat
rem 或单独: tools\pack-nsp.bat hop
```


## 给 Agent 的任务写法（复制即用）

```text
按 docs/guest-app-guide.md 与 docs/contrib-build.md 新增 builtin Guest App：
- 名称：<Name>
- 玩法：<一句话>
- 适配 240x120；`guest/wasm/<name>.c` + `ns_guest.h`
- store/CMake 接线（`NS_GUEST_BUILTIN`）
- 跑通 `tools\setup-env.bat && build.bat`（自测）
- `tools\pack-nsp.bat` / `pack-device-app.bat` 交付
- 不要抬 HEAP；不要新依赖
```

## 常见坑

完整异常点（装包硬拦 / 真机仍炸 / 统计）→ **[design-notes.md §10](design-notes.md)**。

- 大厅只显示 3 行，多 App 会滚动；设备最多约 8 个 store 槽（含 WASM）
- App 模式壳**不会**从队列偷键；必须自己 `poll_key` / `event_poll`
- 短按用 **SHORT_DOWN** 映射，不要依赖 SHORT_UP（产品层有 ~350ms 多击延迟）
- `heartbeat()` 每帧都要，否则壳可能当卡死杀掉（`NS_TRAP_WATCHDOG`）
- wasm 若出现 `env.memmove` 等导入：缺 `minic.c` 或误链了 libc；Web Preview 会报 `Import … "env"`
- 蓄力 / 抬起键：用 `event_poll` 看 `NS_KEY_PHASE_UP` / `REPEAT`；`poll_key` 只吐 DOWN
- **键码**：全端统一 `types.h` / `ns_abi.h`（OK=5 BACK=6 LEFT=1 RIGHT=2）
- 分辨率必须按 **240×120** 布局；不要为模拟器单独做大屏 UI
- 真机 `data segment out of bounds`：线性超 8 KiB；先看 `pack-nsp` 的 `linear footprint` 行
- **勿**抬 FixedHeap / 产品 HEAP；装载缓冲勿放回主 SRAM（用 BT1）

决策与反例总表：[design-notes.md](design-notes.md)。ABI 明细：[host-api-v1.md](host-api-v1.md)。
