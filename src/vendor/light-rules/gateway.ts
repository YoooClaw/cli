import type { OpenClawPluginApi } from "openclaw/plugin-sdk";
import type { Logger } from "../logger.js";
import { assertAncsRepeatTimes, normalizeRepeatTimes } from "../light/repeat.js";
import { validateSegments } from "../light/validators.js";
import { LIGHT_RULE_GATEWAY_METHODS } from "./names.js";
import type { LightSegment } from "../types.js";
import type { BroadcastFn } from "../update/types.js";
import type {
  LightRuleCreateParams,
  LightRuleUpdateParams,
  LightRuleDeleteParams,
} from "./types.js";
import { LightRuleError } from "./storage.js";
import type { LightRuleRegistry } from "./registry.js";

function resolveRuleIdentifier(params: unknown): string | undefined {
  if (!params || typeof params !== "object") return undefined;

  const raw = params as Record<string, unknown>;
  const candidates = [raw.name, raw.id, raw.ruleId, raw.ruleName];
  for (const candidate of candidates) {
    if (typeof candidate !== "string") continue;
    const normalized = candidate.trim().replace(/\.json$/i, "");
    if (normalized) return normalized;
  }

  return undefined;
}

export function registerLightRulesGateway(
  api: OpenClawPluginApi,
  registry: LightRuleRegistry,
  logger: Pick<Logger, "info" | "warn">,
  rememberBroadcast?: (broadcast: BroadcastFn | undefined) => void,
): void {
  type GatewayHandler = Parameters<OpenClawPluginApi["registerGatewayMethod"]>[1];

  const registerGatewayMethodWithBroadcastCapture = (
    method: string,
    handler: GatewayHandler,
  ): void => {
    api.registerGatewayMethod(method, (opts) => {
      rememberBroadcast?.(opts.context?.broadcast);
      return handler(opts);
    });
  };

  // lightrules.list
  registerGatewayMethodWithBroadcastCapture(LIGHT_RULE_GATEWAY_METHODS.list, async ({ respond }) => {
    try {
      registry.reload();
      const rules = registry.list().map((rule) => ({
        ...rule,
        id: rule.name,
      }));
      respond(true, { ok: true, rules });
    } catch (err: any) {
      logger.warn(`${LIGHT_RULE_GATEWAY_METHODS.list} failed: ${err?.message}`);
      respond(false, null, {
        code: "INTERNAL_ERROR",
        message: err?.message ?? "Unknown error",
      });
    }
  });

  // lightrules.create
  registerGatewayMethodWithBroadcastCapture(
    LIGHT_RULE_GATEWAY_METHODS.create,
    async ({ params, respond }) => {
      const { name, title, description, segments, repeat, repeat_times } =
        params as unknown as LightRuleCreateParams;
      const resolvedTitle = typeof title === "string" && title.trim() ? title.trim() : name;

      if (!name || typeof name !== "string") {
        respond(false, null, { code: "INVALID_PARAMS", message: "name is required" });
        return;
      }
      if (!description || typeof description !== "string") {
        respond(false, null, { code: "INVALID_PARAMS", message: "description is required" });
        return;
      }

      const validation = validateSegments(segments);
      if (!validation.valid) {
        respond(false, null, {
          code: "VALIDATION_FAILED",
          message: JSON.stringify(validation.errors),
        });
        return;
      }

      let repeatTimes: number;
      try {
        repeatTimes = normalizeRepeatTimes({ repeat, repeat_times });
        assertAncsRepeatTimes(repeatTimes);
      } catch (err: any) {
        respond(false, null, {
          code: "VALIDATION_FAILED",
          message: err?.message ?? "Unknown error",
        });
        return;
      }

      try {
        const result = await registry.create({
          name,
          title: resolvedTitle,
          description,
          segments: validation.segments,
          repeat_times: repeatTimes,
        });
        logger.info(`Light rule created: ${name}`);
        respond(true, {
          ok: true,
          id: result.meta.name,
          name: result.meta.name,
          title: result.meta.title,
          rule: result.meta,
        });
      } catch (err: any) {
        if (err instanceof LightRuleError) {
          respond(false, null, { code: err.code, message: err.message });
        } else {
          logger.warn(`${LIGHT_RULE_GATEWAY_METHODS.create} failed: ${err?.message}`);
          respond(false, null, {
            code: "INTERNAL_ERROR",
            message: err?.message ?? "Unknown error",
          });
        }
      }
    },
  );

  // lightrules.update
  registerGatewayMethodWithBroadcastCapture(
    LIGHT_RULE_GATEWAY_METHODS.update,
    async ({ params, respond }) => {
      const { title, description, segments, repeat, repeat_times, enabled } =
        params as unknown as LightRuleUpdateParams;
      const name = resolveRuleIdentifier(params);
      const resolvedTitle = typeof title === "string" ? title.trim() : undefined;

      if (!name) {
        respond(false, null, {
          code: "INVALID_PARAMS",
          message: "name is required (or provide id/ruleId/ruleName)",
        });
        return;
      }
      if (title !== undefined && !resolvedTitle) {
        respond(false, null, {
          code: "INVALID_PARAMS",
          message: "title must be a non-empty string",
        });
        return;
      }
      if (description !== undefined && typeof description !== "string") {
        respond(false, null, {
          code: "INVALID_PARAMS",
          message: "description must be a string",
        });
        return;
      }

      let validatedSegments: LightSegment[] | undefined;
      if (segments !== undefined) {
        const validation = validateSegments(segments);
        if (!validation.valid) {
          respond(false, null, {
            code: "VALIDATION_FAILED",
            message: JSON.stringify(validation.errors),
          });
          return;
        }
        validatedSegments = validation.segments;
      }

      let repeatTimes: number | undefined;
      if (repeat !== undefined || repeat_times !== undefined) {
        try {
          repeatTimes = normalizeRepeatTimes({ repeat, repeat_times });
          assertAncsRepeatTimes(repeatTimes);
        } catch (err: any) {
          respond(false, null, {
            code: "VALIDATION_FAILED",
            message: err?.message ?? "Unknown error",
          });
          return;
        }
      }

      try {
        const result = await registry.update({
          name,
          title: resolvedTitle,
          description,
          segments: validatedSegments,
          repeat_times: repeatTimes,
          enabled,
        });
        logger.info(`Light rule updated: ${name}`);
        respond(true, {
          ok: true,
          id: result.meta.name,
          name: result.meta.name,
          title: result.meta.title,
          updated: true,
          rule: result.meta,
        });
      } catch (err: any) {
        if (err instanceof LightRuleError) {
          respond(false, null, { code: err.code, message: err.message });
        } else {
          logger.warn(`${LIGHT_RULE_GATEWAY_METHODS.update} failed: ${err?.message}`);
          respond(false, null, {
            code: "INTERNAL_ERROR",
            message: err?.message ?? "Unknown error",
          });
        }
      }
    },
  );

  // lightrules.delete
  registerGatewayMethodWithBroadcastCapture(
    LIGHT_RULE_GATEWAY_METHODS.delete,
    async ({ params, respond }) => {
      const name = resolveRuleIdentifier(params as unknown as LightRuleDeleteParams);

      if (!name) {
        respond(false, null, {
          code: "INVALID_PARAMS",
          message: "name is required (or provide id/ruleId/ruleName)",
        });
        return;
      }
      try {
        const result = await registry.delete(name);
        logger.info(`Light rule deleted: ${result.name}`);
        respond(true, {
          ok: true,
          id: result.name,
          name: result.name,
          deleted: true,
        });
      } catch (err: any) {
        if (err instanceof LightRuleError) {
          respond(false, null, { code: err.code, message: err.message });
        } else {
          logger.warn(`${LIGHT_RULE_GATEWAY_METHODS.delete} failed: ${err?.message}`);
          respond(false, null, {
            code: "INTERNAL_ERROR",
            message: err?.message ?? "Unknown error",
          });
        }
      }
    },
  );
}
