/**
 * 命令树声明 —— Service-oriented，对齐 lark-cli 形态与本仓 PRD「命令树总览」。
 *
 * 这是命令树的**单一事实来源**：program.ts 据此构建 commander 程序（结构 + options），
 * 实现体由 commands/registry.ts 按 path 提供；未注册的 path 回落 notImplemented 占位。
 *
 * daemon 依赖标记（仅文档用途，体现在 summary 里）：
 *   🟢 不需要 daemon  🟡 需要 daemon 在跑  🔵 进程类（管理 daemon 自身）
 */

export interface OptionSpec {
  /** commander flags，如 `--from <iso8601>`、`--follow` */
  flags: string;
  summary: string;
  /** 默认值（仅对带值的 option 有意义） */
  default?: string;
}

export interface SubcommandSpec {
  /** 子命令名 + 位置参数，如 `show <id>`、`api <method> <path>` */
  name: string;
  summary: string;
  options?: OptionSpec[];
}

export interface ShortcutSpec {
  /** 含 `+` 前缀的快捷命令名，如 `+today` */
  name: string;
  summary: string;
  options?: OptionSpec[];
}

export interface ServiceSpec {
  name: string;
  summary: string;
  /** 子命令列表。leaf 命令（直接执行，无子命令）省略此项并提供 `args`。 */
  subcommands?: SubcommandSpec[];
  /** leaf 命令的位置参数，如 `[keyword]`、`<method> <path>`。 */
  args?: string;
  /** leaf 命令的 options。 */
  options?: OptionSpec[];
  shortcuts?: ShortcutSpec[];
}

const NOTIFICATION_QUERY_OPTIONS: OptionSpec[] = [
  { flags: "--from <iso8601>", summary: "开始时间，如 2026-03-01T09:00:00+08:00" },
  { flags: "--to <iso8601>", summary: "结束时间" },
  { flags: "--app <name>", summary: "按应用过滤（支持中英文别名）" },
  { flags: "--sender <name>", summary: "按发送人/标题过滤" },
  { flags: "--conversation-type <type>", summary: "会话类型 group|private" },
  { flags: "--keyword <text>", summary: "在标题/内容/发送人/会话名中搜索" },
  { flags: "--client <label>", summary: "按 clientLabel 过滤；all 为全部" },
  { flags: "--limit <n>", summary: "最大返回条数", default: "100" },
];

