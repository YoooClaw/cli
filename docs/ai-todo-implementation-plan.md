# CLI 0.11.0 AI TODO 实施方案

状态：已实现并完成单元测试、构建及测试区工具链联调；尚未提交或发布。版本号由发布流程处理。
日期：2026-09-10。

## 1. 分支与基线

本仓库实际主分支是 master（不存在 main）。已 fetch origin，将本地 master 快进至 e53fec4，再依次创建 release/0.11.0、feat/0.11.0-ai-todo。实现先进入功能分支，再合入发布分支；本轮不发布、不推送。

Hermes 对应分支为 feat/0.10.0-ai-todo，消费本项目产物。OpenClaw 参考实现为 openclaw-plugin 的 feat/ai-todo-tools，提交 7587628。共享的是接口和行为规范，Go 不依赖 TS 运行时。

## 2. 范围及架构

新增 yc/yoooclaw todo list、get、create、update、delete 五个命令。完成和恢复统一使用 update.isDone，不增设独立完成、恢复命令。CLI 直接调用云端，复用 profile、凭据及环境解析；不要求 daemon 在线，不在本地保存另一套待办，也不创建本地提醒任务。

Go Client 负责鉴权、ISO 时间转换、校验、幂等、响应解析和部分成功。命令层负责参数解析及既有 JSON 输出规范。Hermes 只包装命令，不重复实现 HTTP 业务逻辑。

本轮不扩展 yclib SDK 或 daemon RPC；现有 yclib 灯效走 daemon，不应为待办 CLI 强加此依赖。其他 Agent 通过统一命令及待办 Skill 复用。

## 3. 对外接口（以实际联调修正为准）

基础地址：https://<当前环境 host>/api/message/todo/agent。全部 POST，携带 Content-Type、Accept-Language、X-Api-Key-Id（裸 API Key）。不传 App JWT，不向模型暴露凭据，不允许请求跟随重定向泄露鉴权头。

| 命令 | 路径 | 请求及结果 |
| --- | --- | --- |
| list | /query | isDone、startTime、endTime、keyword、pageNo、pageSize；items、pageNo、isLastPage |
| get | /detail | todoId；item |
| create | /create | requestId、title、dueAt、isFullDay；todoId、duplicated |
| update（内容） | /update | todoId 及修改字段；item |
| update（状态） | /status | todoIds、isDone；processedTodoIds |
| delete | /deleteBatch | todoIds、deleteReason=manual_delete；processedTodoIds |

成功同时要求 HTTP 成功、业务 code=000000 和响应结构合法。HTTP 200 可能包含业务错误。保留业务码及可读原因；纯文本错误（如 Jwt is missing）也需保留并脱敏。ID 全程字符串，避免大整数精度丢失。

2026-09-10 联调：有时间创建、查询、详情、修改、状态、删除均已实际成功；专用测试待办 842 已清理。无时间创建仍观察到 910001：dueAt 不能为空，与 PRD/契约允许 null 不一致，需后端修复及复验，不能宣称已支持，不得自动补时间绕过。

## 4. 命令契约

目标参数语义与 OpenClaw ntf todo 一致，根命令沿用 yc/yoooclaw。

```sh
yc --format json todo list --is-done false --start-time '2026-09-11T00:00:00+08:00' --end-time '2026-09-12T00:00:00+08:00'
yc --format json todo list --is-done null
yc --format json todo get 841
yc --format json todo create --title '会议' --due-at '2026-09-11T15:00:00+08:00' --is-full-day false
yc --format json todo update 841 --title '审批会议'
yc --format json todo update 841 --is-done true
yc --format json todo update 841 --is-done false
yc --format json todo delete 841 --confirmed
```

这些仅为接口设计示例，当前尚未实现。

- list 默认未完成、不限时间、pageNo=1、pageSize=20；今天未完成的自然语言默认值由 Agent 解析成显式边界。isDone=null 表示全部状态，范围为左闭右开。
- create 接收 --request-key，缺省生成并返回可复用键。未知结果使用原键及原参数核对/重试；不同请求使用新键。
- update 只传指定字段。必须区分未传（保留）、null（清空）、false（布尔值）；拒绝冲突参数和空修改。
- 支持结构化 JSON 文件及 stdin 输入；Hermes 推荐 stdin，避免标题和正文进入进程参数。结构化输入和字段参数冲突时拒绝，不暗中覆盖。
- delete 支持多个 ID；要求显式确认参数，Hermes 仅在用户确认具体目标后传入。
- 输出沿用现有 CLI envelope 和退出码；错误与部分成功退出非零但保留完整 JSON，不丢弃已成功部分。

