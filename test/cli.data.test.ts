/**
 * L2 行为测试 —— 纯读/写磁盘的数据类命令（🟢）：
 * notification / sync / image / recording / log。
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mkdirSync, writeFileSync, existsSync } from "node:fs";
import { join } from "node:path";
import {
  cleanHome,
  makeHome,
  profileDir,
  runCli,
  seedImages,
  seedNotifications,
  seedRecordings,
} from "./helpers/cli.js";

let home: string;
beforeEach(() => {
  home = makeHome();
});
afterEach(() => {
  cleanHome(home);
});

/** 本地时区今日 YYYY-MM-DD（与 daysAgo/today 的本地语义一致）。 */
function localToday(): string {
  const d = new Date();
  const p = (n: number): string => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

describe("notification search", () => {
  const DAY = "2026-05-21";
  beforeEach(() => {
    seedNotifications(home, DAY, [
      { appName: "com.tencent.xin", appDisplayName: "微信", title: "Alice", content: "晚上吃饭吗", timestamp: `${DAY}T10:00:00+08:00`, senderName: "Alice", clientLabel: "phone-a" },
      { appName: "com.tencent.xin", appDisplayName: "微信", title: "群通知", content: "周报提醒", timestamp: `${DAY}T11:00:00+08:00`, conversationType: "group", clientLabel: "phone-a" },
      { appName: "com.feishu", appDisplayName: "飞书", title: "Bob", content: "review PR", timestamp: `${DAY}T12:00:00+08:00`, senderName: "Bob", clientLabel: "phone-b" },
    ]);
  });

  it("时间倒序返回全部", async () => {
    const res = await runCli(["notification", "search", "--from", `${DAY}T00:00:00+08:00`, "--to", `${DAY}T23:59:59+08:00`], { home });
    expect(res.exitCode).toBe(0);
    expect(res.json).toHaveLength(3);
    expect(res.json[0].content).toBe("review PR"); // 12:00 最新
  });

  it("--keyword / --app / --client 过滤", async () => {
    const kw = await runCli(["notification", "search", "--keyword", "周报"], { home });
    expect(kw.json).toHaveLength(1);
    expect(kw.json[0].content).toBe("周报提醒");

    const app = await runCli(["notification", "search", "--app", "飞书"], { home });
    expect(app.json).toHaveLength(1);

    const client = await runCli(["notification", "search", "--client", "phone-b"], { home });
    expect(client.json).toHaveLength(1);
    expect(client.json[0].title).toBe("Bob");
  });

  it("--conversation-type 非法值报 INVALID_ARGUMENT", async () => {
    const res = await runCli(["notification", "search", "--conversation-type", "channel"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });

  it("非法 --from 报 INVALID_ARGUMENT", async () => {
    const res = await runCli(["notification", "search", "--from", "2026/05/21"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });

  it("summary 聚合 topApps/topSenders", async () => {
    const res = await runCli(["notification", "summary"], { home });
    expect(res.json.ok).toBe(true);
    expect(res.json.total).toBe(3);
    const wechat = res.json.topApps.find((a: any) => a.key === "微信");
    expect(wechat.count).toBe(2);
  });

  it("stats --dim app 按应用聚合", async () => {
    const res = await runCli(["notification", "stats", "--from", DAY, "--to", DAY, "--dim", "app"], { home });
    expect(res.json.ok).toBe(true);
    expect(res.json.dim).toBe("app");
    expect(res.json.app.find((a: any) => a.key === "微信").count).toBe(2);
  });

  it("stats 非法 --dim 报 INVALID_ARGUMENT", async () => {
    const res = await runCli(["notification", "stats", "--dim", "bogus"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });

  it("storage-path 打印绝对路径", async () => {
    const res = await runCli(["notification", "storage-path"], { home });
    expect(res.json.path).toBe(join(profileDir(home), "notifications"));
  });

  it("空目录返回空数组而非报错", async () => {
    const empty = makeHome();
    const res = await runCli(["notification", "search"], { home: empty });
    expect(res.exitCode).toBe(0);
    expect(res.json).toEqual([]);
    cleanHome(empty);
  });
});

describe("notification +today", () => {
  it("命中今日通知", async () => {
    const day = localToday();
    seedNotifications(home, day, [
      { appName: "com.feishu", title: "今日", content: "hi", timestamp: new Date().toISOString() },
    ]);
    const res = await runCli(["notification", "+today"], { home });
    expect(res.exitCode).toBe(0);
    expect(res.json).toHaveLength(1);
  });
});

describe("sync scan/fetch/commit", () => {
  const DAY = "2026-05-21";
  beforeEach(() => {
    seedNotifications(home, DAY, [
      { appName: "a", title: "t0", content: "c0", timestamp: `${DAY}T10:00:00+08:00` },
      { appName: "a", title: "t1", content: "c1", timestamp: `${DAY}T10:01:00+08:00` },
    ]);
  });

  it("scan → fetch → commit 推进 checkpoint", async () => {
    const scan = await runCli(["sync", "scan"], { home });
    expect(scan.json.totalPending).toBe(2);

    const fetch = await runCli(["sync", "fetch", "--date", DAY], { home });
    expect(fetch.json.returned).toBe(2);

    await runCli(["sync", "commit", "--date", DAY, "--end-index", String(fetch.json.endIndex)], { home });
    const scan2 = await runCli(["sync", "scan"], { home });
    expect(scan2.json.totalPending).toBe(0);
  });

  it("fetch 缺 --date 报 INVALID_ARGUMENT", async () => {
    const res = await runCli(["sync", "fetch"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });

  it("fetch 不存在日期报错", async () => {
    const res = await runCli(["sync", "fetch", "--date", "2099-01-01"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.ok).toBe(false);
  });
});

describe("image", () => {
  beforeEach(() => {
    seedImages(home, [
      { imageId: "img_ok", metadata: { oss_image_url: "x", created_at: "2026-05-21T10:00:00+08:00", source_app: "微信" }, localFile: "files/img_ok.jpg", status: "synced", clientLabel: "phone-a" },
      { imageId: "img_pending", metadata: { oss_image_url: "y", created_at: "2026-05-21T11:00:00+08:00" }, status: "syncing" },
    ]);
  });

  it("list 总数 + 按 status 过滤", async () => {
    const all = await runCli(["image", "list"], { home });
    expect(all.json.total).toBe(2);
    const synced = await runCli(["image", "list", "--status", "synced"], { home });
    expect(synced.json.total).toBe(1);
  });

  it("status 命中 / 未命中报 NOT_FOUND", async () => {
    const ok = await runCli(["image", "status", "img_ok"], { home });
    expect(ok.json.image.imageId).toBe("img_ok");
    const miss = await runCli(["image", "status", "ghost"], { home });
    expect(miss.exitCode).toBe(1);
    expect(miss.json.error.code).toBe("YOOOCLAW_NOT_FOUND");
  });

  it("path 已就绪返回绝对路径，未就绪报 IMAGE_NOT_READY", async () => {
    const ok = await runCli(["image", "path", "img_ok"], { home });
    expect(ok.json.path).toBe(join(profileDir(home), "images", "files/img_ok.jpg"));
    const notReady = await runCli(["image", "path", "img_pending"], { home });
    expect(notReady.exitCode).toBe(1);
    expect(notReady.json.error.code).toBe("YOOOCLAW_IMAGE_NOT_READY");
  });

  it("+latest 返回最新一张", async () => {
    const res = await runCli(["image", "+latest"], { home });
    expect(res.json.image.imageId).toBe("img_pending"); // 11:00 更新
  });
});

describe("recording", () => {
  beforeEach(() => {
    seedRecordings(home, [
      {
        id: "rec_1",
        clientLabel: "phone-a",
        metadata: { name: "会议", duration_sec: 120, file_size_bytes: 1024, created_at: "2026-05-21T10:00:00+08:00", transfer_status: "synced" },
        status: "synced",
        audioFile: "rec_1.m4a",
        ingestedAt: "2026-05-21T10:00:01+08:00",
        updatedAt: "2026-05-21T10:00:02+08:00",
      },
    ]);
  });

  it("list / status / storage-path", async () => {
    const list = await runCli(["recording", "list"], { home });
    expect(list.json.total).toBe(1);
    expect(list.json.recordings[0]).toMatchObject({ id: "rec_1", has_audio: true });

    const status = await runCli(["recording", "status", "rec_1"], { home });
    expect(status.json.recording.name).toBe("会议");

    const sp = await runCli(["recording", "storage-path"], { home });
    expect(sp.json.path).toBe(join(profileDir(home), "recordings"));
  });

  it("status 未命中报 NOT_FOUND", async () => {
    const res = await runCli(["recording", "status", "ghost"], { home });
    expect(res.exitCode).toBe(1);
    expect(res.json.error.code).toBe("YOOOCLAW_NOT_FOUND");
  });

  it("setup-asr --non-interactive 写出 asr-config.json", async () => {
    const res = await runCli(["recording", "setup-asr", "--non-interactive", "--mode", "api"], { home });
    expect(res.json).toMatchObject({ ok: true, mode: "api" });
    expect(existsSync(join(profileDir(home), "recordings", "asr-config.json"))).toBe(true);

    const bad = await runCli(["recording", "setup-asr", "--non-interactive", "--mode", "bogus"], { home });
    expect(bad.exitCode).toBe(1);
    expect(bad.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });

  it("events 读取 JSONL + --id 过滤", async () => {
    const dir = join(profileDir(home), "recordings", "state");
    mkdirSync(dir, { recursive: true });
    const lines = [
      { ts: "2026-05-21T10:00:00+08:00", recordingId: "rec_1", transfer_status: "syncing" },
      { ts: "2026-05-21T10:00:05+08:00", recordingId: "rec_2", transfer_status: "synced" },
    ].map((e) => JSON.stringify(e)).join("\n");
    writeFileSync(join(dir, "events.jsonl"), lines, "utf-8");

    const all = await runCli(["recording", "events"], { home });
    expect(all.json.total).toBe(2);

    const one = await runCli(["recording", "events", "--id", "rec_1"], { home });
    expect(one.json.total).toBe(1);
    expect(one.json.events[0].recordingId).toBe("rec_1");

    const badSince = await runCli(["recording", "events", "--since", "10x"], { home });
    expect(badSince.exitCode).toBe(1);
    expect(badSince.json.error.code).toBe("YOOOCLAW_INVALID_ARGUMENT");
  });
});

describe("log", () => {
  beforeEach(() => {
    const day = localToday();
    const content = [
      `${day}T10:00:00+08:00 [INFO] daemon 启动`,
      `${day}T10:01:00+08:00 [ERROR] relay 连接失败`,
      `${day}T10:02:00+08:00 [WARN] 端口被占用`,
    ].join("\n");
    writeFileSync(join(profileDir(home), "daemon.log"), content + "\n", "utf-8");
  });

  it("keyword + level 过滤", async () => {
    const kw = await runCli(["log", "relay"], { home });
    expect(kw.json.total).toBe(1);
    expect(kw.json.lines[0].raw).toContain("relay 连接失败");

    const lvl = await runCli(["log", "--level", "error"], { home });
    expect(lvl.json.total).toBe(1);
  });

  it("+errors 只看 error 级", async () => {
    const res = await runCli(["log", "+errors"], { home });
    expect(res.json.level).toBe("error");
    expect(res.json.total).toBe(1);
  });
});