export const COMMAND_TREE: ServiceSpec[] = [
  {
    name: "config",
    summary: "配置管理 🟢",
    subcommands: [
      {
        name: "init",
        summary: "交互式首次向导，生成 config + gateway token 并启动 daemon",
        options: [
          { flags: "--non-interactive", summary: "跳过向导（配合 --from-file）" },
          { flags: "--from-file <path>", summary: "从 JSON 文件导入配置（- 为 stdin）" },
          { flags: "--force", summary: "已存在 config 时覆盖" },
          { flags: "--no-start", summary: "只生成配置，不自动启动 daemon" },
        ],
      },
      {
        name: "show",
        summary: "显示当前 profile 配置（敏感字段遮罩）",
        options: [{ flags: "--show-secrets", summary: "输出敏感字段明文（需 TTY）" }],
      },
      { name: "set <key> <value>", summary: "设置单个配置项（点号路径）" },
      { name: "unset <key>", summary: "删除单个配置项" },
    ],
  },
  {
    name: "profile",
    summary: "多 profile 管理 🟢",
    subcommands: [
      { name: "list", summary: "列出所有 profile，标注 active" },
      { name: "use <name>", summary: "切换 active profile" },
      { name: "create <name>", summary: "新建 profile（走 config init 向导）" },
      {
        name: "delete <name>",
        summary: "删除 profile（非 active，需 --yes）",
        options: [{ flags: "--yes", summary: "跳过确认" }],
      },
    ],
  },
  {
    name: "auth",
    summary: "凭据与鉴权 🟢/🟡",
    subcommands: [
      {
        name: "set-api-key <key>",
        summary: "设置/轮换 account 级 default api-key（- 从 stdin 读）🟢",
        options: [{ flags: "--keychain", summary: "写入 OS keychain 而非文件" }],
      },
      {
        name: "add-api-key <key>",
        summary: "新增一条 multi-key api-key（- 从 stdin 读）🟢",
        options: [
          { flags: "--label <label>", summary: "api-key label（[a-z0-9-]{1,32}）" },
          { flags: "--default", summary: "设为 default key" },
          { flags: "--force", summary: "label 已存在时覆盖" },
        ],
      },
      { name: "list-api-keys", summary: "列出 api-key 条目（key 自动遮罩）🟢" },
      { name: "remove-api-key <label>", summary: "删除指定 label 的 api-key 🟢" },
      { name: "set-default-api-key <label>", summary: "切换 default api-key 🟢" },
      {
        name: "token-rotate",
        summary: "生成新 gateway token；daemon 在跑时随后 restart 生效 🟢",
        options: [{ flags: "--length <n>", summary: "token 字节长度", default: "32" }],
      },
      { name: "status", summary: "显示鉴权状态（本地检查，不调 daemon）🟢" },
      { name: "check", summary: "端到端鉴权体检（调 daemon /daemon/status）🟡" },
    ],
  },
  {
    name: "daemon",
    summary: "守护进程管理 🔵",
    subcommands: [
      {
        name: "start",
        summary: "启动 daemon（默认后台 detach）",
        options: [
          { flags: "--bind <host>", summary: "监听地址（默认 config.daemon.bind）" },
          { flags: "--port <n>", summary: "监听端口（默认 config.daemon.port）" },
          { flags: "--no-detach", summary: "前台运行（systemd/launchd 用）" },
          { flags: "--log-level <level>", summary: "error|warn|info|debug|trace" },
        ],
      },
      { name: "stop", summary: "停止 daemon（SIGTERM → 10s → SIGKILL）" },
      { name: "restart", summary: "stop + start，保留原启动参数" },
      { name: "reload", summary: "重读凭据并增量刷新 Relay 隧道" },
      { name: "status", summary: "打印 daemon 状态（PID/端口/relay/规则数...）" },
      {
        name: "logs",
        summary: "跟踪 daemon 日志",
        options: [
          { flags: "-f, --follow", summary: "持续 tail" },
          { flags: "--lines <n>", summary: "初始展示行数", default: "100" },
          { flags: "--level <level>", summary: "过滤日志级别" },
        ],
      },
      {
        name: "run-foreground",
        summary: "（内部）前台运行 daemon 主循环，供 detach 子进程调用",
        options: [
          { flags: "--bind <host>", summary: "监听地址" },
          { flags: "--port <n>", summary: "监听端口" },
          { flags: "--log-level <level>", summary: "日志级别" },
        ],
      },
    ],
  },
  {
    name: "notification",
    summary: "通知查询 🟢",
    subcommands: [
      { name: "search", summary: "按筛选条件查询通知，时间倒序", options: NOTIFICATION_QUERY_OPTIONS },
      {
        name: "summary",
        summary: "聚合统计 + 样例摘要，供 Agent 总结",
        options: [
          ...NOTIFICATION_QUERY_OPTIONS,
          { flags: "--sample <n>", summary: "返回最近样例条数", default: "30" },
          { flags: "--top <n>", summary: "聚合榜单条数", default: "10" },
        ],
      },
      {
        name: "stats",
        summary: "按维度聚合统计",
        options: [
          { flags: "--from <date>", summary: "YYYY-MM-DD，默认 7 天前" },
          { flags: "--to <date>", summary: "YYYY-MM-DD，默认今天" },
          { flags: "--app <name>", summary: "仅统计指定应用" },
          { flags: "--client <label>", summary: "按 clientLabel 过滤；all 为全部" },
          { flags: "--dim <dim>", summary: "date|app|sender|hour|client|all", default: "all" },
        ],
      },
      { name: "storage-path", summary: "打印 notifications 目录绝对路径" },
    ],
    shortcuts: [
      { name: "+today", summary: "今日通知摘要", options: [{ flags: "--client <label>", summary: "按 clientLabel 过滤；all 为全部" }] },
      { name: "+recent", summary: "最近 1 小时通知", options: [{ flags: "--client <label>", summary: "按 clientLabel 过滤；all 为全部" }] },
      { name: "+unread", summary: "（预留）未读通知" },
    ],
  },
  {
    name: "sync",
    summary: "通知同步给记忆系统 🟢",
    subcommands: [
      { name: "scan", summary: "扫描未处理通知，返回各日期待同步摘要" },
      {
        name: "fetch",
        summary: "获取指定日期未处理通知详情",
        options: [
          { flags: "--date <YYYY-MM-DD>", summary: "目标日期（必填）" },
          { flags: "--max-end-index <index>", summary: "本次快照允许读取的最大 endIndex" },
        ],
      },
      {
        name: "commit",
        summary: "标记指定日期当前批次处理完成",
        options: [
          { flags: "--date <YYYY-MM-DD>", summary: "目标日期（必填）" },
          { flags: "--end-index <index>", summary: "本批次 fetch 返回的 endIndex" },
        ],
      },
    ],
  },
  {
    name: "recording",
    summary: "录音查询与 ASR 配置 🟢",
    subcommands: [
      {
        name: "list",
        summary: "列出所有录音 🟢",
        options: [
          { flags: "--status <status>", summary: "按传输状态过滤" },
          { flags: "--client <label>", summary: "按 clientLabel 过滤；all 为全部" },
        ],
      },
      { name: "status <id>", summary: "查看单条录音详情 🟢" },
      { name: "storage-path", summary: "打印录音存储目录绝对路径 🟢" },
      {
        name: "setup-asr",
        summary: "交互式配置 ASR 转写参数 🟢",
        options: [
          { flags: "--mode <mode>", summary: "api | local（默认 api）" },
          { flags: "--api-key <key>", summary: "api 模式：可选；留空则用 account ock- key" },
          { flags: "--endpoint <url>", summary: "api 模式：自定义 model-proxy 端点" },
          { flags: "--language <lang>", summary: "api 模式：语言提示，如 zh / en / auto" },
          { flags: "--model <name>", summary: "local 模式：Whisper 模型名" },
          { flags: "--non-interactive", summary: "跳过向导，从参数构造配置" },
        ],
      },
      {
        name: "events",
        summary: "查询录音状态事件流（JSONL） 🟢",
        options: [
          { flags: "--id <recordingId>", summary: "只看某条录音的事件" },
          { flags: "--since <duration>", summary: "回看时间窗，如 10m / 1h / 24h" },
          { flags: "--watch", summary: "持续跟随新事件（tail -f）" },
          { flags: "--limit <n>", summary: "返回条数上限", default: "200" },
        ],
      },
    ],
    shortcuts: [{ name: "+latest", summary: "展示最新一条录音详情" }],
  },
  {
    name: "image",
    summary: "图片管理 🟢",
    subcommands: [
      {
        name: "list",
        summary: "列出所有图片",
        options: [
          { flags: "--status <status>", summary: "syncing|synced|sync_failed" },
          { flags: "--app <name>", summary: "按来源应用过滤" },
          { flags: "--client <label>", summary: "按 clientLabel 过滤；all 为全部" },
          { flags: "--from <iso8601>", summary: "created_at 起" },
          { flags: "--to <iso8601>", summary: "created_at 止" },
          { flags: "--limit <n>", summary: "最大返回条数", default: "100" },
        ],
      },
      { name: "status <id>", summary: "查看单张图片详情" },
      {
        name: "path <id>",
        summary: "打印图片本地文件绝对路径",
        options: [{ flags: "--thumbnail", summary: "返回缩略图路径（若有）" }],
      },
      { name: "storage-path", summary: "打印图片存储目录绝对路径" },
    ],
    shortcuts: [{ name: "+latest", summary: "展示最新一张图片详情" }],
  },
  {
    name: "light",
    summary: "灯效硬件控制 🟡",
    subcommands: [
      {
        name: "send",
        summary: "发送灯效指令到硬件（--segments / --preset / --rule 三选一）",
        options: [
          { flags: "--segments <json>", summary: "灯效参数 JSON（原始段）" },
          { flags: "--preset <name>", summary: "内置预设 id（如 red-steady / red-strobe-3）" },
          { flags: "--rule <name>", summary: "已保存的 lightrule 名" },
          { flags: "--repeat", summary: "无限循环播放（覆盖来源默认值）" },
          { flags: "--repeat-times <n>", summary: "整条组合重复次数（0=无限，覆盖来源默认值）" },
        ],
      },
    ],
    shortcuts: [{ name: "+blink", summary: "灯效连通性测试（red-strobe-3）" }],
  },
  {
    name: "lightrule",
    summary: "灯效规则管理 🟡",
    subcommands: [
      { name: "list", summary: "列出所有规则及状态" },
      { name: "show <id>", summary: "查看单条规则详情" },
      {
        name: "create",
        summary: "创建规则（--from-file / --intent ...）",
        options: [
          { flags: "--from-file <path>", summary: "从 JSON 读规则（- 为 stdin）" },
          { flags: "--name <text>", summary: "规则名" },
          { flags: "--intent <text>", summary: "自然语言意图描述" },
          { flags: "--light-action <json>", summary: "命中后的 light 动作 JSON" },
          { flags: "--match-rules <json>", summary: "前置硬过滤规则 JSON" },
        ],
      },
      {
        name: "update <id>",
        summary: "更新现有规则",
        options: [
          { flags: "--from-file <path>", summary: "从 JSON 读规则（- 为 stdin）" },
          { flags: "--name <text>", summary: "规则名" },
          { flags: "--intent <text>", summary: "自然语言意图描述" },
          { flags: "--light-action <json>", summary: "命中后的 light 动作 JSON" },
          { flags: "--match-rules <json>", summary: "前置硬过滤规则 JSON" },
        ],
      },
      {
        name: "delete <id>",
        summary: "删除规则（--yes）",
        options: [{ flags: "--yes", summary: "跳过确认" }],
      },
      { name: "enable <id>", summary: "启用单条规则" },
      { name: "disable <id>", summary: "停用单条规则" },
    ],
    shortcuts: [
      { name: "+on", summary: "启用所有规则" },
      { name: "+off", summary: "停用所有规则（保留定义）" },
    ],
  },
  {
    name: "monitor",
    summary: "定时通知监控任务 🟡",
    subcommands: [
      { name: "list", summary: "列出所有监控任务" },
      { name: "show <name>", summary: "查看监控任务详情" },
      {
        name: "create <name>",
        summary: "创建监控任务（cron 驱动）",
        options: [
          { flags: "--description <text>", summary: "任务描述（必填）" },
          { flags: "--match-rules <json>", summary: "匹配规则 JSON（必填）" },
          { flags: "--schedule <cron>", summary: "cron 表达式（必填）" },
        ],
      },
      {
        name: "delete <name>",
        summary: "删除监控任务（--yes）",
        options: [{ flags: "--yes", summary: "跳过确认" }],
      },
      { name: "enable <name>", summary: "启用监控任务" },
      { name: "disable <name>", summary: "暂停监控任务" },
    ],
  },
  {
    name: "tunnel",
    summary: "Relay 隧道 🟡",
    subcommands: [
      {
        name: "status",
        summary: "查询 Relay 连接状态",
        options: [{ flags: "--client <label>", summary: "只看指定 clientLabel；all 为全部" }],
      },
      {
        name: "reconnect",
        summary: "强制重连",
        options: [{ flags: "--client <label>", summary: "只重连指定 clientLabel" }],
      },
    ],
    shortcuts: [
      {
        name: "+test",
        summary: "daemon 本地 ingest + 鉴权自检（echo 回环）",
        options: [{ flags: "--client <label>", summary: "用指定 clientLabel 的 api-key 做回环写入" }],
      },
    ],
  },
  {
    name: "log",
    summary: "日志检索 🟢",
    args: "[keyword]",
    options: [
      { flags: "--from <date>", summary: "YYYY-MM-DD，默认 7 天前" },
      { flags: "--to <date>", summary: "YYYY-MM-DD，默认今天" },
      { flags: "--limit <n>", summary: "最大返回条数", default: "50" },
      { flags: "--level <level>", summary: "过滤日志级别" },
    ],
    shortcuts: [{ name: "+errors", summary: "昨天起的 error 级日志" }],
  },
  {
    name: "gateway",
    summary: "协议自检 🟢/🟡",
    subcommands: [
      {
        name: "test",
        summary: "模拟手机端调 daemon /notifications，验证本地连通与鉴权 🟡",
        options: [
          { flags: "--from-phone-ip <ip>", summary: "模拟来源 IP" },
          { flags: "--via-relay", summary: "复用 tunnel +test 本地回环路径" },
        ],
      },
    ],
  },
  {
    name: "api",
    summary: "Raw HTTP escape hatch 🟡",
    args: "<method> <path>",
    options: [
      { flags: "--data <json>", summary: "请求体（@file 读文件、- 读 stdin）" },
      { flags: "--header <key:value>", summary: "追加 header（可重复）" },
    ],
  },
  {
    name: "migrate",
    summary: "从 openclaw 插件迁移数据 🟢",
    subcommands: [
      {
        name: "from-openclaw",
        summary: "迁移 notifications/recordings/规则/api-key 到 ~/.yoooclaw",
        options: [
          { flags: "--dry-run", summary: "只打印迁移计划，不写入" },
          { flags: "--source <path>", summary: "自定义源目录，默认 ~/.openclaw" },
        ],
      },
    ],
  },
  {
    name: "update",
    summary: "版本检查 🟢",
    subcommands: [
      {
        name: "self",
        summary: "检查 npm 最新版本并提示（不自动更新）",
        options: [
          { flags: "--beta", summary: "检查 beta channel" },
          { flags: "--json", summary: "只输出版本信息 JSON" },
        ],
      },
    ],
  },
  {
    name: "skills",
    summary: "Agent 技能管理：把随包发布的 SKILL.md 装到 agent 可发现目录 🟢",
    subcommands: [
      { name: "list", summary: "列出随 CLI 发布的内置 Skill 及其触发说明" },
      { name: "targets", summary: "列出支持的 Agent skills 目录与自动探测结果" },
      {
        name: "install",
        summary: "把内置 Skill 安装到 agent skills 目录（默认自动探测，软链）",
        options: [
          { flags: "--agent <agent>", summary: "安装目标 agent：auto|claude|codex|custom", default: "auto" },
          { flags: "--target <dir>", summary: "安装目标目录；传入后优先于自动探测" },
          { flags: "--copy", summary: "复制而非软链（升级后不随包自动更新）" },
          { flags: "--force", summary: "目标已存在同名 Skill 时覆盖" },
        ],
      },
    ],
  },
  {
    name: "doctor",
    summary: "本地环境自检：Node/目录/keychain/config/daemon 🟢",
    options: [
      { flags: "--json", summary: "JSON 输出（给脚本用）" },
      { flags: "--fix", summary: "自动修复可修复的问题" },
    ],
  },
];
