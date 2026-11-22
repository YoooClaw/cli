# 应用消息通知包名映射移植（CLI 最小落库方案）

> 状态：已实现（待评审/合并）  
> 方案日期：2026-11-20  
> 实现日期：2026-11-22  
> 上游：`openclaw-plugin`（`@yoooclaw/phone-notifications` 1.20.5）  
> 下游：`YoooClaw/cli`（0.18.1）  
> 数据快照：2026-11-22 生产接口拉取（19,776 行 → 18,410 条）  
> 范围：**只对发布上线后的新通知**，在落库时把 `appDisplayName` 从包名换成应用显示名。  
> 非目标：历史数据不回填、不迁移；查询/聚合/同步/Assets/Summary Job/transfer 逻辑不改。

---

## 0. 摘要

- **目标**：发布后的新通知落库时写入“包名 → 应用名”的映射结果，让现有查询/统计/摘要/Assets/同步自动显示友好名。
- **唯一运行时代码改动**：`internal/notif/storage.go` 的 `Ingest` 里一行，把 `appDisplayName` 的兜底值从 `appName` 改为 `ResolveAppDisplayName(appName)`。
- **新增**：`internal/notif/appname.go` + `internal/notif/app-name-map.data.json`（`go:embed`，随二进制发布）。
- **不改**：历史文件、查询/聚合/过滤/展示期重解析、Assets、Summary Job、Sync、Transfer、yclib、config、output。
- **接受的代价**：过渡期新旧行混显（旧行包名、新行应用名），统计会短暂拆桶；映射在落库时固化，后续改名不会自动纠正历史新行。
- **最大风险**：快照过期（新 App 仍显示包名）和映射错误被永久落库，必须靠发版前刷新 + 单测守住。
- **实现状态**：已按本方案落地（2026-11-22）；`internal/notif` 与 `internal/summaryjob` 的 `go test -race` 通过；`internal/assets`、`yclib` 因本机模块缓存缺少依赖未执行。
- **数据快照**：2026-11-22 通过生产接口刷新，19,776 行归一化为 18,410 条；插件仓旧快照为 12,561 条（2026-08-22）。

---

## 需求记录（用户输入）

> 本节保留需求方原始输入，作为方案约束来源；未做润色。方案取舍以最后一轮口径 **R4** 为准。

| # | 用户输入（原文） | 提炼要求 | 对方案的影响 |
|---|---|---|---|
| R1 | “openclaw-plugin 里的应用消息通知包名映射的逻辑现在需要移植一份到 /Users/liuxuxu/workspace/cli。分析移植方案及存在的风险” | 把插件的“包名 → 应用名”映射移植到 CLI；先分析方案与风险 | 确定移植对象、数据来源、集成点、风险清单 |
| R2 | “cli 目前区分语言了吗？” | 确认 CLI 是否具备语言/locale 能力，以决定 zh/en 选择逻辑 | 结论：CLI 无通用 locale；本次固定中文，不引入 i18n 体系 |
| R3 | “主要是移植后历史数据怎么处理 会出现展示信息不一致的问题” | 关注历史数据与新数据的展示一致性 | 分析过读时解析/回填方案；但被后续口径收窄 |
| R4 | “现在的要求是 历史数据不管 只是针对发布上线后的新数据解决这个字段映射问题 不改动其他逻辑” | **最终口径**：历史数据不处理；只解决发布后新数据的字段映射；其他逻辑零改动 | 方案收窄为 `Ingest` 一行 + 内置快照；不做读时解析、回填、过滤、Assets 改造 |
| R5 | “输出一个完整上下文文档” | 产出可交接的完整方案文档 | 本文档 |
| R6 | “文档把我输入的要求也总结进去” | 文档中保留需求输入 | 本节 |

### 需求口径演进

1. **初始**：完整移植插件逻辑（含展示期重解析、过滤、历史数据修正），并评估风险。
2. **澄清**：CLI 没有通用语言/locale 体系，不能照搬插件的 `Accept-Language`；语言选择需要单独定。
3. **关注点**：历史数据与新数据的展示一致性，担心同一 App 出现包名和应用名两种展示。
4. **最终收窄（R4）**：历史数据不管；只针对发布上线后的新数据解决字段映射；不改动其他逻辑。
5. **由此确定**：接受过渡期混显；不做读时解析、不做回填、不改查询/聚合/过滤/Assets/Sync/Transfer；只改落库一行。

