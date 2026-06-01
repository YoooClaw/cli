/**
 * daemon /light/send 实际投递路径。
 *
 * 把 { segments | preset | rule } 解析成最终的 segments + repeat_times，再调
 * phone-notifications 的 sendLightEffect 走云 API（需要 LIGHT_APP_KEY /
 * LIGHT_TEMPLATE_ID 进程环境变量 + OPENCLAW_HOST_* 决定的 apiUrl）。
 *
 * 优先级：rule > preset > segments。用户显式传 --repeat / --repeat-times 时
 * 会覆盖 rule/preset 自带的 repeat_times。
 */
import {
  LIGHT_PRESETS_V1,
  sendLightEffect,
  validateSegments,
  normalizeRepeatTimes,
  assertAncsRepeatTimes,
  type LightSegment,
  type LightRuleRegistry,
} from "../shared.js";
import { resolveApiKey } from "../credentials/store.js";
import type { DaemonLogger } from "./logger.js";

/** 连通性测试用的 preset id（`+blink` shortcut 映射到这里）。 */
export const BLINK_PRESET_ID = "red-strobe-3";

export interface LightSendInput {
  segments?: unknown;
  preset?: string;
  rule?: string;
  repeat?: boolean;
  repeat_times?: number;
}

export interface LightSendOutcome {
  ok: boolean;
  status: number;
  body: Record<string, unknown>;
}

interface ResolvedSource {
  segments: LightSegment[];
  repeat_times: number;
  source: "segments" | "preset" | "rule";
  reason: string;
  title?: string;
}

export async function performLightSend(
  input: LightSendInput,
  deps: { logger: DaemonLogger; registry: LightRuleRegistry },
): Promise<LightSendOutcome> {
  let resolved: ResolvedSource;
  try {
    resolved = resolveSource(input, deps.registry);
  } catch (err) {
    return errorOutcome(400, "INVALID_PARAMS", (err as Error).message);
  }

  // 用户显式传 --repeat / --repeat-times 时覆盖 rule/preset 自带的值
  const overrideRepeat = input.repeat !== undefined || input.repeat_times !== undefined;
  let repeatTimes = resolved.repeat_times;
  if (overrideRepeat) {
    try {
      repeatTimes = normalizeRepeatTimes({
        repeat: input.repeat,
        repeat_times: input.repeat_times,
      });
    } catch (err) {
      return errorOutcome(400, "VALIDATION_FAILED", (err as Error).message);
    }
  }
  try {
    assertAncsRepeatTimes(repeatTimes);
  } catch (err) {
    return errorOutcome(400, "VALIDATION_FAILED", (err as Error).message);
  }

  const apiKey = resolveApiKey();
  if (!apiKey.value) {
    return errorOutcome(
      401,
      "AUTH_REQUIRED",
      "未配置 api-key，运行 `yoooclaw auth set-api-key <ock_…>`",
    );
  }

  deps.logger.info(
    `/light/send 解析完成 source=${resolved.source} reason=${resolved.reason} ` +
      `segments=${resolved.segments.length} repeat_times=${repeatTimes}`,
  );

  const result = await sendLightEffect(
    apiKey.value,
    resolved.segments,
    deps.logger,
    { repeat_times: repeatTimes },
    resolved.reason,
    resolved.title,
  );

  if (!result.ok) {
    return errorOutcome(
      result.status ?? 502,
      "LIGHT_SEND_FAILED",
      result.error ?? "灯效投递失败",
    );
  }

  return {
    ok: true,
    status: 200,
    body: {
      ok: true,
      accepted: true,
      delivered: true,
      source: resolved.source,
      bizUniqueId: result.bizUniqueId,
      segments: resolved.segments,
      repeat_times: repeatTimes,
      response: result.response,
    },
  };
}

function resolveSource(
  input: LightSendInput,
  registry: LightRuleRegistry,
): ResolvedSource {
  if (input.rule) {
    const meta = registry.get(input.rule);
    if (!meta) throw new Error(`规则不存在：${input.rule}`);
    return {
      segments: meta.segments,
      repeat_times: meta.repeat_times,
      source: "rule",
      reason: `rule:${meta.name}`,
      title: meta.title,
    };
  }

  if (input.preset) {
    const preset = LIGHT_PRESETS_V1.presets.find((p) => p.presetId === input.preset);
    if (!preset) {
      const available = LIGHT_PRESETS_V1.presets.map((p) => p.presetId).join(", ");
      throw new Error(`未知 preset：${input.preset}（可用：${available}）`);
    }
    return {
      segments: preset.segments,
      repeat_times: preset.repeat_times,
      source: "preset",
      reason: `preset:${preset.presetId}`,
      title: preset.displayName,
    };
  }

  if (input.segments !== undefined) {
    const validation = validateSegments(input.segments);
    if (!validation.valid) {
      throw new Error(JSON.stringify(validation.errors));
    }
    return {
      segments: validation.segments,
      repeat_times: 1,
      source: "segments",
      reason: "manual",
    };
  }

  throw new Error("需要 segments / preset / rule 之一");
}

function errorOutcome(status: number, code: string, message: string): LightSendOutcome {
  return {
    ok: false,
    status,
    body: { ok: false, error: { code, message } },
  };
}
