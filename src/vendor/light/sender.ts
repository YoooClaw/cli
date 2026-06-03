import { randomUUID } from "node:crypto";
import { buildLightEffectApnsBody } from "./protocol.js";
import { getEnvUrls } from "../env.js";
import type { LightSegment } from "../types.js";
import type { RepeatArgument } from "./repeat.js";

export interface SendLightResult {
  ok: boolean;
  bizUniqueId?: string;
  response?: unknown;
  status?: number;
  error?: string;
}

export async function sendLightEffect(
  apiKey: string,
  segments: LightSegment[],
  logger?: { info: (msg: string) => void; warn: (msg: string) => void },
  repeatInput?: RepeatArgument,
  reason?: string,
  title?: string,
): Promise<SendLightResult> {
  const apiUrl = getEnvUrls().lightApiUrl;
  // 后端要求 appKey / templateId 写死在代码里（不再从 env 注入）
  const appKey = "phone-notifications";
  const templateId = "1990771146010017800";
  const resolvedTitle = resolveLightTitle(title, reason, segments);

  logger?.info(
    `Light sender: apiUrl=${apiUrl ?? "UNSET"}, appKey=${appKey ? appKey.substring(0, 8) + "…" : "UNSET"}, templateId=${templateId ?? "UNSET"}, apiKey=${apiKey ? apiKey.substring(0, 20) + "…" : "EMPTY"}, title=${resolvedTitle}, reason=${reason ?? ""}, segments=${JSON.stringify(segments)}`,
  );

  if (!apiUrl) {
    return {
      ok: false,
      error: "灯效 API 未配置，请确认构建时已封装 OPENCLAW_HOST_*",
    };
  }

  let bizContent: string;
  try {
    bizContent = buildLightEffectApnsBody(segments, repeatInput, reason);
  } catch (error: any) {
    return { ok: false, error: error?.message ?? String(error) };
  }
  const bizUniqueId = randomUUID();

  const requestBody = {
    appKey,
    bizMap: { noticeType: "APP_NOTIFICATION_IMPORTANT", title: resolvedTitle, reason },
    bizUniqueId,
    paramsMap: { bizContent },
    pushType: "SPECIFY_PUSH",
    templateId,
  };

  logger?.info(
    `Light sender: POST ${apiUrl}, bizUniqueId=${bizUniqueId}, body=${JSON.stringify(requestBody).substring(0, 500)}`,
  );

  const res = await fetch(apiUrl, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Api-Key-Id": apiKey.startsWith("Bearer ")
        ? apiKey.slice("Bearer ".length)
        : apiKey,
    },
    body: JSON.stringify(requestBody),
  });

  const resBody = await res.text();
  if (!res.ok) {
    logger?.warn(
      `Light sender: FAILED ${res.status}, url=${apiUrl}, resBody=${resBody.substring(0, 500)}`,
    );
    return { ok: false, status: res.status, error: resBody };
  }

  logger?.info(`Light sender: OK bizUniqueId=${bizUniqueId}, resBody=${resBody.substring(0, 200)}`);
  return { ok: true, bizUniqueId, response: JSON.parse(resBody) };
}

function resolveLightTitle(
  title: string | undefined,
  reason: string | undefined,
  segments: LightSegment[],
): string {
  const trimmedTitle = title?.trim();
  if (trimmedTitle) return trimmedTitle;

  const trimmedReason = reason?.trim();
  if (trimmedReason) return trimmedReason;

  const modeDesc = segments.map((segment) => segment.mode).join("+");
  return `Effect: ${modeDesc || "custom"}`;
}