## 5. 时间、幂等和修改流程

时间指开始时间。接收用户当前时区的 ISO（含偏移/Z），Go 转为 UTC 毫秒；不接收没有偏移的日期时间。全天使用 YYYY-MM-DD，并编码为业务日期的 UTC 零点；无时间用 JSON null，isFullDay=false。验证非法日期、时刻和时间区间。兼容数字毫秒输入时仅留给明确 CLI 调用，不要求模型计算时间戳。

requestId 用 agent_todo_ 加 SHA-256 base64url，合计 54 字符，不超过服务端 64。由稳定调用键派生，重放一致；空键随机生成。创建传输失败至多自动重试一次，沿用相同 ID；业务错误不自动重试。旧格式长度 75 的问题不得带入 Go 实现。

只修改状态时直接调用 status。修改内容时先 detail 校验目标及新手事项限制、合并后的时间，再 update。内容和状态同时修改：先内容后状态；内容失败停止，状态失败返回 contentUpdated=true、statusUpdated=false 和错误，只重试失败阶段。processedTodoIds 必须检查未处理 ID，不能把 HTTP 成功等同整批成功。

## 6. 文件改造计划

| 位置 | 计划 |
| --- | --- |
| internal/aitodo/client.go、time.go、types.go（新增） | 云端协议、参数与时间模型、幂等及错误处理 |
| internal/cli/cmd_todo.go（新增）、root.go | Cobra 命令、stdin/文件输入、确认、统一输出 |
| internal/creds、config、envhost（复用） | 当前 profile、测试/生产环境、凭据读取 |
| skills/yoooclaw-todo/SKILL.md（后续新增） | 自然语言默认范围、创建引导、删除确认；不在本轮创建 |
| internal/skills、assets.go 及相关测试 | 核对 Skill 打包、安装、升级发现链路 |
| README.md、README.zh-CN.md | 命令示例、错误及重试说明 |

## 7. 实施顺序与验收

1. 固定命令输入输出及跨项目样例，先实现 Go Client 与时间转换。
2. 接入命令，验证不开 daemon 也可执行；补充 Skill 与文档。
3. 单测覆盖六个完整 URL、环境/凭据、ISO/全天/null、64 字符与重放、布尔三态、分页、响应结构、纯文本 401、业务错误、部分成功、重试上限。
4. Go test/vet 及项目要求的构建检查；跨平台核对 stdin 和 JSON 输出。
5. 测试区仅创建标记明确的测试事项，完成查询→详情→改标题/时间→完成→恢复→删除；记录响应并确认清理，不修改用户已有待办。无时间用例单列后端依赖。
6. CLI 发布可供 Hermes 打包的 0.11.0 产物；Hermes 完成安装与真实模型验收后再进入对应发布流程。

单次 HTTP 预算建议 30 秒，创建重试及复合修改的总时长须受整体截止时间约束。与 Hermes 约定待办命令整体不超过 100 秒、宿主包装超时 120 秒；取消后结果未知需如实反馈，不能换新键重建。

## 8. 实施记录（2026-09-10）

已落地 internal/aitodo（client、time）、internal/cli/cmd_todo、命令注册、yoooclaw-todo Skill、两种 README 和回归测试。结构化输入统一使用 --json-file FILE / -；CLI 默认无时间创建可表达，但后端当前仍拒绝。

验证结果：
- 待办客户端、参数、CLI 与 Skill 测试通过；Go vet 通过。
- Go 全量首次仅新增 Skill 清单预期和录音测试临时目录清理失败；更新 Skill 预期后，Skill 与录音包均复跑通过。录音业务代码未修改。
- Hermes 真实 handler → 编译 CLI → 测试区：定时创建、查询、详情、标题修改、完成、恢复、删除均成功（测试 ID 883 已删除）。
- 全天创建及详情成功（测试 ID 884 已删除）；无时间创建仍返回 910001 / dueAt 不能为空，未擅自补时间。
- 本轮测试未替换现有 CLI、OpenClaw 或 Hermes 安装；编译产物存于临时目录。

正式发布仍需先发布含本功能的 CLI 0.11.0，再打包 Hermes。后续宿主真实聊天入口的模型路由和删除确认体验需安装后验收；本轮的真实接口联调不等同于完成该项模型验收。