### 最终验收口径（以 R4 为准）

- 发布后新通知：`appDisplayName` 写入映射到的应用名；
- 发布前历史通知：不改、不迁移、不保证与新数据展示一致；
- 查询、聚合、过滤、Assets、Summary Job、Sync、Transfer：不修改；
- 不引入语言配置、网络刷新、后台任务、数据迁移工具；
- 唯一运行时代码改动点：`internal/notif/storage.go` 的 `Ingest` 落库兜底。

---

## 1. 背景

手机通知上报到 CLI 时，`app` 字段是包名（Android package name 或 iOS bundle id），例如：

```text
com.tencent.xin
com.tencent.mm
com.ss.android.lark
```

插件（openclaw-plugin）已经有“包名 → 应用显示名”的映射，并把它作为内置快照随代码分发：

```text
com.tencent.xin -> { zh: "微信", en: "WeChat" }
com.ss.android.lark -> { zh: "飞书" }
```

CLI 目前没有这份映射。它落库时直接写：

```go
// internal/notif/storage.go，Ingest 内
entry := s.buildStored(n, clientLabel)
if entry.AppDisplayName == "" {
    entry.AppDisplayName = entry.AppName // 包名
}
```

因此 CLI 的通知查询、统计、摘要、Assets、记忆同步里，应用名都是包名。  
本次目标：**发布后的新通知**在落库时写入映射到的应用名，让所有既有消费方自动显示友好名；历史数据保持原样。

---

## 2. 范围与非目标

### 2.1 本次要做

- 新增内置映射快照与加载器；
- 在 `Ingest` 落库时解析 `appDisplayName`；
- 补充单测与发布检查；
- 明确映射数据来源和刷新流程。

### 2.2 明确不做

| 不做 | 原因 |
|---|---|
| 历史数据回填/重写 JSONL | 需求明确“历史数据不管”；回填有并发、格式、回滚风险 |
| 查询/展示时重新解析（`Prefer`） | 属于“其他逻辑”，本次不改 |
| `AppLabel`/`MatchesAppFilter` 改造 | 现有逻辑已经会读 `AppDisplayName`，新数据自动生效 |
| Assets/Summary Job/Sync/Transfer 改造 | 同上；新数据字段已带值 |
| 定时刷新、服务端请求、本地缓存 | 插件已改为“发版内置快照”，CLI 保持无网络、无后台任务 |
| CLI 通用 i18n / locale 体系 | 本次固定中文显示名，不引入语言框架 |
| 数据迁移工具/backfill 命令 | 非本次范围；未来需要时单独设计 |

---

## 3. 现状分析

### 3.1 插件侧逻辑

| 能力 | 位置 | 语义 |
|---|---|---|
| 快照加载 | `src/notification/app-name-map.ts` | 首次查询同步读盘，进程内常驻；支持 `APP_NAME_MAP_DATA_FILE` 覆盖；损坏/缺失时退回包名 |
| 语言选择 | `pickDisplayName`（30 行） | zh：`zh → en → 包名`；en：`en → zh → 包名` |
| 查询 | `lookupAppDisplayName`（116 行） | 命中返回显示名，未命中 `undefined` |
| 回退 | `resolveAppDisplayName`（127 行） | 未命中返回包名 |
| 展示期优先 | `preferAppDisplayName`（138 行） | 快照优先于落库值，用于修正历史数据 |
| 落库 | `src/notification/storage.ts` 304-312 | `appDisplayName = resolveDisplayName(appName)`，失败回退包名 |
| 过滤 | `src/cli/helpers.ts` 163-175 | 候选 = `appName` + `appDisplayName` + 当前快照名 + 5 组别名 |
| 刷新 | `scripts/sync-app-name-map.ts` | 发版前手动拉取并写回 `app-name-map.data.json`；运行时无刷新 |

插件的关键设计：**落库值只是快照，展示时会用当前映射重新解析**（见 `src/notification/APP_DISPLAYNAME_UPDATE.md`）。本次 CLI 移植**只取落库部分**，不取展示期重解析。

### 3.2 CLI 侧现状

