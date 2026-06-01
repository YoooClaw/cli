/**
 * StandaloneRuntime —— 对外暴露**结构兼容** OpenClawPluginApi 的子集，
 * 让 phone-notifications 的注册模块（registerNotificationInterfaces /
 * registerLightRulesGateway 等）**不改代码**就能在 daemon 里跑。
 *
 * 实现侧：把注册进来的 HTTP 路由与 gateway 方法收集到 Map，由 daemon HTTP server 派发。
 * 不属于 CLI 形态的能力（registerTool / registerCli / on('before_prompt_build')）做 no-op。
 */
import type { IncomingMessage, ServerResponse } from "node:http";
import { runWithIngestContext, type IngestContext } from "../shared.js";

export type HttpAuth = "gateway" | "public" | string;
export interface HttpRouteSpec {
  path: string;
  auth?: HttpAuth;
  handler: (req: IncomingMessage, res: ServerResponse) => void | Promise<void>;
}

export type GatewayRespond = (
  ok: boolean,
  data?: unknown,
  error?: { code: string; message: string },
) => void;

export interface GatewayOpts {
  params?: unknown;
  respond: GatewayRespond;
  context?: {
    broadcast?: (event: string, payload: unknown) => void;
  } & IngestContext;
  client?: unknown;
}
export type GatewayHandler = (opts: GatewayOpts) => void | Promise<void>;

export interface GatewayResult {
  ok: boolean;
  data?: unknown;
  error?: { code: string; message: string };
}

interface RuntimeLogger {
  debug: (m: string) => void;
  info: (m: string) => void;
  warn: (m: string) => void;
  error: (m: string) => void;
}

export class StandaloneRuntime {
  readonly httpRoutes = new Map<string, HttpRouteSpec>();
  readonly gatewayMethods = new Map<string, GatewayHandler>();
  readonly services: string[] = [];

  constructor(
    readonly logger: RuntimeLogger,
    readonly pluginConfig: Record<string, unknown>,
    private readonly stateDir: string,
  ) {}

  /** runtime.state.resolveStateDir —— 兼容插件读 stateDir 的用法。 */
  readonly runtime = {
    state: {
      resolveStateDir: (): string => this.stateDir,
    },
  };

  registerHttpRoute(spec: HttpRouteSpec): void {
    this.httpRoutes.set(spec.path, spec);
  }

  registerGatewayMethod(method: string, handler: GatewayHandler): void {
    this.gatewayMethods.set(method, handler);
  }

  // ── CLI 形态下的 no-op（PRD「兼容性 shim」）──
  registerService(name: string): void {
    this.services.push(typeof name === "string" ? name : "service");
  }
  registerCli(): void {
    /* CLI 模式不复用插件 CLI 注册 */
  }
  registerTool(): void {
    /* CLI 不开 MCP，Agent 走 Skill + 直接调命令 */
  }
  on(): void {
    /* before_prompt_build 等宿主事件在 CLI 模式不参与 */
  }

  /** 调用一个 gateway 方法，把 respond 收敛成 Promise<GatewayResult>。 */
  callGateway(
    method: string,
    params: unknown,
    context?: IngestContext,
  ): Promise<GatewayResult> {
    const handler = this.gatewayMethods.get(method);
    if (!handler) {
      return Promise.resolve({
        ok: false,
        error: { code: "METHOD_NOT_FOUND", message: `未注册的 gateway 方法：${method}` },
      });
    }
    return new Promise<GatewayResult>((resolve) => {
      let settled = false;
      const respond: GatewayRespond = (ok, data, error) => {
        if (settled) return;
        settled = true;
        resolve({ ok, data, error });
      };
      Promise.resolve(runWithIngestContext(context, () => handler({ params, respond, context }))).catch((err) => {
        if (!settled) {
          settled = true;
          resolve({
            ok: false,
            error: { code: "INTERNAL_ERROR", message: (err as Error).message },
          });
        }
      });
    });
  }
}
