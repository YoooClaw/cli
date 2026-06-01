import type { ApiKeyEntry, CredentialSet } from "../credentials/store.js";
import { startRelayTunnel, type RelayTunnelHandle } from "./relay.js";
import type { DaemonLogger } from "./logger.js";
import type { StandaloneRuntime } from "./runtime.js";

export interface TunnelSupervisorOptions {
  tunnelUrl: string;
  runtime: StandaloneRuntime;
  httpBaseUrl: string;
  httpToken?: string;
  heartbeatSec?: number;
  reconnectBackoffMs?: number;
  stateDir: string;
  logger: DaemonLogger;
}

interface ManagedTunnel {
  entry: ApiKeyEntry;
  handle: RelayTunnelHandle;
}

export interface TunnelApplyResult {
  started: string[];
  stopped: string[];
  restarted: string[];
  unchanged: string[];
}

export class TunnelSupervisor {
  private readonly tunnels = new Map<string, ManagedTunnel>();
  private defaultLabel: string | null = null;
  private mode: CredentialSet["mode"] = "none";

  constructor(private readonly opts: TunnelSupervisorOptions) {}

  async apply(set: CredentialSet): Promise<TunnelApplyResult> {
    const started: string[] = [];
    const stopped: string[] = [];
    const restarted: string[] = [];
    const unchanged: string[] = [];
    const next = new Map(set.entries.map((entry) => [entry.label, entry]));

    for (const [label, managed] of this.tunnels) {
      const nextEntry = next.get(label);
      if (!nextEntry) {
        await managed.handle.stop("removed");
        this.tunnels.delete(label);
        stopped.push(label);
        continue;
      }
      if (nextEntry.key !== managed.entry.key) {
        await managed.handle.stop("key-changed");
        this.tunnels.set(label, { entry: nextEntry, handle: this.start(nextEntry) });
        restarted.push(label);
      } else {
        managed.entry = nextEntry;
        unchanged.push(label);
      }
    }

    for (const entry of set.entries) {
      if (this.tunnels.has(entry.label)) continue;
      this.tunnels.set(entry.label, { entry, handle: this.start(entry) });
      started.push(entry.label);
    }

    this.defaultLabel = set.defaultEntry?.label ?? null;
    this.mode = set.mode;
    return { started, stopped, restarted, unchanged };
  }

  async stopAll(reason: string): Promise<void> {
    const handles = [...this.tunnels.values()].map((managed) => managed.handle);
    this.tunnels.clear();
    await Promise.all(handles.map((handle) => handle.stop(reason).catch(() => undefined)));
  }

  async reconnect(label?: string): Promise<{ reconnected: string[]; missing?: string }> {
    const labels = label ? [label] : [...this.tunnels.keys()];
    const reconnected: string[] = [];
    for (const item of labels) {
      const managed = this.tunnels.get(item);
      if (!managed) return { reconnected, missing: item };
      await managed.handle.stop("manual-reconnect").catch(() => undefined);
      this.tunnels.set(item, { entry: managed.entry, handle: this.start(managed.entry) });
      reconnected.push(item);
    }
    return { reconnected };
  }

  pushEvent(event: string, payload: unknown): void {
    for (const managed of this.tunnels.values()) {
      managed.handle.pushEvent(event, payload);
    }
  }

  status(): {
    mode: CredentialSet["mode"];
    defaultLabel: string | null;
    tunnels: Array<{
      label: string;
      default: boolean;
      connected: boolean;
      url: string;
      reconnectAttempt: number;
      lastDisconnectReason?: string;
    }>;
  } {
    return {
      mode: this.mode,
      defaultLabel: this.defaultLabel,
      tunnels: [...this.tunnels.values()].map((managed) => {
        const status = managed.handle.status();
        return {
          label: managed.entry.label,
          default: managed.entry.label === this.defaultLabel,
          connected: status.connected,
          url: status.url,
          reconnectAttempt: status.reconnectAttempt,
          lastDisconnectReason: status.lastDisconnectReason,
        };
      }),
    };
  }

  defaultStatus() {
    const status = this.status();
    return status.tunnels.find((tunnel) => tunnel.label === status.defaultLabel) ?? status.tunnels[0];
  }

  private start(entry: ApiKeyEntry): RelayTunnelHandle {
    return startRelayTunnel({
      label: entry.label,
      tunnelUrl: this.opts.tunnelUrl,
      apiKey: entry.key,
      runtime: this.opts.runtime,
      httpBaseUrl: this.opts.httpBaseUrl,
      httpToken: this.opts.httpToken,
      heartbeatSec: this.opts.heartbeatSec,
      reconnectBackoffMs: this.opts.reconnectBackoffMs,
      stateDir: this.opts.stateDir,
      logger: this.opts.logger,
    });
  }
}
