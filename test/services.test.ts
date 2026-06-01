import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import {
  defaultConfig,
  SECRET_REF_PATHS,
} from "../src/config/schema.js";
import {
  getByPath,
  setByPath,
  unsetByPath,
  coerceConfigValue,
  maskConfig,
} from "../src/config/store.js";
import { resolveRef, writeRef } from "../src/credentials/refs.js";
import {
  addApiKey,
  removeApiKey,
  resolveApiKey,
  resolveApiKeyEntries,
  setApiKey,
} from "../src/credentials/store.js";
import { scanSync, fetchSync, commitSync } from "../src/notification/sync.js";
import { readImageIndex, resolveImageFile } from "../src/image/storage.js";
import { profilePaths } from "../src/paths.js";
import { HANDLERS } from "../src/commands/registry.js";
import { COMMAND_TREE } from "../src/command-tree.js";

let home: string;

beforeEach(() => {
  home = mkdtempSync(join(tmpdir(), "yc-test-"));
  process.env.YOOOCLAW_HOME = home;
  delete process.env.YOOOCLAW_API_KEY;
});
afterEach(() => {
  rmSync(home, { recursive: true, force: true });
  delete process.env.YOOOCLAW_HOME;
});

describe("config store", () => {
  it("get/set/unset by dot path", () => {
    const cfg = defaultConfig("/x/creds.json") as unknown as Record<string, unknown>;
    setByPath(cfg, "daemon.port", 28000);
    expect(getByPath(cfg, "daemon.port")).toBe(28000);
    expect(unsetByPath(cfg, "daemon.port")).toBe(true);
    expect(getByPath(cfg, "daemon.port")).toBeUndefined();
  });

  it("coerces typed values", () => {
    expect(coerceConfigValue("daemon.port", "123")).toBe(123);
    expect(coerceConfigValue("relay.enabled", "false")).toBe(false);
    expect(coerceConfigValue("notification.ignoredApps", "a,b, c")).toEqual(["a", "b", "c"]);
    expect(coerceConfigValue("notification.retentionDays", "null")).toBeNull();
    expect(() => coerceConfigValue("daemon.port", "abc")).toThrow();
  });

  it("masks inline secret refs only", () => {
    const cfg = defaultConfig("/x/creds.json");
    cfg.auth.tokenRef = "inline:c2VjcmV0";
    const masked = maskConfig(cfg);
    expect(getByPath(masked, "auth.tokenRef")).toBe("inline:****");
    expect(SECRET_REF_PATHS).toContain("auth.tokenRef");
  });
});

describe("credential refs", () => {
  it("file scheme round-trips", () => {
    const file = join(home, "creds.json");
    const ref = `file:${file}#gatewayToken`;
    writeRef(ref, "tok_123");
    expect(resolveRef(ref).value).toBe("tok_123");
  });

  it("env scheme reads from environment", () => {
    process.env.YC_TEST_REF = "envval";
    expect(resolveRef("env:YC_TEST_REF").value).toBe("envval");
    delete process.env.YC_TEST_REF;
  });

  it("inline scheme decodes base64", () => {
    const ref = `inline:${Buffer.from("hello").toString("base64")}`;
    expect(resolveRef(ref).value).toBe("hello");
  });
});

describe("api-key resolution", () => {
  it("env overrides file", () => {
    setApiKey("file_key_abcd", false);
    expect(resolveApiKey().source).toBe("file");
    process.env.YOOOCLAW_API_KEY = "env_key";
    expect(resolveApiKey()).toMatchObject({ source: "env", value: "env_key" });
  });

  it("migrates legacy file key when adding the first labeled key", () => {
    setApiKey("legacy_key", false);
    addApiKey("phone_key", "phone-a");

    const set = resolveApiKeyEntries();
    expect(set.mode).toBe("file-multi");
    expect(set.entries.map((entry) => entry.label)).toEqual(["default", "phone-a"]);
    expect(set.defaultEntry).toMatchObject({ label: "default", key: "legacy_key" });
  });

  it("rotates only the default key in file-multi mode", () => {
    addApiKey("phone_a_key", "phone-a");
    addApiKey("phone_b_key", "phone-b", { makeDefault: true });

    setApiKey("phone_b_new", false);

    const set = resolveApiKeyEntries();
    expect(set.defaultEntry).toMatchObject({ label: "phone-b", key: "phone_b_new" });
    expect(set.entries.find((entry) => entry.label === "phone-a")?.key).toBe("phone_a_key");
  });

  it("keeps empty apiKeys as explicit file-multi none", () => {
    addApiKey("phone_a_key", "phone-a");
    removeApiKey("phone-a");

    const set = resolveApiKeyEntries();
    expect(set).toMatchObject({ mode: "file-multi", entries: [] });
    expect(resolveApiKey()).toMatchObject({ source: "none" });
  });
});

describe("notification sync checkpoint", () => {
  function seed(count: number): string {
    const paths = profilePaths("default");
    mkdirSync(paths.notifications, { recursive: true });
    const items = Array.from({ length: count }, (_, i) => ({
      appName: "wechat",
      title: `t${i}`,
      content: `c${i}`,
      timestamp: `2026-05-21T10:00:0${i % 10}+08:00`,
    }));
    writeFileSync(join(paths.notifications, "2026-05-21.json"), JSON.stringify(items));
    return paths.notifications;
  }

  it("scan → fetch → commit advances checkpoint", () => {
    const dir = seed(3);
    const scan = scanSync(dir) as { totalPending: number };
    expect(scan.totalPending).toBe(3);

    const fetched = fetchSync(dir, "2026-05-21") as { returned: number; endIndex: number };
    expect(fetched.returned).toBe(3);

    commitSync(dir, "2026-05-21", String(fetched.endIndex));
    const scan2 = scanSync(dir) as { totalPending: number };
    expect(scan2.totalPending).toBe(0);
  });

  it("fetch on missing date throws NOT_FOUND", () => {
    const dir = seed(1);
    expect(() => fetchSync(dir, "2099-01-01")).toThrow();
  });
});

describe("image storage", () => {
  it("reads index and resolves relative file path", () => {
    const paths = profilePaths("default");
    mkdirSync(paths.images, { recursive: true });
    writeFileSync(
      join(paths.images, "index.json"),
      JSON.stringify({ images: [{ imageId: "img_1", metadata: { oss_image_url: "x", created_at: "2026-05-21T10:00:00+08:00" }, localFile: "files/img_1.jpg", status: "synced" }] }),
    );
    const entries = readImageIndex(paths);
    expect(entries).toHaveLength(1);
    expect(resolveImageFile(paths, entries[0].localFile!)).toBe(join(paths.images, "files/img_1.jpg"));
  });
});

describe("command registry consistency", () => {
  it("every handler key maps to a real command path in the tree", () => {
    const validPaths = new Set<string>();
    for (const svc of COMMAND_TREE) {
      // 无 subcommands 的 service 注册为 leaf（handler key = service 名）
      if (!svc.subcommands?.length) validPaths.add(svc.name);
      for (const sub of svc.subcommands ?? []) validPaths.add(`${svc.name} ${sub.name.split(/\s+/)[0]}`);
      for (const sc of svc.shortcuts ?? []) validPaths.add(`${svc.name} ${sc.name}`);
    }
    for (const key of Object.keys(HANDLERS)) {
      expect(validPaths.has(key), `handler "${key}" has no command-tree entry`).toBe(true);
    }
  });
});
