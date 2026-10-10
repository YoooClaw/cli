# 设备交互与验收

当需求涉及灯效、振动、通知、BLE、电池、录音、屏幕、计时或存储时，读取项目 `docs/guest-capabilities.md` 的参数与限制；Wasm 调用以 `guest/sdk/ns_guest.h` 为准，文档中的 `api->` 是原生 Host 写法。当前目标设备均配备马达，不需要增加“无马达”设置或验收分支。调用失败仍须处理返回值。

| 需求 | 接口与示例 | 专用场景应验证 |
|---|---|---|
| 短按、长按、蓄力松开 | `event_poll`，参考 `hop.c`；`poll_key` 仅 DOWN | 触发时机、按住/松开、BACK 退出 |
| 得分灯、状态灯、呼吸灯 | `led_effect` / `led_off`，参考 `pulse.c` | 灯组、颜色、亮度、模式、闪烁间隔、关闭 |
| 撞击、到点提醒 | `vibe` / `vibe_pulse` / `vibe_stop`，参考 `quake.c` | 触发条件、时长、间隔、次数、停止 |
| 手机通知 | `notify_recv`，参考 `pulse.c` / `quake.c` | 标题正文解析、长度检查、显示及提醒 |
| 手机联动 | `ble_send` / `ble_recv` | 协议、上下行字节、断连返回值、长度限制 |
| 电量与充电显示 | `sys_battery` / `sys_charge` | 电量更新、未充电/充电中/充满 |
| 倒计时、周期提醒 | `timer_*`、`now_ms`，参考 `chrono.c` | 到点仅触发预期次数，退出取消定时器 |
| 录音控制 | `rec_*`，见下方规则 | 开始、暂停、恢复、停止、时长、文件名、打点、失败处理 |
| 屏幕控制 | `screen_*` | 熄屏、亮屏、亮度 0..255、恢复操作 |
| 分数与偏好 | `kv_*`，参考 `counter.c` | 写入后读取、重新启动；断电持久化留待真机 |

灯色使用 `0x00RRGGBB`，屏幕绘制通常使用 RGB565，不混用。长驻灯效和循环振动在取消、结束与 BACK 退出路径上明确关闭；释放应用创建的定时器。提醒只承诺应用运行期间生效。BLE 需求要定义双方的数据格式与手机发送端，网页模拟器不是真实手机连接。

## 浏览器试玩

自带预览器的“硬件模拟”展示三组灯、灯效模式和马达节拍；振动以高亮提示，不驱动电脑马达。呼吸灯周期仅为近似演示，实际亮度、灯组布局、呼吸节奏及马达力度留待真机确认。

面板支持电量、充电状态、UTF-8 通知及十六进制 BLE 收发；BLE 可切换连接状态。输入通过共享内存传递，应用持续运行时也可接收。通知最大 200 字节（含两个长度字节），BLE 最大 180 字节；队列各 4 项，通知满时丢最旧、BLE 满时拒绝新包。

## 场景自动化

预览页面暴露 `window.nsHardware`：

- `injectNotification(title, body)`、`injectBle(hex)`：注入外部事件。
- `setBattery(percent, charge)`：charge 为 0 未充电、1 充电中、2 充满。
- `setBleConnected(boolean)`：模拟连接状态。
- `sequence`、`calls`：最近 2000 条调用，含递增 sequence、name、args、result；`dropped` 表示被截断的条数。
- `snapshot()`：返回调用记录及灯/马达模拟状态。

在触发业务动作前记录 sequence，按后续记录断言，防止旧调用让新场景误通过。例如通知提醒（标题应符合应用实际触发条件）：

```js
const before = await page.evaluate(() => window.nsHardware.sequence);
await page.evaluate(() => window.nsHardware.injectNotification('任务', '已完成'));
await page.waitForFunction(n => window.nsHardware.calls.some(c =>
  c.sequence > n && c.name === 'vibe_pulse' && c.result === 0 &&
  c.args.on_ms === 60 && c.args.off_ms === 60 && c.args.repeat === 2), before);
```

结合屏幕断言验证业务结果；纯接口调用不能证明文字显示、得分或碰撞正确。退出清理应在 BACK 前取 sequence，再检查应用主动发出的 `led_off`、`vibe_stop`。`host_reset` 是模拟器结束会话后清空视觉效果的独立记录，不能代替应用清理。若场景主动退出，末尾重新启动应用，供通用验收继续执行。

测试报告保存 `hardwareSimulation` 及 `hardwareValidation`。报告中的浏览器模拟结果仅证明调用逻辑，不证明真实灯光、马达、无线链路或断电存储；交付摘要分别列出已断言的应用行为、模拟交互和待真机验收项。旧项目要采用此预览器，按 environment.md 的快照迁移步骤更新并重新验收。

## 新版录音、屏幕与 KV

本快照已开放 Wasm `ns_rec_start/stop/pause/resume`、`ns_rec_get_state/is_starting/elapsed_ms/filename/add_marker`，以及 `ns_screen_*`。新应用使用 `#include "ns_guest.h"` 和 `NS_GUEST_EXPORT void start(void)`；`ns_abi.h` 为兼容入口，不是独立接口定义。构建必须使用随附 pipeline，包含 `guest/sdk` 搜索路径及 `--stack-first`。

录音开始前检查当前状态与启动中状态；只有明确由本应用成功启动的录音才能按本应用退出策略处理。已有的产品录音不应被无关小程序停止。把“退出继续录音还是停止”写进需求与验收；普通临时录音默认退出停止本应用启动的录音。失败时显示状态，不能仅凭调用就宣称开始成功。三击触发可在应用运行期间识别，不能宣称改变设备全局双击规则；退出后的定时录音不由应用循环保证。

`rec_filename` 要提供有界缓冲并检查返回值；网页给出模拟文件名，不生成音频文件。浏览器录音状态和时间仅覆盖状态机，每次应用启动重置模拟状态，不能证明真实设备启动延迟、文件落盘、麦克风采集或跨应用录音共存。

屏幕熄灭时仍保留可恢复操作（例如 OK 亮屏、BACK 返回）；记录原亮度，在需要时恢复。网页以画布亮度模拟亮灭屏，硬件面板仍可见。真机软灭屏不等于释放唤醒锁或进入低功耗，不能承诺因此省电。`screen_*` 需要兼容新版固件。

KV 已重新开放：每应用最多 8 个键，键/值分别最多 15/31 字节（另留 NUL），每会话 set 最多 64 次。按事件写入，避免逐帧写。浏览器按 catalog 的 app id 隔离，在同一页面内重启应用可读回；刷新页面会丢失，不能作为设备断电持久化证明。拖入文件按文件名隔离，仅用于临时试玩。

`window.nsHardware.snapshot()` 还包含 `recorder`（最近状态变更时的 state/elapsed_ms）与 `screen`（on/brightness）。调用日志记录录音控制、文件名、打点、屏幕变更和 KV 操作及返回值。以 sequence 划定每次操作范围，验证暂停时计时不增长、恢复后增长、熄屏后仍可恢复、重开能读回存储；查询类接口由应用使用并通过画面或测试协议断言。
