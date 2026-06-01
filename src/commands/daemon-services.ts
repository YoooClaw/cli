/**
 * 依赖 daemon 的 service（🟡）—— 统一经 DaemonClient 走 localhost HTTP RPC。
 * light / lightrule / tunnel / monitor / gateway / api。
 */
import { readFileSync } from "node:fs";
import type { CliContext } from "../context.js";
import { YoooclawError } from "../errors.js";
import { DaemonClient, assertDaemonRunning } from "../daemon/client.js";
import { confirm, readStdin } from "../prompt.js";

function client(ctx: CliContext): DaemonClient {
  assertDaemonRunning(ctx.paths);
  return new DaemonClient(ctx.paths);
}

function parseJsonArg(raw: string | undefined, label: string): unknown {
  if (raw === undefined) return undefined;
  try {
    return JSON.parse(raw);
  } catch {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", `${label} 不是合法 JSON`);
  }
}

// ── light ──
export async function lightSend(
  ctx: CliContext,
  _args: unknown[],
  opts: {
    segments?: string;
    preset?: string;
    rule?: string;
    repeat?: boolean;
    repeatTimes?: string;
  },
): Promise<unknown> {
  if (!opts.segments && !opts.preset && !opts.rule) {
    throw new YoooclawError(
      "YOOOCLAW_INVALID_ARGUMENT",
      "需要 --segments / --preset / --rule 之一",
    );
  }
  const body: Record<string, unknown> = {};
  if (opts.segments) body.segments = parseJsonArg(opts.segments, "--segments");
  if (opts.preset) body.preset = opts.preset;
  if (opts.rule) body.rule = opts.rule;
  if (opts.repeat) body.repeat = true;
  if (opts.repeatTimes !== undefined) body.repeat_times = Number(opts.repeatTimes);
  const res = await client(ctx).post("/light/send", body);
  return res.body;
}

/** +blink shortcut：映射到 presets-v1 的 red-strobe-3，做连通性测试。 */
export async function lightBlink(ctx: CliContext): Promise<unknown> {
  const res = await client(ctx).post("/light/send", { preset: "red-strobe-3" });
  return res.body;
}

// ── lightrule ──
async function listRules(c: DaemonClient): Promise<Array<Record<string, unknown>>> {
  const res = await c.post<{ data?: { rules?: Array<Record<string, unknown>> } }>("/gateway/lightrules.list", {});
  return res.body?.data?.rules ?? [];
}

export async function lightruleList(ctx: CliContext): Promise<unknown> {
  return { ok: true, rules: await listRules(client(ctx)) };
}

export async function lightruleShow(ctx: CliContext, args: unknown[]): Promise<unknown> {
  const [id] = args as [string];
  const rule = (await listRules(client(ctx))).find((r) => r.id === id || r.name === id);
  if (!rule) throw new YoooclawError("YOOOCLAW_NOT_FOUND", `规则不存在：${id}`);
  return { ok: true, rule };
}

interface LightRuleOpts {
  fromFile?: string;
  name?: string;
  intent?: string;
  lightAction?: string;
  matchRules?: string;
}

async function buildRuleParams(opts: LightRuleOpts, name?: string): Promise<Record<string, unknown>> {
  if (opts.fromFile) {
    const raw = opts.fromFile === "-" ? await readStdin() : readFileSync(opts.fromFile, "utf-8");
    return JSON.parse(raw) as Record<string, unknown>;
  }
  const params: Record<string, unknown> = {};
  if (name) params.name = name;
  if (opts.name) params.name = opts.name;
  if (opts.intent) params.description = opts.intent;
  const action = parseJsonArg(opts.lightAction, "--light-action");
  if (action !== undefined) {
    params.segments = Array.isArray(action) ? action : (action as { segments?: unknown }).segments ?? action;
  }
  const match = parseJsonArg(opts.matchRules, "--match-rules");
  if (match !== undefined) params.matchRules = match;
  return params;
}

export async function lightruleCreate(
  ctx: CliContext,
  _args: unknown[],
  opts: LightRuleOpts,
): Promise<unknown> {
  const c = client(ctx);
  const params = await buildRuleParams(opts);
  const res = await c.post("/gateway/lightrules.create", params);
  if (!res.body || (res.body as { ok?: boolean }).ok === false) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", JSON.stringify((res.body as { error?: unknown })?.error ?? res.body));
  }
  return (res.body as { data?: unknown }).data ?? res.body;
}

export async function lightruleUpdate(
  ctx: CliContext,
  args: unknown[],
  opts: LightRuleOpts,
): Promise<unknown> {
  const [id] = args as [string];
  const c = client(ctx);
  const params = await buildRuleParams(opts, id);
  params.name = id;
  const res = await c.post("/gateway/lightrules.update", params);
  return (res.body as { data?: unknown }).data ?? res.body;
}

export async function lightruleDelete(
  ctx: CliContext,
  args: unknown[],
  opts: { yes?: boolean },
): Promise<unknown> {
  const [id] = args as [string];
  if (!opts.yes && !(await confirm(`确认删除规则 \`${id}\`？`, false))) {
    throw new YoooclawError("YOOOCLAW_CONFIRMATION_REQUIRED", "已取消");
  }
  const res = await client(ctx).post("/gateway/lightrules.delete", { name: id });
  return (res.body as { data?: unknown }).data ?? res.body;
}

export async function lightruleEnable(ctx: CliContext, args: unknown[]): Promise<unknown> {
  const [id] = args as [string];
  const res = await client(ctx).post("/gateway/lightrules.update", { name: id, enabled: true });
  return (res.body as { data?: unknown }).data ?? res.body;
}

