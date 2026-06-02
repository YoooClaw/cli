/**
 * L5 进程生命周期测试 —— 必须用真子进程（detach / fork / 信号 / lock 文件）。
 * 经 `bun src/bin.ts daemon …` 黑盒驱动 start / stop / restart / 单例锁 / 陈旧锁恢复。
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { cleanHome, makeHome, profileDir, runCliSubprocess } from "./helpers/cli.js";
import { freePort } from "./helpers/daemon.js";

let home: string;

/** 写一份 relay 关闭的最小 config，端口取空闲端口。 */
async function writeConfig(): Promise<void> {
  const dir = profileDir(home);
  mkdirSync(dir, { recursive: true });
  const port = await freePort();
  const credentialsPath = join(dir, "credentials.json");
  writeFileSync(
    join(dir, "config.json"),
    JSON.stringify({
      version: 1,
      daemon: { bind: "127.0.0.1", port, logLevel: "info", detach: true },
      auth: { mode: "token", tokenRef: `file:${credentialsPath}#gatewayToken` },
      relay: { url: "wss://relay.invalid/ws", heartbeatSec: 10, reconnectBackoffMs: 2000, enabled: false },
      notification: { retentionDays: null, ignoredApps: [] },
      lightRules: { enabled: true },
      autoUpdate: { enabled: true, channel: "stable" },
      output: { defaultFormat: "auto" },
      image: { maxBytes: 1000000 },
    }),
    "utf-8",
  );
}

beforeEach(async () => {
  home = makeHome();
  await writeConfig();
});
afterEach(() => {
  // 兜底：确保 detach 出去的 daemon 被收掉
  runCliSubprocess(["daemon", "stop"], { home });
  cleanHome(home);
});

describe("daemon 生命周期", () => {
  it("start（detach）→ status 在跑 → stop", () => {
    const start = runCliSubprocess(["daemon", "start"], { home });
    expect(start.exitCode).toBe(0);
    expect(start.json).toMatchObject({ ok: true, detached: true });
    expect(typeof start.json.pid).toBe("number");
    expect(existsSync(join(profileDir(home), "daemon.lock"))).toBe(true);

    const status = runCliSubprocess(["daemon", "status"], { home });
    expect(status.json).toMatchObject({ ok: true, server: "yoooclaw" });
    expect(status.json.pid).toBe(start.json.pid);

    const stop = runCliSubprocess(["daemon", "stop"], { home });
    expect(stop.json.ok).toBe(true);
    expect(stop.json.stopped).toBe(start.json.pid);
    expect(existsSync(join(profileDir(home), "daemon.lock"))).toBe(false);
  });

  it("重复 start 报 ALREADY_RUNNING（单例锁）", () => {
    const first = runCliSubprocess(["daemon", "start"], { home });
    expect(first.json.ok).toBe(true);

    const second = runCliSubprocess(["daemon", "start"], { home });
    expect(second.exitCode).toBe(1);
    expect(second.json.error.code).toBe("YOOOCLAW_DAEMON_ALREADY_RUNNING");
  });

  it("未运行时 stop / status 报 DAEMON_NOT_RUNNING", () => {
    const stop = runCliSubprocess(["daemon", "stop"], { home });
    expect(stop.exitCode).toBe(1);
    expect(stop.json.error.code).toBe("YOOOCLAW_DAEMON_NOT_RUNNING");

    const status = runCliSubprocess(["daemon", "status"], { home });
    expect(status.exitCode).toBe(1);
    expect(status.json.error.code).toBe("YOOOCLAW_DAEMON_NOT_RUNNING");
  });

  it("restart 拉起新进程", () => {
    const start = runCliSubprocess(["daemon", "start"], { home });
    const oldPid = start.json.pid;
    expect(typeof oldPid).toBe("number");

    const restart = runCliSubprocess(["daemon", "restart"], { home });
    expect(restart.json.ok).toBe(true);
    expect(restart.json.pid).not.toBe(oldPid);

    const status = runCliSubprocess(["daemon", "status"], { home });
    expect(status.json.pid).toBe(restart.json.pid);
  });

  it("陈旧锁（进程已死）不阻塞 start", () => {
    // 写一个指向已死 pid 的 lock
    writeFileSync(
      join(profileDir(home), "daemon.lock"),
      JSON.stringify({ pid: 999999, startedAt: new Date().toISOString(), bind: "127.0.0.1", port: 65000 }),
      "utf-8",
    );
    const start = runCliSubprocess(["daemon", "start"], { home });
    expect(start.json.ok).toBe(true);
    expect(start.json.pid).not.toBe(999999);
  });

  it("config init 默认自动拉起 daemon", async () => {
    // 用一个未初始化的新 profile，避免命中 beforeEach 写好的 default config。
    const port = await freePort();
    const importPath = join(home, "init-import.json");
    writeFileSync(
      importPath,
      JSON.stringify({
        daemon: { bind: "127.0.0.1", port },
        relay: { enabled: false },
      }),
      "utf-8",
    );

    const init = runCliSubprocess(
      ["config", "init", "--profile", "fresh", "--from-file", importPath],
      { home },
    );
    try {
      expect(init.exitCode).toBe(0);
      expect(init.json.daemon).toMatchObject({ started: true });
      expect(typeof init.json.daemon.pid).toBe("number");
      expect(existsSync(join(profileDir(home, "fresh"), "daemon.lock"))).toBe(true);

      const status = runCliSubprocess(["daemon", "status", "--profile", "fresh"], { home });
      expect(status.json).toMatchObject({ ok: true, server: "yoooclaw" });
      expect(status.json.pid).toBe(init.json.daemon.pid);
    } finally {
      runCliSubprocess(["daemon", "stop", "--profile", "fresh"], { home });
    }
  });

  it("config init --no-start 只生成配置不启动 daemon", () => {
    const importPath = join(home, "init-import-nostart.json");
    writeFileSync(importPath, JSON.stringify({ relay: { enabled: false } }), "utf-8");

    const init = runCliSubprocess(
      ["config", "init", "--profile", "fresh", "--from-file", importPath, "--no-start"],
      { home },
    );
    expect(init.exitCode).toBe(0);
    expect(init.json.daemon.started).toBe(false);
    expect(existsSync(join(profileDir(home, "fresh"), "daemon.lock"))).toBe(false);
  });
});