| 位置 | 现状 |
|---|---|
| `internal/notif/types.go` 10-13 | `AppName` 包名；`AppDisplayName` 显示名（可空） |
| `internal/notif/storage.go` 369-390 | `buildStored` 只设置 `AppName`，不设置显示名 |
| `internal/notif/storage.go` 189-191 | `Ingest` 把 `AppDisplayName` 兜底为 `AppName` |
| `internal/notif/query.go` 280-299 | `--app` 过滤候选已包含 `AppDisplayName` |
| `internal/notif/aggregate.go` 44 | `AppLabel` 优先 `AppDisplayName` |
| `internal/notif/sync.go` 190/254 | `NextSync`/`FetchSync` 直接返回落库结构 |
| `internal/summaryjob/summaryjob.go` 107/161/171 | 摘要使用 `AppDisplayName` |
| `internal/assets/project.go` 92-96 | Assets 优先 `appDisplayName` |
| `internal/transfer/package.go` 103/247 | 导出包含 `appDisplayName`；记录身份哈希排除该字段 |
| `internal/notif/storage.go` 455 | 内容去重 key 不含 `appDisplayName` |

结论：**所有既有消费方都已经读取并使用 `AppDisplayName`**（输出、聚合、过滤、资产投影、摘要、同步、导出）。因此只要新数据的这个字段有值，查询、统计、摘要、Assets、同步、导出、`yclib` 都会自动显示友好名，不需要改其他代码。这是本次“只改一处”能成立的原因。

### 3.3 当前快照数据事实

CLI 当前内置快照来自 2026-11-22 生产接口 `POST /api/application-config/app-package/config-all` 的实时拉取，按插件 `scripts/sync-app-name-map.ts` 的规则归一化（去空、去等于包名、`zh == en` 只留 `zh`、按 key 排序）：

| 指标 | 值 |
|---|---|
| 接口原始行数 | 19,776 |
| 归一化条目数 | 18,410 |
| 文件大小 | 949,420 字节（约 927 KB） |
| 有 `zh`（`appName`） | 18,410 |
| 有 `en`（`appNameEn`） | 606 |
| 中英都有 | 606 |
| 旧字符串格式 | 0 |
| 空名字/等于包名 | 0 |
| 名字最大长度 | 46 字符 |
| `com./cn./org.` 前缀 | 15,949 |
| 其他前缀（多为 iOS bundle id） | 2,461 |
| 重复包名 | 1,357 个包名出现多行（IOS/ANDROID），多出 1,362 行 |
| 其中名字不同 | 822 个包 |
| 重复处理 | 与插件脚本一致：按接口返回顺序后者覆盖前者；已知常用 App 已校验 |

> 插件仓内置旧快照为 12,561 条（2026-08-22 生成）；CLI 已刷新为上述 18,410 条。重复包名的“后者覆盖”意味着显示名依赖接口返回顺序，发版前建议抽查常用 App。

格式：

```json
{
  "com.tencent.xin": { "en": "WeChat", "zh": "微信" },
  "com.tencent.mm": { "zh": "微信" },
  "com.ss.android.lark": { "zh": "飞书" }
}
```

---

## 4. 方案决策

| 编号 | 决策 | 说明 |
|---|---|---|
| D1 | **只做落库解析** | 修改 `Ingest` 一处；不碰查询/聚合/展示 |
| D2 | **固定中文优先** | CLI 无 locale；`zh → en → 包名`，与插件默认一致 |
| D3 | **历史数据不动** | 不扫描、不重写、不回填；旧行继续显示包名 |
| D4 | **不回填、不迁移** | 避免并发写、格式变化、回滚风险 |
| D5 | **快照内置二进制** | `go:embed`，无网络、无定时任务、无本地缓存 |
| D6 | **不加 provider 生命周期** | Go 侧 `sync.Once` 懒加载即可，不需要 start/stop |
| D7 | **不加语言配置** | 本次不引入 `--lang`/config/environment 语言体系 |

---

## 5. 详细设计

### 5.1 新增文件

```text
internal/notif/
  app-name-map.data.json   # 从插件仓复制/刷新，go:embed
  appname.go               # 加载、查询、回退
  appname_test.go          # 单测
```

> `go:embed` 不能引用包目录以外的文件，因此必须把 JSON vendor 到 CLI 仓，不能在构建时直接读插件仓路径。

### 5.2 API 草案

