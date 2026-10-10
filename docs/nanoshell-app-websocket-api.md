# NanoShell 程序列表与安装包下载接口

版本：v1

面向：YoooClaw App 开发者

状态：CLI 功能分支已实现这两个方法；尚待 App 与线上 Relay 联调。示例用于约定格式，不是可下载的真实数据。

## 1. 接入方式

复用 App 已有的 Relay WebSocket 连接和登录鉴权，新增两个 RPC 方法：

| 方法 | 用途 |
| --- | --- |
| `nanoshell.apps.list` | 查询当前用户可访问的已发布程序列表 |
| `nanoshell.apps.download` | 按程序 SHA-256 获取完整 `app.wasm` 文件 |

无需单独建立 NanoShell WebSocket 连接。连接地址、认证和目标 CLI 的选择沿用 App 当前接入方式，本文不新增连接参数。

安装包保存在对应的 CLI 主机。查询与下载时，该 CLI 必须在线。App 被关闭不会删除已发布的包，重新进入后可再次查询。

列表只包含已完成发布的程序；开发中、测试失败或等待用户确认的临时构建不出现在列表中。

## 2. 公共消息格式

请求：

```json
{
  "type": "req",
  "id": "ns-list-001",
  "method": "nanoshell.apps.list",
  "params": {}
}
```

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `type` | string | 是 | 固定为 `req` |
| `id` | string | 是 | App 生成的请求 ID；同一连接中未完成的请求不得重复，建议使用 UUID |
| `method` | string | 是 | 本文定义的方法名 |
| `params` | object | 是 | 方法参数；无参数时传 `{}` |

成功响应：

```json
{
  "type": "res",
  "id": "ns-list-001",
  "ok": true,
  "payload": {}
}
```

失败响应：

```json
{
  "type": "res",
  "id": "ns-list-001",
  "ok": false,
  "error": {
    "code": "INVALID_PARAMS",
    "message": "limit must be between 1 and 100"
  }
}
```

响应 `id` 与请求一致。App 必须按 `id` 匹配请求，不能依赖响应顺序。`ok=true` 时读取 `payload`，`ok=false` 时读取 `error`；不要依赖 `message` 文案判断错误类型。

### 与现有接口的格式关系

本接口复用 CLI 已有的 `req/res` RPC 格式，App 成功响应的数据入口为 **`payload`**。

| 项目 | 现有 RPC | 本文约定 |
| --- | --- | --- |
| 请求外壳 | `type / id / method / params` | 一致 |
| 成功响应外壳 | `type / id / ok / payload` | 一致 |
| 失败响应外壳 | `type / id / ok / error {code, message}` | 一致 |
| 方法命名 | 点分命名，如 `recordings.list` | `nanoshell.apps.list`、`nanoshell.apps.download` |
| 业务字段命名 | 如 `recordingId`，使用 camelCase | `appId`、`packageId`、`packageBytes` 等 |

注意区分两种现有调用方式：本文使用 `type=req` 的 RPC。现有 HTTP 代理消息 `type=request` 返回的是 `proxy_response`，不能混用。CLI 内部 HTTP Gateway 的成功响应为 `{ok:true,data:...}`，转发层将该 `data` 转为 WebSocket 的 `payload`；App 不应据此把本文外层 `payload` 改为 `data`。下载的 `payload.data` 则是新定义的 Base64 内容字段。

以下属于本次新增业务约定，并非现有所有接口共用的格式：

- 现有 `recordings.list` 返回 `{total, recordings}`，没有游标分页；本文返回 `{items, nextCursor}`。两者外层 RPC 相同，但 App 需要独立的程序列表数据模型，不应直接复用录音列表模型。
- `packageId`、整包 Base64、分页游标和包大小限制均为本接口新增。
- 通用的 `INVALID_PARAMS`、`INTERNAL_ERROR` 沿用已有命名；`PACKAGE_*`、`INVALID_CURSOR` 等为本接口新增业务错误码，App 需增加对应处理。

以上已按 CLI 的 `internal/relay/types.go`、`internal/relay/dispatcher.go`、`internal/daemon/server_light.go` 和 `internal/daemon/server_ingest.go` 核对；尚未核对 App 客户端源码或真实 Relay 联调结果。