export async function lightruleDisable(ctx: CliContext, args: unknown[]): Promise<unknown> {
  const [id] = args as [string];
  const res = await client(ctx).post("/gateway/lightrules.update", { name: id, enabled: false });
  return (res.body as { data?: unknown }).data ?? res.body;
}

async function toggleAll(ctx: CliContext, enabled: boolean): Promise<unknown> {
  const c = client(ctx);
  const rules = await listRules(c);
  const results: Array<{ name: unknown; ok: boolean }> = [];
  for (const rule of rules) {
    const res = await c.post("/gateway/lightrules.update", { name: rule.name, enabled });
    results.push({ name: rule.name, ok: (res.body as { ok?: boolean })?.ok !== false });
  }
  return { ok: true, enabled, count: results.length, results };
}

export const lightruleOn = (ctx: CliContext): Promise<unknown> => toggleAll(ctx, true);
export const lightruleOff = (ctx: CliContext): Promise<unknown> => toggleAll(ctx, false);

// ── tunnel ──
export async function tunnelStatus(
  ctx: CliContext,
  _args: unknown[],
  opts: { client?: string },
): Promise<unknown> {
  const query = opts.client ? `?client=${encodeURIComponent(opts.client)}` : "";
  return (await client(ctx).get(`/tunnel/status${query}`)).body;
}
export async function tunnelReconnect(
  ctx: CliContext,
  _args: unknown[],
  opts: { client?: string },
): Promise<unknown> {
  return (await client(ctx).post("/tunnel/reconnect", opts.client ? { client: opts.client } : undefined)).body;
}
export async function tunnelTest(
  ctx: CliContext,
  _args: unknown[],
  opts: { client?: string },
): Promise<unknown> {
  return (await client(ctx).post("/tunnel/test", opts.client ? { client: opts.client } : undefined)).body;
}

// ── monitor ──
export async function monitorList(ctx: CliContext): Promise<unknown> {
  return (await client(ctx).get("/monitors")).body;
}
export async function monitorShow(ctx: CliContext, args: unknown[]): Promise<unknown> {
  const [name] = args as [string];
  const res = await client(ctx).get<{ monitors?: Array<{ name: string }> }>("/monitors");
  const monitor = res.body?.monitors?.find((m) => m.name === name);
  if (!monitor) throw new YoooclawError("YOOOCLAW_NOT_FOUND", `监控任务不存在：${name}`);
  return { ok: true, monitor };
}
export async function monitorCreate(
  ctx: CliContext,
  args: unknown[],
  opts: { description?: string; matchRules?: string; schedule?: string },
): Promise<unknown> {
  const [name] = args as [string];
  if (!opts.description || !opts.matchRules || !opts.schedule) {
    throw new YoooclawError("YOOOCLAW_INVALID_ARGUMENT", "--description / --match-rules / --schedule 均必填");
  }
  const res = await client(ctx).post("/monitors", {
    name,
    description: opts.description,
    matchRules: parseJsonArg(opts.matchRules, "--match-rules"),
    schedule: opts.schedule,
  });
  return res.body;
}
export async function monitorDelete(
  ctx: CliContext,
  args: unknown[],
  opts: { yes?: boolean },
): Promise<unknown> {
  const [name] = args as [string];
  if (!opts.yes && !(await confirm(`确认删除监控任务 \`${name}\`？`, false))) {
    throw new YoooclawError("YOOOCLAW_CONFIRMATION_REQUIRED", "已取消");
  }
  return (await client(ctx).request("DELETE", `/monitors/${encodeURIComponent(name)}`)).body;
}
export async function monitorEnable(ctx: CliContext, args: unknown[]): Promise<unknown> {
  const [name] = args as [string];
  return (await client(ctx).post(`/monitors/${encodeURIComponent(name)}/enable`)).body;
}
export async function monitorDisable(ctx: CliContext, args: unknown[]): Promise<unknown> {
  const [name] = args as [string];
  return (await client(ctx).post(`/monitors/${encodeURIComponent(name)}/disable`)).body;
}

// ── gateway test ──
export async function gatewayTest(
  ctx: CliContext,
  _args: unknown[],
  opts: { fromPhoneIp?: string; viaRelay?: boolean },
): Promise<unknown> {
  const c = client(ctx);
  if (opts.viaRelay) {
    return { ok: true, viaRelay: true, result: (await c.post("/tunnel/test")).body };
  }
  const probe = {
    notifications: [
      { id: `gwtest_${Date.now()}`, app: "yoooclaw.gatewaytest", title: "gateway test", body: "probe", timestamp: new Date().toISOString() },
    ],
  };
  const res = await c.post("/notifications", probe);
  return {
    ok: res.status >= 200 && res.status < 300,
    fromPhoneIp: opts.fromPhoneIp ?? null,
    status: res.status,
    response: res.body,
  };
}

// ── raw api ──
export async function apiRaw(
  ctx: CliContext,
  args: unknown[],
  opts: { data?: string; header?: string[] | string },
): Promise<unknown> {
  const [method, path] = args as [string, string];
  let data: unknown;
  if (opts.data) {
    let raw: string;
    if (opts.data === "-") raw = await readStdin();
    else if (opts.data.startsWith("@")) raw = readFileSync(opts.data.slice(1), "utf-8");
    else raw = opts.data;
    try {
      data = JSON.parse(raw);
    } catch {
      data = raw;
    }
  }
  const headers: Record<string, string> = {};
  const headerList = Array.isArray(opts.header) ? opts.header : opts.header ? [opts.header] : [];
  for (const h of headerList) {
    const idx = h.indexOf(":");
    if (idx > 0) headers[h.slice(0, idx).trim()] = h.slice(idx + 1).trim();
  }
  const res = await client(ctx).request(method.toUpperCase(), path, data, headers);
  return { ok: res.status >= 200 && res.status < 300, status: res.status, body: res.body };
}