```go
package notif

import (
    _ "embed"
    "encoding/json"
    "os"
    "strings"
    "sync"
)

//go:embed app-name-map.data.json
var appNameMapData []byte

// 测试/运维覆盖：设置后视为唯一来源；读不到就退化为空映射。
const appNameMapFileEnv = "YOOOCLAW_APP_NAME_MAP_FILE"

type localizedAppNames struct {
    Zh string `json:"zh"`
    En string `json:"en"`
}

var (
    appNamesOnce sync.Once
    appNames     map[string]localizedAppNames
)

func loadAppNames() {
    raw := appNameMapData
    if path := strings.TrimSpace(os.Getenv(appNameMapFileEnv)); path != "" {
        b, err := os.ReadFile(path)
        if err != nil {
            appNames = map[string]localizedAppNames{}
            return
        }
        raw = b
    }
    var m map[string]localizedAppNames
    if err := json.Unmarshal(raw, &m); err != nil {
        appNames = map[string]localizedAppNames{}
        return
    }
    appNames = m
}

func LookupAppDisplayName(pkg string) (string, bool) {
    key := strings.TrimSpace(pkg)
    if key == "" {
        return "", false
    }
    appNamesOnce.Do(loadAppNames)
    names, ok := appNames[key]
    if !ok {
        return "", false
    }
    if v := strings.TrimSpace(names.Zh); v != "" {
        return v, true
    }
    if v := strings.TrimSpace(names.En); v != "" {
        return v, true
    }
    return "", false
}

// 命中返回显示名；未命中原样返回包名，保持现有行为。
func ResolveAppDisplayName(pkg string) string {
    if name, ok := LookupAppDisplayName(pkg); ok {
        return name
    }
    return pkg
}

// 已加载快照条目数（未加载时触发加载），供测试与诊断使用。
func AppNameMapSize() int {
    appNamesOnce.Do(loadAppNames)
    return len(appNames)
}

// 仅供测试：丢弃已加载快照，下次访问重新解析。
// 不要在并发读的用例中使用；相关用例不要 t.Parallel。
func ResetAppNameMapForTest() {
    appNamesOnce = sync.Once{}
    appNames = nil
}
```

说明：

- **懒加载**：只有真正 ingest 的通知进程才付解析成本；CLI 其他命令不受影响。
- **降级**：快照缺失、损坏、格式不符时 `appNames` 为空，`Resolve` 返回包名，不影响通知落库。
- **语言**：`Lookup` 固定 `zh` 优先，缺失时用 `en`，再缺失视为未命中。
- **线程安全**：`sync.Once` + 只读 map；daemon 并发 ingest 安全。
- **测试重置**：如需在测试中切换快照，提供 `ResetAppNameMapForTest()` 并把 `appNames` 置空；使用 `t.Setenv` 的用例不要 `t.Parallel`。
- **旧格式兼容**：当前快照 0 条字符串值；若要兼容插件可能出现的旧格式（`"pkg": "名字"`），可给 `localizedAppNames` 增加 `UnmarshalJSON`（字符串 → `Zh`），或在整体解析失败时用 `map[string]json.RawMessage` 逐项解析。非必需，但建议加，避免上游格式变化时静默全量退化。

### 5.3 唯一改动点

```go
// internal/notif/storage.go，Ingest 内（当前 189-191 行）
entry := s.buildStored(n, clientLabel)
if entry.AppDisplayName == "" {
-    entry.AppDisplayName = entry.AppName
+    entry.AppDisplayName = ResolveAppDisplayName(entry.AppName)
}
```

`buildStored` 目前不设置显示名，所以这个分支对新数据总是成立。  
保留 `if` 是为了让 diff 最小，也不改变未来 `buildStored` 可能设置显示名的行为。

### 5.4 不修改的调用链

```text
Ingest（新通知）
  → buildStored（只设置 AppName）
  → 本次唯一改动：ResolveAppDisplayName
  → 落盘 JSONL
  → Query / Sync / Assets / SummaryJob / Transfer / yclib（全部原样）
```

### 5.5 性能与资源

本机实测（darwin/arm64，Go 1.27.1，直接 JSON 解析）：

| 指标 | 值 |
|---|---|
| 首次解析 949 KB | 约 5.4 ms |
| 常驻堆 | 约 4.5 MB |
| 单词 lookup | 约 5 ns |
| 二进制增量 | 约 949 KB（未压缩） |

