import type { LightSegment } from "../types.js";

export interface LightRuleMeta {
  /** 内部唯一标识符（用于 tasks 目录名、规则查找和更新/删除） */
  name: string;
  /** 人类可读的规则名；应是对 description 的简短总结 */
  title: string;
  type: "light-rule";
  /**
   * 自然语言触发条件 + 规则用途。事件驱动方案下，这是 LLM 判断命中的
   * **唯一权威输入**，必须由用户用自己的话写。
   */
  description: string;
  segments: LightSegment[];
  repeat_times: number;
  enabled: boolean;
  createdAt: string;
  updatedAt?: string;
}

export interface LightRuleCreateParams {
  name: string;
  title: string;
  description: string;
  segments: unknown;
  repeat_times?: number;
  repeat?: boolean;
}

export interface LightRuleUpdateParams {
  name: string;
  title?: string;
  description?: string;
  segments?: unknown;
  repeat_times?: number;
  repeat?: boolean;
  enabled?: boolean;
}

export interface LightRuleDeleteParams {
  name: string;
}
