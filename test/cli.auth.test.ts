/**
 * L2 行为测试 —— auth（api-key 多态 / label 校验 / gateway token）。
 * 🟢 部分（不调 daemon）。账号级 api-key 落 home/credentials.json。
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { cleanHome, makeHome, profileDir, runCli, runCliSubprocess } from "./helpers/cli.js";

let home: string;
beforeEach(() => {
  home = makeHome();
});
afterEach(() => {
  cleanHome(home);
});

function importFile(): string {
  const p = join(home, "import.json");
  writeFileSync(p, "{}", "utf-8");
  return p;
}

const KEY_A = "ock_aaaa1111bbbb2222";
const KEY_B = "ock_cccc3333dddd4444";

describe("auth set/list api-key（单 key 文件态）", () => {
  it("set-api-key 写共享文件并遮罩回显", async () => {
    const res = await runCli(["auth", "set-api-key", KEY_A], { home });
    expect(res.json).toMatchObject({ ok: true, source: "file", label: "default" });
    expect(res.json.masked).toMatch(/\*\*\*/);
    expect(res.json.masked).not.toContain("1111");
    expect(existsSync(join(home, "credentials.json"))).toBe(true);

    const status = await runCli(["auth", "status"], { home });
    expect(status.json.apiKey).toMatchObject({ present: true, source: "file" });
  });

  it("空 key 报 INVALID_ARGUMENT", async () => {
    const res = await runCli(["auth", "set-api-key", ""], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });

  it("set-api-key - 从 stdin 读（子进程）", () => {
    const res = runCliSubprocess(["auth", "set-api-key", "-"], { home, input: KEY_A });
    expect(res.exitCode).toBe(0);
    expect(res.json).toMatchObject({ ok: true, source: "file" });
  });
});

describe("auth multi-key", () => {
  it("add-api-key 迁移 legacy 单 key 到 file-multi", async () => {
    await runCli(["auth", "set-api-key", KEY_A], { home });
    const add = await runCli(["auth", "add-api-key", KEY_B, "--label", "phone-b"], { home });
    expect(add.json).toMatchObject({ ok: true, mode: "file-multi", migratedLegacyApiKey: true });

    const list = await runCli(["auth", "list-api-keys"], { home });
    expect(list.json.mode).toBe("file-multi");
    const labels = list.json.items.map((i: any) => i.label).sort();
    expect(labels).toEqual(["default", "phone-b"]);
    for (const item of list.json.items) expect(item.masked).toMatch(/\*\*\*/);
  });

  it("add-api-key 缺 --label 报 INVALID_ARGUMENT", async () => {
    const res = await runCli(["auth", "add-api-key", KEY_A], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });

  it("非法 label 报 APIKEY_LABEL_INVALID", async () => {
    const res = await runCli(["auth", "add-api-key", KEY_A, "--label", "BAD_Label"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_APIKEY_LABEL_INVALID");
  });

  it("重复 label 报 DUPLICATE，--force 可覆盖", async () => {
    await runCli(["auth", "add-api-key", KEY_A, "--label", "phone-a"], { home });
    const dup = await runCli(["auth", "add-api-key", KEY_B, "--label", "phone-a"], { home });
    expect(dup.exitCode).toBe(1);
    expect(dup.json.error.code).toBe("YOOOCLAW_APIKEY_LABEL_DUPLICATE");

    const forced = await runCli(
      ["auth", "add-api-key", KEY_B, "--label", "phone-a", "--force"],
      { home },
    );
    expect(forced.json.ok).toBe(true);
  });

  it("set-default-api-key 切换 default", async () => {
    await runCli(["auth", "add-api-key", KEY_A, "--label", "phone-a"], { home });
    await runCli(["auth", "add-api-key", KEY_B, "--label", "phone-b"], { home });
    const def = await runCli(["auth", "set-default-api-key", "phone-b"], { home });
    expect(def.json).toMatchObject({ ok: true, defaultLabel: "phone-b" });
  });

  it("remove-api-key 删除条目；删不存在报 NOT_FOUND", async () => {
    await runCli(["auth", "add-api-key", KEY_A, "--label", "phone-a"], { home });
    await runCli(["auth", "add-api-key", KEY_B, "--label", "phone-b"], { home });
    const rm = await runCli(["auth", "remove-api-key", "phone-a"], { home });
    expect(rm.json).toMatchObject({ ok: true, removed: "phone-a", remaining: 1 });

    const miss = await runCli(["auth", "remove-api-key", "ghost"], { home });
    expect(miss.exitCode).toBe(1);
    expect(miss.json.error.code).toBe("YOOOCLAW_APIKEY_LABEL_NOT_FOUND");
  });
});

describe("auth gateway token", () => {
  it("token-rotate 未初始化报 CONFIG_INVALID", async () => {
    const res = await runCli(["auth", "token-rotate"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_CONFIG_INVALID");
  });

  it("token-rotate 生成新 token 并写入 credentials", async () => {
    await runCli(["config", "init", "--from-file", importFile(), "--no-start"], { home });
    const res = await runCli(["auth", "token-rotate"], { home });
    expect(res.json.ok).toBe(true);
    expect(typeof res.json.token).toBe("string");
    const creds = JSON.parse(readFileSync(join(profileDir(home), "credentials.json"), "utf-8"));
    expect(creds.gatewayToken).toBe(res.json.token);
  });

  it("token-rotate --length 太短报 INVALID_ARGUMENT", async () => {
    await runCli(["config", "init", "--from-file", importFile(), "--no-start"], { home });
    const res = await runCli(["auth", "token-rotate", "--length", "8"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });
});