首次 ingest 会在 `Storage.mu` 内完成加载；daemon 生命周期内只发生一次。若希望避开这 5 ms，可在 `Storage.Init` 预热（可选，不改变语义）。

---

## 6. 行为变化

### 6.1 新数据（发布后）

```json
// 发布后 ingest 的新通知
{
  "appName": "com.tencent.xin",
  "appDisplayName": "微信",
  "title": "...",
  "content": "...",
  "timestamp": "..."
}
```

- `appName` 仍是包名，作为稳定身份；
- `appDisplayName` 变成应用名；
- 未知包名仍等于包名；
- 无 `app` 字段仍是 `Unknown`；
- 去重 key 不含显示名，重复上报行为不变。

`appDisplayName` 在结构体上是 `omitempty`：命中映射的新行会写入该字段；未命中时仍写包名，与改前一致。

### 6.2 历史数据（发布前）

完全不动：

```json
{
  "appName": "com.tencent.xin",
  "appDisplayName": "com.tencent.xin"
}
```

旧行继续显示包名，直到被保留期清理或用户手工迁移（本次不提供迁移）。

### 6.3 过渡期可见效果

同一个 App 在查询结果里会同时出现两种显示名：

```text
[旧] appName=com.tencent.mm, appDisplayName=com.tencent.mm
[新] appName=com.tencent.mm, appDisplayName=微信
```

| 场景 | 表现 |
|---|---|
| `notification search` | 新旧行显示不同 |
| `notification stats --dim app` | `com.tencent.mm` 和 `微信` 两个桶 |
| `notification summary` 的 `topApps` | 同上，分别计数 |
| `--app 微信` | 只命中新行（旧行没有友好名，且本次不改过滤逻辑） |
| `--app com.tencent.mm` | 新旧都命中（`AppName` 仍是包名） |
| Assets / Summary Job | 新任务/新行友好名，旧行包名 |
| transfer 导出 | 新行友好名，旧行包名 |

这是“历史数据不管 + 不改其他逻辑”的必然结果，属于已接受范围。

### 6.4 明确不变量

- `appName` 永远是包名，是稳定身份；
- `appDisplayName` 只是展示字段，可能为空、可能是包名、可能是旧快照；
- `contentKey` 不变；
- transfer 记录身份哈希不变（`package.go` 显式排除 `appDisplayName`）；
- 旧文件不重写、不迁移；
- 不新增网络请求、定时器、后台任务；
- 不新增 CLI 全局语言开关。

---

## 7. 实施步骤

### Phase 0：数据准备（0.5 天）

- [x] 确认使用哪份快照：2026-11-22 重新拉取生产接口最新数据，不再直接使用插件仓旧文件；
- [x] 如重新拉取，确认 CLI 的 api key 有权访问 `POST /api/application-config/app-package/config-all`（实测 200 / `success:true`）；
- [ ] 确认应用元数据再分发合规（插件已随包分发，通常无新增问题）；
- [x] 把文件复制到 `internal/notif/app-name-map.data.json`；
- [x] 记录来源、拉取时间、条数、sha256（见下方实现记录）。

> 内置快照的质量 = 发版时的数据新鲜度。2026-11-22 拉取返回 19,776 行、归一化 18,410 条；发版前仍需再刷新一次，之后新增的 App 才不会被漏掉。

### Phase 1：加载器与数据（1 天）

- [x] 新增 `internal/notif/appname.go`；
- [x] 新增 `internal/notif/app-name-map.data.json`；
- [x] 实现 `LookupAppDisplayName` / `ResolveAppDisplayName` / 测试重置；
- [x] 增加单测：命中、未命中、空包名、损坏文件、缺失覆盖文件、条数与已知条目；
- [x] `go vet ./internal/notif/...` 通过（受本机依赖缓存限制，未跑全仓 `go vet`）。

### Phase 2：落库接入（0.5 天）

- [x] 修改 `internal/notif/storage.go` 的唯一一行；
- [x] 增加 `Ingest` 用例：`com.tencent.xin → 微信`、未知包名不变、`Unknown` 不变；
- [x] 增加去重回归：同一通知重复 ingest，`dedupedById`/`dedupedByContent` 与改前一致；
- [x] `go test -race ./internal/notif/... ./internal/summaryjob/...` 通过；
- [ ] 覆盖率门禁（`make cover-gate`）——受本机模块缓存限制未跑，待完整依赖环境补跑。

