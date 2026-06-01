/**
 * L3 + L4 端到端测试 —— 拉起真实 daemon 子进程（relay 关闭、绑回环随机端口）。
 *
 * L3：🟡 命令经 DaemonClient 打到 daemon（status / auth check / gateway / tunnel /
 *     monitor / lightrule / api / 鉴权失败）。命令本体用进程内 runCli 执行。
 * L4：模拟手机端，直接 fetch daemon HTTP（/health 公开、/notifications 经 api-key 鉴权）。
 */
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { cleanHome, makeHome, profileDir, runCli } from "./helpers/cli.js";
import { startDaemon, type DaemonHandle } from "./helpers/daemon.js";

const SEGMENT = { mode: "steady", duration_s: 5, brightness: 192, color: { r: 255, g: 0, b: 0 } };

describe("L3 daemon standalone（无 token）", () => {
  let home: string;
  let daemon: DaemonHandle;

  beforeAll(async () => {
    home = makeHome();
    daemon = await startDaemon({ home });
  });
  afterAll(async () => {
    await daemon.stop();
    cleanHome(home);
  });

  it("daemon status 打到 /daemon/status", async () => {
    const res = await runCli(["daemon", "status"], { home });
    expect(res.exitCode).toBe(0);
    expect(res.json).toMatchObject({ ok: true, server: "yoooclaw" });
    expect(res.json.relay.mode).toBe("standalone-http");
    expect(res.json.port).toBe(daemon.port);
  });

  it("auth check 端到端可达", async () => {
    const res = await runCli(["auth", "check"], { home });
    expect(res.json).toMatchObject({ ok: true, daemonReachable: true, status: 200 });
  });

  it("gateway test 注入探针通知", async () => {
    const res = await runCli(["gateway", "test"], { home });
    expect(res.json).toMatchObject({ ok: true, status: 200 });
  });

  it("tunnel status / reconnect（relay 关闭走 standalone）", async () => {
    const status = await runCli(["tunnel", "status"], { home });
    expect(status.json).toMatchObject({ ok: true, mode: "standalone-http", connected: false });

    const reconnect = await runCli(["tunnel", "reconnect"], { home });
    expect(reconnect.json).toMatchObject({ ok: true, reconnected: false });
  });

  it("tunnel +test 回环 echo 自检", async () => {
    const res = await runCli(["tunnel", "+test"], { home });
    expect(res.json).toMatchObject({ ok: true, clientLabel: "local" });
    expect(res.json.loopback.ok).toBe(true);
  });

  it("api GET /health（公开端点）", async () => {
    const res = await runCli(["api", "GET", "/health"], { home });
    expect(res.json).toMatchObject({ ok: true, status: 200 });
    expect(res.json.body.server).toBe("yoooclaw");
  });

  it("api POST /notifications 注入数据", async () => {
    const payload = JSON.stringify({
      notifications: [{ id: "api_1", app: "test.api", title: "t", body: "b", timestamp: new Date().toISOString() }],
    });
    const res = await runCli(["api", "POST", "/notifications", "--data", payload], { home });
    expect(res.json.ok).toBe(true);
    expect(res.json.status).toBe(200);
  });

  it("light send 缺三选一参数报 INVALID_ARGUMENT", async () => {
    const res = await runCli(["light", "send"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });

  describe("monitor CRUD", () => {
    it("create → list → show → disable → delete", async () => {
      const create = await runCli(
        ["monitor", "create", "m1", "--description", "晚间提醒", "--match-rules", '{"app":"wechat"}', "--schedule", "0 21 * * *"],
        { home },
      );
      expect(create.json.ok).toBe(true);
      expect(create.json.monitor.name).toBe("m1");

      const list = await runCli(["monitor", "list"], { home });
      expect(list.json.monitors.some((m: any) => m.name === "m1")).toBe(true);

      const show = await runCli(["monitor", "show", "m1"], { home });
      expect(show.json.monitor.schedule).toBe("0 21 * * *");

      const disable = await runCli(["monitor", "disable", "m1"], { home });
      expect(disable.json).toMatchObject({ name: "m1", enabled: false });

      const del = await runCli(["monitor", "delete", "m1", "--yes"], { home });
      expect(del.json.deleted).toBe(true);
    });

    it("create 缺必填项报 INVALID_ARGUMENT", async () => {
      const res = await runCli(["monitor", "create", "bad", "--description", "x"], { home });
      expect(res.exitCode).toBe(1);
      expect(res.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
    });

    it("show 不存在报 NOT_FOUND", async () => {
      const res = await runCli(["monitor", "show", "ghost"], { home });
      expect(res.exitCode).toBe(1);
      expect(res.json.error.code).toBe("YOOOCLAW_NOT_FOUND");
    });
  });

  describe("lightrule CRUD", () => {
    it("list 初始为空", async () => {
      const res = await runCli(["lightrule", "list"], { home });
      expect(res.json).toMatchObject({ ok: true });
      expect(Array.isArray(res.json.rules)).toBe(true);
    });

    it("create → show → disable → enable → delete", async () => {
      const create = await runCli(
        ["lightrule", "create", "--name", "r1", "--intent", "重要消息闪红灯", "--light-action", JSON.stringify([SEGMENT])],
        { home },
      );
      expect(create.json.name).toBe("r1");

      const show = await runCli(["lightrule", "show", "r1"], { home });
      expect(show.json).toMatchObject({ ok: true });
      expect(show.json.rule.name).toBe("r1");

      const disable = await runCli(["lightrule", "disable", "r1"], { home });
      expect(disable.json).toBeTruthy();

      const enable = await runCli(["lightrule", "enable", "r1"], { home });
      expect(enable.json).toBeTruthy();

      const del = await runCli(["lightrule", "delete", "r1", "--yes"], { home });
      expect(del.json).toBeTruthy();
    });

    it("show 不存在报 NOT_FOUND", async () => {
      const res = await runCli(["lightrule", "show", "ghost"], { home });
      expect(res.exitCode).toBe(1);
      expect(res.json.error.code).toBe("YOOOCLAW_NOT_FOUND");
    });
  });
});

describe("L3 鉴权失败", () => {
  let home: string;
  let daemon: DaemonHandle;

  beforeAll(async () => {
    home = makeHome();
    daemon = await startDaemon({ home, token: "T0_correct_token_value" });
  });
  afterAll(async () => {
    await daemon.stop();
    cleanHome(home);
  });

  it("正确 token：status 200", async () => {
    const res = await runCli(["daemon", "status"], { home });
    expect(res.json.ok).toBe(true);
  });

  it("token 不一致：UNAUTHORIZED", async () => {
    // daemon 已把 T0 读进内存；改写本地凭据让 client 发出不一致的 token。
    writeFileSync(
      join(profileDir(home), "credentials.json"),
      JSON.stringify({ gatewayToken: "T1_wrong_token_value" }),
      "utf-8",
    );
    const res = await runCli(["daemon", "status"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_UNAUTHORIZED");
  });
});

describe("L4 手机端 ingest（api-key 鉴权）", () => {
  let home: string;
  let daemon: DaemonHandle;
  const KEY = "ock_phone_a_key_1234";

  beforeAll(async () => {
    home = makeHome();
    daemon = await startDaemon({
      home,
      apiKeys: [{ label: "phone-a", key: KEY, default: true }],
    });
  });
  afterAll(async () => {
    await daemon.stop();
    cleanHome(home);
  });

  it("/health 公开可达", async () => {
    const res = await fetch(`${daemon.baseUrl}/health`);
    expect(res.ok).toBe(true);
    const body = await res.json();
    expect(body.server).toBe("yoooclaw");
  });

  it("用 api-key 推送通知 → 落盘并打上 clientLabel", async () => {
    const res = await fetch(`${daemon.baseUrl}/notifications`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${KEY}` },
      body: JSON.stringify({
        notifications: [
          { id: "phone_1", app: "com.tencent.xin", title: "Alice", body: "hi from phone", timestamp: new Date().toISOString() },
        ],
      }),
    });
    expect(res.ok).toBe(true);

    // 经 CLI 查询验证落盘 + clientLabel 归属
    const search = await runCli(["notification", "search", "--client", "phone-a"], { home });
    expect(search.json.length).toBeGreaterThanOrEqual(1);
    expect(search.json.some((n: any) => n.clientLabel === "phone-a")).toBe(true);
  });
});

describe("L3 daemon 未运行", () => {
  let home: string;
  beforeEach(() => {
    home = makeHome();
  });
  afterEach(() => {
    cleanHome(home);
  });

  it("🟡 命令报 DAEMON_NOT_RUNNING", async () => {
    const res = await runCli(["tunnel", "status"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_DAEMON_NOT_RUNNING");
  });
});
