import type { StandaloneRuntime } from "./runtime.js";

export interface RelayStatusSnapshot {
  mode: "relay" | "standalone-http";
  connected: boolean;
  url: string;
  enabled: boolean;
  reconnectAttempt: number;
  lastDisconnectReason?: string;
  note?: string;
}

export interface DaemonGatewayCompatOptions {
  version: string;
  protocol: number;
  capabilities: string[];
  profile: string;
  bind: string;
  getPort: () => number;
  startedAt: string;
  getLastIngestAt: () => string | null;
  getIngestCount: () => number;
  getLightRuleCount: () => number;
  getRelayStatus: () => RelayStatusSnapshot;
}

function gatewayMethods(runtime: StandaloneRuntime): string[] {
  return [...runtime.gatewayMethods.keys()].sort();
}

function buildHealthPayload(
  runtime: StandaloneRuntime,
  opts: DaemonGatewayCompatOptions,
): Record<string, unknown> {
  const relay = opts.getRelayStatus();
  return {
    status: "ok",
    healthy: true,
    server: "yoooclaw",
    version: opts.version,
    protocol: opts.protocol,
    capabilities: opts.capabilities,
    profile: opts.profile,
    bind: opts.bind,
    port: opts.getPort(),
    startedAt: opts.startedAt,
    lastIngestAt: opts.getLastIngestAt(),
    ingestCount: opts.getIngestCount(),
    lightRules: opts.getLightRuleCount(),
    relay,
    features: {
      methods: gatewayMethods(runtime),
      capabilities: opts.capabilities,
    },
  };
}

function buildChannelsPayload(opts: DaemonGatewayCompatOptions): Record<string, unknown> {
  const relay = opts.getRelayStatus();
  const account = {
    id: `yoooclaw.daemon/${opts.profile}`,
    channel: "yoooclaw.daemon",
    accountId: opts.profile,
    label: `YoooClaw daemon (${opts.profile})`,
    enabled: true,
    running: true,
    connected: relay.connected,
    mode: relay.mode,
    url: relay.url,
    reconnectAttempt: relay.reconnectAttempt,
    lastError: relay.lastDisconnectReason ?? relay.note,
  };

  return {
    channelOrder: ["yoooclaw.daemon"],
    channelMeta: [
      {
        id: "yoooclaw.daemon",
        label: "YoooClaw daemon",
        type: "daemon",
      },
    ],
    channels: [
      {
        id: "yoooclaw.daemon",
        label: "YoooClaw daemon",
        enabled: true,
        running: true,
        connected: relay.connected,
        accounts: [account],
      },
    ],
    accounts: [account],
    relay,
  };
}

function paramsRecord(params: unknown): Record<string, unknown> {
  return params && typeof params === "object"
    ? (params as Record<string, unknown>)
    : {};
}

export function registerDaemonGatewayCompatMethods(
  runtime: StandaloneRuntime,
  opts: DaemonGatewayCompatOptions,
): void {
  runtime.registerGatewayMethod("health", async ({ respond }) => {
    respond(true, buildHealthPayload(runtime, opts));
  });

  runtime.registerGatewayMethod("channels.status", async ({ respond }) => {
    respond(true, buildChannelsPayload(opts));
  });

  runtime.registerGatewayMethod("agents.list", async ({ respond }) => {
    respond(true, { total: 0, agents: [], items: [] });
  });

  runtime.registerGatewayMethod("sessions.list", async ({ respond }) => {
    respond(true, { total: 0, sessions: [], items: [] });
  });

  runtime.registerGatewayMethod("chat.history", async ({ params, respond }) => {
    const sessionKey = paramsRecord(params).sessionKey;
    respond(true, {
      sessionKey: typeof sessionKey === "string" ? sessionKey : undefined,
      total: 0,
      messages: [],
    });
  });

  runtime.registerGatewayMethod("usage.cost", async ({ respond }) => {
    respond(true, {
      totalCost: 0,
      cost: 0,
      currency: "USD",
      items: [],
      usage: [],
    });
  });

  runtime.registerGatewayMethod("cron.list", async ({ respond }) => {
    respond(true, { total: 0, crons: [], tasks: [], items: [] });
  });

  runtime.registerGatewayMethod("wake", async ({ respond }) => {
    respond(true, { ok: true, woken: true });
  });
}