### Phase 3：发布与验证（0.5 天）

- [ ] 发版前按 Phase 0 再刷新快照；
- [ ] 构建至少一个平台产物；
- [ ] 升级 daemon 后，用手机发送一条 `com.tencent.xin` 测试通知；
- [ ] `yc notification search --limit 1` 确认 `appDisplayName=微信`、`appName=com.tencent.xin`；
- [ ] `yc notification stats --dim app` 确认新行进入“微信”桶；
- [ ] 确认旧行文件未变化；
- [ ] 发布说明注明“仅对发布后新通知生效，历史通知仍显示包名”。

### 实现记录（2026-11-22）

- 分支：`feat/app-name-map`
- 快照：2026-11-22 通过生产接口 `config-all` 实时拉取，19,776 行归一化为 18,410 条；文件 949,420 字节；sha256 `e21734faf095ac76662311f6926ea8cddff9d941f56becdeafbcb60aeae75836`；插件仓旧快照 12,561 条（2026-08-22）仅作参考。
- 接口验证：带本机 api key 请求生产接口返回 HTTP 200、`success:true`、19,776 行。
- 新增：`internal/notif/appname.go`、`internal/notif/app-name-map.data.json`、`internal/notif/appname_test.go`。
- 修改：`internal/notif/storage.go` 的 `Ingest` 一行。
- 验证：`go vet ./internal/notif/...` 通过；`go test -race ./internal/notif/... ./internal/summaryjob/...` 通过；`internal/assets`、`yclib` 因本机缺少 `aliyun-oss-go-sdk-v2` / `gorilla/websocket` 模块缓存未执行。
- 待办：发版前再刷新快照；真机/daemon 验证；在完整依赖环境补跑 `assets`/`yclib`/覆盖率；评审合并。

---

## 8. 测试计划

| 用例 | 断言 |
|---|---|
| 快照加载 | `AppNameMapSize() > 1000`；`com.tencent.xin → 微信`；`com.tencent.mm → 微信` |
| 中文优先/英文回退 | 仅 `en` 的条目返回英文；中英都有返回中文 |
| 未知包名 | `ResolveAppDisplayName("com.foo.bar") == "com.foo.bar"` |
| 空包名 | `ResolveAppDisplayName("") == ""`，不 panic |
| 覆盖文件缺失 | 设置 `YOOOCLAW_APP_NAME_MAP_FILE=/nonexistent`，全部回退包名 |
| 覆盖文件损坏 | 写入非法 JSON，全部回退包名；`Reset` 后恢复 |
| 落库命中 | Ingest `com.tencent.xin` → `AppName=com.tencent.xin`，`AppDisplayName=微信` |
| 落库未知 | Ingest `wechat` → 两个字段都是 `wechat` |
| 落库 Unknown | 无 `app` → 两个字段都是 `Unknown`（现有用例保持） |
| 去重不变 | 重复 ingest 的 `dedupedById`/`dedupedByContent` 与基线一致 |
| 历史文件不变 | 读操作前后文件字节/mtime 不变 |
| 全量回归 | `go test -race ./...` 通过 |

---

## 9. 风险与缓解

| 风险 | 影响 | 概率 | 缓解 |
|---|---|---|---|
| 快照过期 | 发布后新 App 仍显示包名 | 高 | 已刷新至 2026-11-22；发版前再刷新；记录条数/sha256；发布检查项 |
| 映射错误/脏数据 | 错误名字永久写进新行 | 中 | 抽查常用 App；单测断言已知条目；数据条数下限 |
| 语言固定中文 | 以后切英文/繁体不会自动纠正 | 中 | 现在明确固定中文；如产品要英文，先定策略再发布 |
| 过渡期混显 | 同一 App 出现包名和中文名，统计拆桶 | 高（确定发生） | 已接受“历史不管”；发布说明写清；后续可做读时解析（超出本次） |
| 映射更新后新行之间不一致 | 不同时间落库的新行使用不同快照，改名后显示不同 | 中 | 本次范围不解决；后续读时解析或回填 |
| transfer import 绕过 Ingest | 导入行不走本次映射 | 中 | 插件导出通常已带显示名；若源数据缺失，属范围外 |
| 快照损坏/格式变化 | 全部回退包名 | 低 | 解析失败降级；单测条数与已知条目；兼容旧字符串格式 |
| 首次加载 5.4 ms / 4.5 MB | daemon 首次 ingest 轻微延迟 | 低 | 懒加载；可选 `Init` 预热 |
| 二进制 +949 KB | 下载体积增加 | 低 | 可接受；数据比插件旧快照更新 |
| 现有测试命中 `com.tencent.xin` | 断言可能变化 | 低 | 已确认现有用例未断言该包的显示值；`Unknown` 用例仍成立；跑全量 |
| 全局单例测试污染 | 用例互相影响 | 低 | `ResetAppNameMapForTest`；覆盖用例不并行 |
| 数据再分发合规 | 法务风险 | 中/低 | 插件已有先例；确认内部授权 |

