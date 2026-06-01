import { describe, expect, it, vi } from "vitest";
import { RelayDispatcher } from "../src/daemon/relay-dispatcher.js";
import { StandaloneRuntime } from "../src/daemon/runtime.js";
import { registerDaemonGatewayCompatMethods } from "../src/daemon/gateway-compat.js";

const logger = {
  debug: vi.fn(),
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
};

class FakeRelayClient {
  private inbound?: (frame: unknown) => void | Promise<void>;
  readonly sent: string[] = [];

  onInbound(handler: (frame: unknown) => void | Promise<void>): void {
    this.inbound = handler;
  }

  sendRaw(text: string): void {
    this.sent.push(text);
  }

  emit(frame: unknown): void {
    this.inbound?.(frame);
  }
}

function tick(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

describe("RelayDispatcher", () => {
  it("returns Gateway RPC results in the payload field", async () => {
    const client = new FakeRelayClient();
    const runtime = new StandaloneRuntime(logger, {}, "/tmp/yoooclaw-test");
    runtime.registerGatewayMethod("health", async ({ respond }) => {
      respond(true, { server: "yoooclaw" });
    });

    const dispatcher = new RelayDispatcher({
      client: client as never,
      runtime,
      logger,
      httpBaseUrl: "http://127.0.0.1:18789",
    });
    dispatcher.start();

    client.emit({ type: "req", id: "relay-1", method: "health" });
    await tick();

    expect(JSON.parse(client.sent[0])).toEqual({
      type: "res",
      id: "relay-1",
      ok: true,
      payload: { server: "yoooclaw" },
    });
  });

  it("pushes daemon events with the Gateway event field", () => {
    const client = new FakeRelayClient();
    const runtime = new StandaloneRuntime(logger, {}, "/tmp/yoooclaw-test");
    const dispatcher = new RelayDispatcher({
      client: client as never,
      runtime,
      logger,
      httpBaseUrl: "http://127.0.0.1:18789",
    });

    dispatcher.pushEvent("recording.status", { recordingId: "rec-1" });

    expect(JSON.parse(client.sent[0])).toEqual({
      type: "event",
      event: "recording.status",
      payload: { recordingId: "rec-1" },
    });
  });
});

describe("daemon Gateway compatibility methods", () => {
  it("registers health for mobile Gateway probes", async () => {
    const runtime = new StandaloneRuntime(logger, {}, "/tmp/yoooclaw-test");
    registerDaemonGatewayCompatMethods(runtime, {
      version: "0.0.0-test",
      protocol: 1,
      capabilities: ["notifications", "recordings"],
      profile: "default",
      bind: "127.0.0.1",
      getPort: () => 18789,
      startedAt: "2026-05-25T00:00:00.000Z",
      getLastIngestAt: () => null,
      getIngestCount: () => 0,
      getLightRuleCount: () => 0,
      getRelayStatus: () => ({
        mode: "relay",
        connected: true,
        url: "wss://relay.example/ws",
        enabled: true,
        reconnectAttempt: 0,
      }),
    });

    const result = await runtime.callGateway("health", {});

    expect(result.ok).toBe(true);
    expect(result.data).toMatchObject({
      status: "ok",
      server: "yoooclaw",
      version: "0.0.0-test",
      protocol: 1,
      capabilities: ["notifications", "recordings"],
    });
    expect((result.data as { features: { methods: string[] } }).features.methods).toContain("health");
  });
});
