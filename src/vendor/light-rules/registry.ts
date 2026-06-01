/**
 * LightRuleRegistry —— 灯效规则的内存索引 + CRUD 包装
 *
 * 角色定位：
 *  - 在事件驱动重构方案下，每条通知到达时都需要拿到"当前 enabled 的全部规则"
 *    用来拼装 Agent 的 system prompt。如果每次都扫盘 N 个 meta.json，
 *    5 秒延迟预算会被磨穿，所以引入这个常驻内存的 index。
 *  - 同时它是 storage.ts 的 CRUD 包装：所有 create/update/delete 都会先落盘、
 *    再更新内存 Map，保证两者一致。
 *
 * 本类**不**做规则匹配 —— 匹配交给 Agent。registry 只负责"有哪些规则"和
 * "按需拼装 prompt"。
 *
 * 写路径用一个 promise 链做串行化（最朴素的 mutex），保证并发 CRUD
 * 不会出现"内存和磁盘交错"的状态。
 *
 * 见 prd-lightrules-event-driven-refactor.md §数据模型 / §触发流程
 */

import {
  createLightRule,
  deleteLightRule,
  listLightRules,
  updateLightRule,
  type LightRuleStorageContext,
} from "./storage.js";
import type { LightRuleMeta } from "./types.js";

export { LightRuleError } from "./storage.js";

type CreateParams = Parameters<typeof createLightRule>[1];
type UpdateParams = Parameters<typeof updateLightRule>[1];
type CreateResult = ReturnType<typeof createLightRule>;
type UpdateResult = ReturnType<typeof updateLightRule>;
type DeleteResult = ReturnType<typeof deleteLightRule>;

export class LightRuleRegistry {
  private readonly ctx: LightRuleStorageContext;
  /** name → meta 的内存索引；落盘成功后才更新 */
  private readonly index = new Map<string, LightRuleMeta>();
  /** 写路径串行化锁：每次 mutate 都 chain 在前一次之后 */
  private writeChain: Promise<unknown> = Promise.resolve();

  constructor(ctx: LightRuleStorageContext) {
    this.ctx = ctx;
    this.reload();
  }

  /**
   * 从磁盘重新加载全部规则到内存。仅在构造时和外部显式触发时使用。
   * 正常 CRUD 路径不应该调用本方法 —— 通过 create/update/delete 增量维护即可。
   */
  reload(): void {
    this.index.clear();
    for (const meta of listLightRules(this.ctx)) {
      this.index.set(meta.name, meta);
    }
  }

  /** 全部规则（包含 disabled），调用方不要改返回值。 */
  list(): LightRuleMeta[] {
    return Array.from(this.index.values());
  }

  /** 仅 enabled 的规则。事件驱动评估链路使用。 */
  getEnabled(): LightRuleMeta[] {
    return this.list().filter((rule) => rule.enabled);
  }

  /** 按名字精确查找；不存在返回 null。 */
  get(name: string): LightRuleMeta | null {
    return this.index.get(name) ?? null;
  }

  /**
   * 创建规则。落盘成功后再写入内存索引。
   * 失败时 storage 抛 LightRuleError，内存索引保持不变。
   */
  async create(params: CreateParams): Promise<CreateResult> {
    return this.runExclusive(() => {
      const result = createLightRule(this.ctx, params);
      this.index.set(result.meta.name, result.meta);
      return result;
    });
  }

  /**
   * 更新规则。落盘成功后再刷新内存条目。
   */
  async update(params: UpdateParams): Promise<UpdateResult> {
    return this.runExclusive(() => {
      const result = updateLightRule(this.ctx, params);
      this.index.set(result.meta.name, result.meta);
      return result;
    });
  }

  /**
   * 删除规则。落盘成功后再从内存索引移除。
   */
  async delete(name: string): Promise<DeleteResult> {
    return this.runExclusive(() => {
      const result = deleteLightRule(this.ctx, name);
      this.index.delete(result.name);
      return result;
    });
  }

  /**
   * 拼装 Agent 评估用的 system prompt。
   *
   * 当前实现是一个最小可用骨架：列出全部 enabled 规则的 title + name + description。
   * M3 接入真实 Agent 链路时，会按 prd-lightrules-event-driven-refactor.md §Prompt 拼装
   * 的模板补齐"任务说明 / 输出约束"等段落。
   *
   * 现在先把方法签名冻结，避免后续散落在多个调用点。
   */
  buildSystemPrompt(): string {
    const enabled = this.getEnabled();
    if (enabled.length === 0) {
      return "当前没有启用任何灯效规则。";
    }

    const lines: string[] = [
      '你是一个判断"通知 → 灯效规则"是否命中的助手。',
      "",
      "下面是用户当前启用的全部灯效规则（按创建时间倒序）：",
      "",
    ];

    const sorted = [...enabled].sort((a, b) =>
      (b.createdAt ?? "").localeCompare(a.createdAt ?? ""),
    );

    sorted.forEach((rule, idx) => {
      lines.push(
        `[${idx + 1}] title: ${rule.title}`,
        `    name: ${rule.name}`,
        `    description: ${rule.description}`,
        "",
      );
    });

    lines.push(
      '任务：根据用户接下来发给你的"通知"内容，判断哪些规则被命中。',
      "- 命中 0 条：不调用任何工具，直接结束。",
      "- 命中 1 条或多条：对每一条命中的规则调用一次 trigger_light(rule_name)。",
      "- 不要回复任何自由文本；判定结果**只**通过 tool calling 表达。",
      '- 拿不准的时候倾向于"不触发"，避免骚扰用户。',
    );

    return lines.join("\n");
  }

  /**
   * 把一段同步操作排队进 write chain，保证 mutate 串行执行。
   *
   * 用 promise 链做 mutex 是最简单的方案：每次 runExclusive 都把
   * `writeChain` 推进一步。即使前一个任务失败，链也会继续往下走，
   * 后续任务不会被永久阻塞。
   */
  private runExclusive<T>(fn: () => T): Promise<T> {
    const next = this.writeChain.then(
      () => fn(),
      () => fn(),
    );
    // 不让链上的失败传播下去（已在 then 的 onRejected 回调里吸收）
    this.writeChain = next.catch(() => undefined);
    return next;
  }
}
