/**
 * L2 行为测试 —— config / profile / 全局 flags / doctor / migrate（🟢，无 daemon）。
 * 黑盒跑 run(argv)，断言 stdout JSON + exitCode + 落盘。
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { cleanHome, makeHome, profileDir, runCli } from "./helpers/cli.js";

let home: string;
beforeEach(() => {
  home = makeHome();
});
afterEach(() => {
  cleanHome(home);
});

/** 往 home 写一个可被 --from-file 读取的配置文件，返回路径。 */
function importFile(partial: unknown = {}): string {
  const p = join(home, "import.json");
  writeFileSync(p, JSON.stringify(partial), "utf-8");
  return p;
}

describe("config", () => {
  it("init 生成 config + gateway token", async () => {
    const res = await runCli(["config", "init", "--from-file", importFile()], { home });
    expect(res.exitCode).toBe(0);
    expect(res.json.ok).toBe(true);
    expect(res.json.profile).toBe("default");
    expect(typeof res.json.gatewayToken).toBe("string");
    expect(res.json.gatewayToken.length).toBeGreaterThan(0);
    expect(existsSync(join(profileDir(home), "config.json"))).toBe(true);
    expect(existsSync(join(profileDir(home), "credentials.json"))).toBe(true);
  });

  it("已初始化再 init 报 ALREADY_EXISTS，--force 可覆盖", async () => {
    await runCli(["config", "init", "--from-file", importFile()], { home });
    const blocked = await runCli(["config", "init", "--from-file", importFile()], { home });
    expect(blocked.exitCode).toBe(1);
    expect(blocked.json.ok).toBe(false);
    expect(blocked.json.error.code).toBe("YOOOCLAW_ALREADY_EXISTS");

    const forced = await runCli(
      ["config", "init", "--from-file", importFile(), "--force"],
      { home },
    );
    expect(forced.exitCode).toBe(0);
    expect(forced.json.ok).toBe(true);
  });

  it("非交互且无 --from-file 报 NOT_INTERACTIVE", async () => {
    const res = await runCli(["config", "init", "--non-interactive"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_NOT_INTERACTIVE");
  });

  it("show 未初始化报 CONFIG_INVALID", async () => {
    const res = await runCli(["config", "show"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_CONFIG_INVALID");
  });

  it("show 返回结构化 config（默认不含明文 secret 值）", async () => {
    await runCli(["config", "init", "--from-file", importFile()], { home });
    const res = await runCli(["config", "show"], { home });
    expect(res.exitCode).toBe(0);
    expect(res.json.version).toBe(1);
    expect(res.json.daemon.bind).toBe("127.0.0.1");
    // tokenRef 是 file: 引用而非明文 token
    expect(res.json.auth.tokenRef).toMatch(/^file:/);
  });

  it("set/unset 按点号路径生效并落盘", async () => {
    await runCli(["config", "init", "--from-file", importFile()], { home });
    const set = await runCli(["config", "set", "daemon.port", "28123"], { home });
    expect(set.json).toMatchObject({ ok: true, key: "daemon.port", value: 28123 });

    const show = await runCli(["config", "show"], { home });
    expect(show.json.daemon.port).toBe(28123);

    const unset = await runCli(["config", "unset", "daemon.port"], { home });
    expect(unset.json).toMatchObject({ ok: true, removed: true });
  });

  it("set 非法数字 / version 字段报 INVALID_ARGUMENT", async () => {
    await runCli(["config", "init", "--from-file", importFile()], { home });
    const bad = await runCli(["config", "set", "daemon.port", "abc"], { home });
    expect(bad.exitCode).toBe(1);
    expect(bad.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");

    const ver = await runCli(["config", "set", "version", "2"], { home });
    expect(ver.exitCode).toBe(1);
    expect(ver.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });
});

describe("profile", () => {
  it("list 至少列出 default 并标 active", async () => {
    const res = await runCli(["profile", "list"], { home });
    expect(res.exitCode).toBe(0);
    const def = res.json.find((p: any) => p.name === "default");
    expect(def).toMatchObject({ active: true, initialized: false });
  });

  it("（用 config init --profile 建出 work）→ use → list 反映切换", async () => {
    const created = await runCli(
      ["config", "init", "--profile", "work", "--from-file", importFile()],
      { home },
    );
    expect(created.exitCode).toBe(0);
    expect(created.json.profile).toBe("work");

    const used = await runCli(["profile", "use", "work"], { home });
    expect(used.json).toMatchObject({ ok: true, active: "work" });

    const list = await runCli(["profile", "list"], { home });
    const work = list.json.find((p: any) => p.name === "work");
    expect(work).toMatchObject({ active: true, initialized: true });
  });

  it("create 走交互向导，非 TTY 下报 NOT_INTERACTIVE", async () => {
    // 命令树未给 `profile create` 暴露 --from-file，因此非交互环境只能落到向导分支并报错。
    const res = await runCli(["profile", "create", "tmp"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_NOT_INTERACTIVE");
  });

  it("use 不存在的 profile 报 PROFILE_NOT_FOUND", async () => {
    const res = await runCli(["profile", "use", "ghost"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_PROFILE_NOT_FOUND");
  });

  it("不能删除 active profile", async () => {
    const res = await runCli(["profile", "delete", "default"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });

  it("delete --yes 删除非 active profile", async () => {
    await runCli(["config", "init", "--profile", "tmp", "--from-file", importFile()], { home });
    const del = await runCli(["profile", "delete", "tmp", "--yes"], { home });
    expect(del.json).toMatchObject({ ok: true, deleted: "tmp" });
    expect(existsSync(profileDir(home, "tmp"))).toBe(false);
  });
});

describe("全局 flags", () => {
  it("非法 --format 报 INVALID_ARGUMENT 且非零退出", async () => {
    const res = await runCli(["config", "show", "--format", "xml"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });

  it("未实现命令回落 NOT_IMPLEMENTED", async () => {
    const res = await runCli(["notification", "+unread"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_NOT_IMPLEMENTED");
  });

  it("--profile 路由到对应 profile，互不影响", async () => {
    await runCli(["config", "init", "--profile", "work", "--from-file", importFile()], { home });
    expect(existsSync(join(profileDir(home, "work"), "config.json"))).toBe(true);
    // default 仍未初始化
    const def = await runCli(["config", "show"], { home });
    expect(def.json.error.code).toBe("YOOOCLAW_CONFIG_INVALID");
  });
});

describe("doctor", () => {
  it("未初始化时整体 ok（仅 warn），含确定性 checks", async () => {
    const res = await runCli(["doctor"], { home });
    expect(res.json.ok).toBe(true);
    const names = res.json.checks.map((c: any) => c.name);
    expect(names).toEqual(expect.arrayContaining(["node-version", "root-dir", "profile-config", "api-key"]));
    const profileCheck = res.json.checks.find((c: any) => c.name === "profile-config");
    expect(profileCheck.status).toBe("warn");
  });

  it("init 后 gateway-token 检查转 ok", async () => {
    await runCli(["config", "init", "--from-file", importFile()], { home });
    const res = await runCli(["doctor"], { home });
    const token = res.json.checks.find((c: any) => c.name === "gateway-token");
    expect(token.status).toBe("ok");
  });

  it("--json 作为 --format json 的兼容别名", async () => {
    const res = await runCli(["doctor", "--format", "pretty", "--json"], { home });
    expect(res.json.ok).toBe(true);
    expect(res.stdout.trim().split("\n")).toHaveLength(1);
  });
});

describe("migrate from-openclaw", () => {
  /** 造一个假的 ~/.openclaw 源目录。 */
  function fakeSource(): string {
    const src = join(home, "openclaw-src");
    const plugin = join(src, "plugins", "phone-notifications", "notifications");
    mkdirSync(plugin, { recursive: true });
    writeFileSync(join(plugin, "2026-05-21.json"), JSON.stringify([{ appName: "wechat", title: "t", content: "c", timestamp: "2026-05-21T10:00:00+08:00" }]), "utf-8");
    writeFileSync(join(src, "credentials.json"), JSON.stringify({ apiKey: "ock_legacy_key" }), "utf-8");
    return src;
  }

  it("--dry-run 只打印计划不写入", async () => {
    const src = fakeSource();
    const res = await runCli(["migrate", "from-openclaw", "--dry-run", "--source", src], { home });
    expect(res.json).toMatchObject({ ok: true, dryRun: true, source: src });
    expect(res.json.apiKey.action).toBe("copy");
    const ntf = res.json.subdirs.find((s: any) => s.name === "notifications");
    expect(ntf.exists).toBe(true);
    // dry-run 不应迁移
    expect(existsSync(join(profileDir(home), "notifications", "2026-05-21.json"))).toBe(false);
  });

  it("真跑迁移 notifications + api-key", async () => {
    const src = fakeSource();
    const res = await runCli(["migrate", "from-openclaw", "--source", src], { home });
    expect(res.json.ok).toBe(true);
    expect(res.json.migrated).toContain("notifications");
    expect(res.json.apiKey.action).toBe("copied");
    expect(existsSync(join(profileDir(home), "notifications", "2026-05-21.json"))).toBe(true);
    expect(existsSync(join(home, "credentials.json"))).toBe(true);
  });
});
