/**
 * daemon 测试夹具 —— 拉起一个真实 daemon 子进程（绑回环随机端口、relay 关闭），
 * 供 L3（🟡 命令端到端）与 L4（模拟手机端 ingest）使用。
 *
 * 用 `bun src/bin.ts daemon run-foreground` 直接拿到子进程句柄，teardown 时 SIGTERM。
 * relay 默认关闭，避免连生产 Relay；可选写入 gateway token / 多 api-key。
 */
import { type ChildProcess, spawn } from "node:child_process";
import { createServer } from "node:net";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { CLI_BIN, profileDir } from "./cli.js";

/** 找一个当前空闲的端口（listen 0 拿到后立即释放）。 */
export function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = createServer();
    srv.on("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr ? addr.port : 0;
      srv.close(() => resolve(port));
    });
  });
}

export interface StoredApiKey {
  label: string;
  key: string;
  default?: boolean;
}

export interface StartDaemonOpts {
  home: string;
  profile?: string;
  port?: number;
  /** gateway token；提供后写入 per-profile credentials.json，daemon 与 client 都用它。 */
  token?: string;
  /** 多 api-key（写 account 级共享 credentials.json 的 apiKeys[]）。 */
  apiKeys?: StoredApiKey[];
  /** 是否启用 relay（默认 false，避免连生产）。 */
  relay?: boolean;
}

export interface DaemonHandle {
  port: number;
  home: string;
  profile: string;
  baseUrl: string;
  token?: string;
  child: ChildProcess;
  stop: () => Promise<void>;
}

/** 写出一份最小可用的 config.json。 */
function writeConfig(opts: Required<Pick<StartDaemonOpts, "home" | "profile" | "port" | "relay">>): void {
  const dir = profileDir(opts.home, opts.profile);
  mkdirSync(dir, { recursive: true });
  const credentialsPath = join(dir, "credentials.json");
  const config = {
    version: 1,
    daemon: { bind: "127.0.0.1", port: opts.port, logLevel: "info", detach: true },
    auth: { mode: "token", tokenRef: `file:${credentialsPath}#gatewayToken` },
    relay: {
      url: "wss://relay.invalid/ws",
      heartbeatSec: 10,
      reconnectBackoffMs: 2000,
      enabled: opts.relay,
    },
    notification: { retentionDays: null, ignoredApps: [] },
    lightRules: { enabled: true },
    autoUpdate: { enabled: true, channel: "stable" },
    output: { defaultFormat: "auto" },
    image: { maxBytes: 20 * 1024 * 1024 },
  };
  writeFileSync(join(dir, "config.json"), JSON.stringify(config, null, 2), "utf-8");
}

async function waitForHealth(baseUrl: string, timeoutMs = 8000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${baseUrl}/health`);
      if (res.ok) return;
    } catch {
      /* not up yet */
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`daemon 未在 ${timeoutMs}ms 内就绪：${baseUrl}`);
}

/** 拉起 daemon 子进程，返回句柄；调用方在 afterEach 里 await handle.stop()。 */
export async function startDaemon(opts: StartDaemonOpts): Promise<DaemonHandle> {
  const profile = opts.profile ?? "default";
  const port = opts.port ?? (await freePort());
  const relay = opts.relay ?? false;
  const dir = profileDir(opts.home, profile);
  mkdirSync(dir, { recursive: true });

  writeConfig({ home: opts.home, profile, port, relay });
  if (opts.token) {
    writeFileSync(
      join(dir, "credentials.json"),
      JSON.stringify({ gatewayToken: opts.token }, null, 2),
      "utf-8",
    );
  }
  if (opts.apiKeys) {
    writeFileSync(
      join(opts.home, "credentials.json"),
      JSON.stringify({ apiKeys: opts.apiKeys }, null, 2),
      "utf-8",
    );
  }

  const env: Record<string, string> = { ...process.env } as Record<string, string>;
  env.YOOOCLAW_HOME = opts.home;
  delete env.YOOOCLAW_API_KEY;
  delete env.YOOOCLAW_PROFILE;

  const child = spawn(
    "bun",
    [CLI_BIN, "daemon", "run-foreground", "--profile", profile, "--port", String(port)],
    { env, stdio: "ignore" },
  );

  const baseUrl = `http://127.0.0.1:${port}`;
  const stop = async (): Promise<void> => {
    if (child.exitCode !== null || child.signalCode !== null) return;
    await new Promise<void>((resolve) => {
      const done = (): void => resolve();
      child.once("exit", done);
      child.kill("SIGTERM");
      setTimeout(() => {
        if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
        resolve();
      }, 5000);
    });
  };

  try {
    await waitForHealth(baseUrl);
  } catch (err) {
    await stop();
    throw err;
  }

  return { port, home: opts.home, profile, baseUrl, token: opts.token, child, stop };
}