## 3. 标识与版本

| 字段 | 含义 |
| --- | --- |
| `appId` | 稳定的应用标识，例如 `com.example.flappy`；同一应用升级后保持不变 |
| `version` | 应用版本，正整数；发布更新时递增 |
| `packageId` | 完整 `app.wasm` 文件字节（包括 NSP1 头）的 SHA-256，小写、64 位十六进制字符串；标识一个不可变的安装包 |

程序字节相同时 `packageId` 相同，不受 ZIP 压缩方式、文件时间或 README 变化影响。同一应用的同一版本不允许被替换成另一份包，修订时必须提高 `version`。

`packageId` 同时用于下载定位、缓存去重和完整性校验，不再增加单独的 MD5。列表、下载响应和实际 `app.wasm` 的哈希必须一致。

相同包 ID 不代表任何用户都可访问。服务端会校验当前连接身份的访问权限，App 无需在请求中传 `userId` 或 `clientLabel`。

## 4. 查询程序列表

方法：`nanoshell.apps.list`

### 请求参数

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `limit` | integer | 否 | 20 | 每页条数，范围 1～100 |
| `cursor` | string | 否 | `""` | 第一页省略或传空字符串；后续页原样使用上一页的 `nextCursor` |

```json
{
  "type": "req",
  "id": "ns-list-001",
  "method": "nanoshell.apps.list",
  "params": {
    "limit": 20,
    "cursor": ""
  }
}
```

### 成功响应