---

## 10. 待确认问题

1. **语言**：确认固定中文显示名？如果未来要英文，是否现在就改为 `en` 优先？（落库后不会自动纠正，必须先定。）
2. **数据来源**：已确认（2026-11-22）CLI 本机 api key 可请求生产接口，已刷新为 18,410 条；发版前由发布负责人再刷新一次。
3. **transfer 导入**：发布后 `transfer import` 进来的历史通知不经过 `Ingest`，是否接受它们保留源数据的显示名？
4. **过渡期混显**：确认接受新旧行在查询/统计中同时出现包名和应用名？如果不接受，需要追加“读时解析”小迭代。
5. **工具化**：本次是否只手工复制快照，还是同时加一个 dev-only 的同步脚本（用 `creds` + `config` 拉取）？

---

## 11. 附录

### 11.1 插件端数据拉取（供刷新参考）

```bash
curl -sS -X POST "https://<host>/api/application-config/app-package/config-all" \
  -H "Content-Type: application/json" \
  -H "Accept-Language: zh" \
  -H "X-Api-Key-Id: <api-key>" \
  -d '{"platform":""}'
```

响应形如：

```json
{
  "success": true,
  "data": [
    { "packageName": "com.tencent.xin", "appName": "微信", "appNameEn": "WeChat" }
  ]
}
```

生成快照时与插件脚本保持一致：

- 丢弃空 `packageName`；
- `appName`/`appNameEn` 去空白；
- 等于包名的名字剔除；
- `zh == en` 时只保留 `zh`；
- 按 key 排序，紧凑 JSON，末尾换行。

> 2026-11-22 实测：生产接口可用（HTTP 200 / `success:true` / 19,776 行）。同一 `packageName` 可能有多行（IOS/ANDROID），按插件脚本规则“后者覆盖前者”；当前数据有 1,357 个重复包名、822 个名字不同，已按该规则归一化为 18,410 条。

### 11.2 关键代码位置

**插件**

- `openclaw-plugin/src/notification/app-name-map.ts`
- `openclaw-plugin/src/notification/storage.ts`
- `openclaw-plugin/src/cli/helpers.ts`
- `openclaw-plugin/scripts/sync-app-name-map.ts`
- `openclaw-plugin/src/notification/APP_DISPLAYNAME_UPDATE.md`
- `openclaw-plugin/test/app-name-map.test.ts`

**CLI**

- `cli/internal/notif/storage.go`（`Ingest` 189-191、`buildStored` 369、`contentKey` 455）
- `cli/internal/notif/types.go`（10-13）
- `cli/internal/notif/query.go`（280-299）
- `cli/internal/notif/aggregate.go`（44）
- `cli/internal/notif/sync.go`（190、254）
- `cli/internal/summaryjob/summaryjob.go`（107、161、171）
- `cli/internal/assets/project.go`（92-96）
- `cli/internal/transfer/package.go`（103、247）
- `cli/internal/daemon/server.go`（659-696）

### 11.3 更新记录

| 日期 | 变更 |
|---|---|
| 2026-11-20 | 初稿：确定“只做新数据落库解析、历史不动、其他逻辑不改”的最小方案 |
| 2026-11-20 | 追加“需求记录（用户输入）”章节，保留原始需求与口径演进 |
| 2026-11-22 | 按方案实现：新增快照/加载器/单测，`Ingest` 接入；`internal/notif` 与 `internal/summaryjob` 的 `go test -race` 通过 |
| 2026-11-22 | 实测生产接口可用并刷新快照：19,776 行 → 18,410 条，sha256 `e21734…`；同步更新性能、风险与待确认项 |
