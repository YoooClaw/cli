# 健康字段说明

## 问题与边界

健康字段解释曾分别维护在 skill 和 Harness。
修改字段时需要同步多处，容易遗漏。
本改动将解释集中到 CLI，并随结果自动返回。
模型无需额外调用命令才看到说明。

本阶段只修改 CLI。Harness 接入另提 PR。
CLI 发布后，再合并 Harness 配套改动。

## 唯一来源

`internal/healthdata/semantics.json` 保存解释。
`semantics.go` 用 Go embed 将资源打入二进制。
资源按 common、items、sleepSessions 等类型组织。
每节的 matchKeys 同时覆盖完整字段和摘要字段。
运动明细也匹配指标说明，以覆盖其内嵌指标。
新增类别时，在 CLI 同步增加解释和选择键。
普通字段无需逐一复制验证 schema。

## 输出契约

查询和记忆结果顶层新增 sourceSemantics：

| 字段 | 含义 |
|---|---|
| formatVersion | 说明结构版本，目前为 1 |
| source | health-data |
| dataSchemaVersion | 对应数据格式，目前为 3 |
| revision | 完整内嵌资源的 SHA256，解释变化自动更新 |
| sections[].type | 数据类别，common 是公共说明 |
| sections[].matchKeys | 该类别在数据对象上的字段名 |
| sections[].rules | 字段定义、解释边界和使用规则 |

说明属于元数据，不是用户观测。
它不进入数据哈希、分页摘要或消费检查点。
同一 pending token 继续返回原批次数据。
更换 CLI 后，其解释可更新，数据版本不变。

| 入口 | 选择范围 |
|---|---|
| health-data list/get | 当前返回页的类型，加公共说明 |
| health-data series | 返回指标的说明，加公共说明 |
| sync next | 当前批次最新快照中的类型 |
| sync scan / 空批次 | 只返回公共说明，没有观测明细 |
| health-data schema | 默认全部，可用 --type 选择多个类别 |

schema 无需健康数据已同步。
未知 --type 返回参数错误，不静默忽略。
无已知说明的新字段仍保留其原数据。
这不是完整 JSON Schema 校验接口。

## Harness 接入要求

文件读取路径可先调用 schema 获取完整目录。
按 matchKeys 选择说明并注入现有角色上下文。
选择逻辑不应另存一份健康类型枚举。
说明读取失败必须可见，不能静默缺失。
Harness 的缓存策略和失败处理由配套 PR 验证。

## 验证

隔离测试覆盖类型选择、摘要、分页和健康快照更新。
检查说明不进入证据、版本和消费状态。
检查 schema 在无健康数据时仍可查询。
两个 skill 保留流程，字段定义由返回说明提供。
本地测试不证明真实模型理解或云端集成效果。

健康批次仅返回当前 data，保留 previousVersion。
健康确认记录只需身份和版本，不再保存旧 data。
已有待确认批次在输出时也移除 previousData。
此投影不改 token、冻结数据或确认范围。
日历和提醒事项仍保留原有前后对照。