```json
{
  "type": "res",
  "id": "ns-list-001",
  "ok": true,
  "payload": {
    "items": [
      {
        "appId": "com.example.flappy",
        "name": "小鸟飞行",
        "version": 1,
        "packageId": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
        "packageBytes": 10240,
        "publishedAt": "2026-10-09T10:00:00Z"
      }
    ],
    "nextCursor": ""
  }
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `items` | array | 当前页程序列表；没有程序时为 `[]`，不返回 `null` |
| `items[].appId` | string | 应用标识 |
| `items[].name` | string | 展示名称 |
| `items[].version` | integer | 当前发布版本 |
| `items[].packageId` | string | 下载和校验使用的完整 SHA-256 |
| `items[].packageBytes` | integer | 完整程序文件字节数，包括已有 NSP1 头，不是 ZIP 大小或 Base64 字符数 |
| `items[].publishedAt` | string | 当前版本首次发布的 UTC 时间，RFC 3339 格式 |
| `nextCursor` | string | 下一页游标；`""` 表示没有下一页 |

列表每个 `appId` 只显示最新发布版本，按 `publishedAt` 降序排列，同时间按 `appId` 升序排列。重复发布同一版本不改变发布时间。

游标不透明，App 不解析或拼接。分页期间可能有新版本发布，App 按 `appId` 合并列表并保留较高版本；下拉刷新时从第一页重新查询。v1 不承诺跨页快照一致性。

## 5. 获取安装包

方法：`nanoshell.apps.download`

### 请求参数

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `packageId` | string | 是 | 列表返回的完整安装包 ID |
| `appId` | string | 否，建议传 | 列表中的应用 ID；与 version 一起定位对应元数据 |
| `version` | integer | 否，建议传 | 列表中的版本，指定时必须同时传 appId |

```json
{
  "type": "req",
  "id": "ns-download-001",
  "method": "nanoshell.apps.download",
  "params": {
    "appId": "com.example.flappy",
    "version": 1,
    "packageId": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  }
}
```

相同程序可能被不同应用或版本复用，因此 App 建议同时提交列表中的 `appId`、`version` 和 `packageId`。仅传 `packageId` 时，唯一匹配可正常下载；匹配到多个应用/版本时返回 `INVALID_PARAMS`，不会随机选取元数据。响应结构保持不变。

### 成功响应

```json
{
  "type": "res",
  "id": "ns-download-001",
  "ok": true,
  "payload": {
    "appId": "com.example.flappy",
    "version": 1,
    "packageId": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    "fileName": "app.wasm",
    "contentType": "application/octet-stream",
    "encoding": "base64",
    "size": 10240,
    "data": "此处为完整app.wasm文件（含NSP1头）的Base64字符串"
  }
}
```

上面的 `data` 是说明性占位文本，不可直接解码；哈希和大小也是示例值。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `appId` | string | 安装包所属应用 |
| `version` | integer | 安装包版本 |
| `packageId` | string | 必须等于请求的包 ID，同时作为 SHA-256 校验值 |
| `fileName` | string | 建议保存名称，仅作展示；不能直接作为不受约束的文件路径 |
| `contentType` | string | 带 NSP1 头时为 `application/octet-stream`；裸 Wasm 为 `application/wasm` |
| `encoding` | string | 固定为 `base64` |
| `size` | integer | 解码后完整程序文件的字节数，应与列表中的 `packageBytes` 一致 |
| `data` | string | 标准 Base64，使用 `+`、`/` 和所需的 `=` 填充，无换行，无 `data:` 前缀 |

v1 一次响应返回整个 `app.wasm` 文件，不提供分片、断点续传或下载进度事件。界面可以显示“正在下载”，不能根据此协议显示实时百分比。

当前设备程序上限为裸 Wasm **12288 字节**，带 16 字节 NSP1 头时完整文件为 **12304 字节**；完整程序的 Base64 最长为 16408 个字符。本地 WebSocket 集成测试覆盖带头的最大程序；上线前仍需 App 与线上 Relay 联调。发布命令允许的 ZIP 上限为 128 KiB，与下载数据大小是两个概念。

### App 校验与保存顺序

1. 检查响应 `id`、`ok`、`encoding`，以及 `packageId` 是否等于所请求的值。
2. 检查 `size` 和 Base64 长度没有超过约定上限，再解码到临时文件或有界缓冲区。
3. 验证解码后的字节数等于 `size`，且与列表中的 `packageBytes` 一致。
4. 对解码后的完整程序文件计算 SHA-256，转为小写十六进制，必须等于 `packageId`。不要对 Base64 文本计算哈希。
5. 核对响应中的 `appId`、`version` 与选中列表项一致。文件前 4 字节若为 `NSP1`，保留完整 16 字节头，并核对头中 payload 长度与实际字节一致；不要裁掉头或重新编译。
6. 全部通过后保存为 `app.wasm`，再进入现有硬件安装流程。App 无需解压 ZIP。

云电脑实测的 flappy、flappy-game、fishing、rec3click 四个程序均带 NSP1 头，因此正常下载响应使用 `application/octet-stream`。文件名虽为 `.wasm`，并不代表从第一个字节起就是裸 Wasm。

应用名称来自列表的 `name`，应用 ID 和版本来自列表/下载响应。如果硬件安装流程还需要 manifest，由 App 使用这些元数据及 `entry: "app.wasm"` 组织；不能把程序文件单独写入任意位置就假定已经安装。硬件安装协议仍待与 App 联调确认。

下载接口只负责将包交给 App。**下载成功不代表已安装到硬件**，App 应分别显示下载、传输、安装结果。向硬件发送哪些文件及使用什么传输协议，沿用硬件安装接口，不属于这两个接口的范围。

## 6. 错误处理

业务错误统一使用公共失败响应格式。

| `error.code` | 场景 | App 处理建议 |
| --- | --- | --- |
| `INVALID_PARAMS` | 参数类型、范围或包 ID 格式错误 | 修正请求，不自动重试 |
| `INVALID_CURSOR` | 游标无效或已失效 | 清空游标，从第一页重新查询 |
| `PACKAGE_NOT_FOUND` | 包不存在，或当前身份无权访问 | 刷新列表，提示包不可用；不透露其他用户的包是否存在 |
| `PACKAGE_TOO_LARGE` | 包超过当前整包传输上限 | 提示当前版本不支持下载该包 |
| `PACKAGE_CORRUPTED` | CLI 上保存的包损坏或哈希不符 | 提示包不可用，需要重新发布；不进入安装 |
| `STORAGE_UNAVAILABLE` | CLI 本地存储不可读取 | 提示稍后重试 |
| `INTERNAL_ERROR` | CLI 内部处理失败 | 允许用户重试 |

现有 RPC 转发层也可能返回 `HTTP_ERROR`、`INVALID_GATEWAY_RESPONSE`。App 应按通用请求失败处理，并保留请求 ID 便于排查。未识别的错误码同样要有通用提示，不能当作成功。

以下情况不是上述接口保证返回的业务错误：

- **CLI 离线、连接断开、请求超时**：可能没有 `res`，也可能由 Relay 返回其既有错误。按现有连接协议处理，不将超时展示成“没有程序”。
- **登录失效**：按 App 现有登录与重连流程处理。
- **App 校验失败**：由 App 本地判定为下载损坏，删除临时文件，不安装。

建议请求超时：列表 15 秒、下载 30 秒；这属于 App 默认策略，不是响应时延保证。两个接口均只读，可以重试。连接恢复后重试使用新的请求 `id`，下载仍使用原 `packageId`；对已超时请求的迟到响应应忽略。

## 7. App 页面与缓存行为

1. 用户进入“硬件程序”页面时，查询第一页。
2. 下拉刷新或 App 回到前台且页面可见时，重新查询；已有未完成查询时合并触发，避免重复请求。
3. 点击下载时，若本地已有相同 `packageId` 且重新校验通过，可直接复用；否则调用下载接口。
4. 下载期间发布了新版本，也必须返回请求指定的旧包，不能静默换成最新版。旧包已不可用时返回 `PACKAGE_NOT_FOUND`，由 App 刷新列表。
5. 本地已下载的包可以用于离线安装；CLI 离线时不能获取新列表和新包。缓存列表应标明尚未刷新。
6. 缓存列表和包访问关联按账号及目标 CLI 隔离，切换账号不能沿用上一账号的可访问列表。

第一版没有“新程序发布”推送，App 主动查询即可；App 被杀掉后不需要补收历史推送。

## 8. 联调验收

- 空列表返回 `items: []`，与 CLI 离线可明确区分。
- 分页、下拉刷新、前台恢复查询正常；并发响应能按请求 ID 正确匹配。
- 同应用升级后列表显示最新版本；重复发布相同包不增加重复项。
- 列表选择的 `packageId` 与下载返回内容一致，下载期间发布新版不会换包。
- 程序可解码、长度匹配、SHA-256 匹配、应用 ID 和版本匹配，能够交给硬件安装流程。
- 断线、超时、包不存在、无权限和损坏场景不会被当作下载成功。
- 不同客户端不能查询或下载彼此未授权的包。
- 12304 字节、带 NSP1 头的程序能通过真实 Relay 完整传输；超过约定上限时返回明确错误。
- App 重启后可以重新查询；合法本地缓存可以复用。

## 9. 联调准备：生成并发布测试包

CLI 版本需要包含 `nanoshell` 命令。发布仅通过本机命令执行，App 只有本文的两个只读接口。

```bash
yoooclaw --profile default nanoshell storage-path
yoooclaw --profile default nanoshell publish --package /absolute/path/flappy.zip --client phone-a
yoooclaw --profile default nanoshell list --client phone-a
```

`phone-a` 替换为已有 API-key 的客户端 label。默认存储目录是 `~/.yoooclaw/profiles/default/nanoshell/`，与通知、录音同级，支持已有的 `YOOOCLAW_HOME` 与 profile 设置。CLI 复制包后原项目可移动；不要手工写入该目录。发布需要在 Agent 完成验收且用户认可后执行，CLI 包校验本身不代表用户授权或真机验收。

发布返回应用元数据与 `duplicated`：同客户端、同应用版本、同包重复发布为 `true`；同版本不同包返回 `VERSION_CONFLICT`，需要提升应用版本后重新构建和验收。不同客户端可分别发布同一包，权限相互独立。客户端 label 为 `default` 时也严格按归属隔离。

CLI 随包提供 `nanoshell-app-builder` skill，可用已有 `skills install` 命令安装。CLI 发布命令接收程序 ZIP 并提取 `app.wasm` 入库；App 下载接口只返回程序字节，不返回 ZIP、manifest.json 或 README。
