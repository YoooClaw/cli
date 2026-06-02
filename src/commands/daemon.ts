/**
 * daemon service —— start / stop / restart / status / logs / run-foreground（🔵）。
 */
import { spawn } from "node:child_process";
import { existsSync, readFileSync, statSync, watchFile } from "node:fs";
import { setTimeout as sleep } from "node:timers/promises";
import type { CliContext } from "../context.js";
import { YoooclawError } from "../errors.js";
import { requireConfig } from "../config/store.js";
import { daemonState, isProcessAlive, readLock, removeLock } from "../daemon/lock.js";
import { DaemonClient } from "../daemon/client.js";
import { runDaemonForeground, type DaemonStartOpts } from "../daemon/main.js";

interface StartOpts {
  bind?: string;
  port?: string;
  detach?: boolean; // commander：--no-detach → false
  logLevel?: string;
}

function startOpts(opts: StartOpts): DaemonStartOpts {
  return {
    bind: opts.bind,
    port: opts.port ? Number(opts.port) : undefined,
    logLevel: opts.logLevel,
  };
}

/** 内部命令：前台运行 daemon 主循环（detach 子进程入口）。 */
export async function daemonRunForeground(
  ctx: CliContext,
  _args: unknown[],
  opts: StartOpts,
): Promise<unknown> {
  await runDaemonForeground(ctx, startOpts(opts));
  return { ok: true }; // 实际不会到这（runDaemonForeground 不 resolve）
}

export async function daemonStart(
  ctx: CliContext,
  _args: unknown[],
  opts: StartOpts,
): Promise<unknown> {
  requireConfig(ctx.paths);
  const state = daemonState(ctx.paths);
  if (state.running) {
    throw new YoooclawError(
      "YOOOCLAW_DAEMON_ALREADY_RUNNING",
      `daemon 已在运行（pid ${state.lock?.pid}）`,
      { pid: state.lock?.pid },
    );
  }
  if (state.stale) removeLock(ctx.paths);

  // --no-detach：前台运行（systemd/launchd 包装）
  if (opts.detach === false) {
    await runDaemonForeground(ctx, startOpts(opts));
    return { ok: true };
  }

  // 默认：fork 子进程跑 run-foreground 并 detach
  const binEntry = process.argv[1];
  const args = [binEntry, "daemon", "run-foreground", "--profile", ctx.profile];
  if (opts.bind) args.push("--bind", opts.bind);
  if (opts.port) args.push("--port", opts.port);
  if (opts.logLevel) args.push("--log-level", opts.logLevel);

  const child = spawn(process.execPath, args, {
    detached: true,
    stdio: "ignore",
    windowsHide: true,
    env: process.env,
  });
  child.unref();

  // 轮询 lock 确认起来（最多 ~3s）
  for (let i = 0; i < 30; i += 1) {
    await sleep(100);
    const s = daemonState(ctx.paths);
    if (s.running) {
      return { ok: true, pid: s.lock?.pid, bind: s.lock?.bind, port: s.lock?.port, detached: true };
    }
  }
  throw new YoooclawError(
    "YOOOCLAW_UNKNOWN",
    "daemon 启动超时（3s 内未写出 lock）",
    { hint: "查看 yoooclaw daemon logs 排查" },
  );
}

export async function daemonStop(ctx: CliContext): Promise<unknown> {
  const lock = readLock(ctx.paths);
  if (!lock || !isProcessAlive(lock.pid)) {
    removeLock(ctx.paths);
    throw new YoooclawError("YOOOCLAW_DAEMON_NOT_RUNNING", "daemon 未运行");
  }

  // Windows 上 Node 没有真正的 POSIX 信号：process.kill(pid, "SIGTERM") 等价于
  // TerminateProcess，daemon 的 shutdown() 优雅退出钩子（隧道断开、移除 lock）不会跑。
  // 因此 Windows 优先打 HTTP /daemon/stop 让 daemon 自己走优雅退出，失败再回退到硬杀。
  const graceful = process.platform === "win32"
    ? "stop-endpoint"
    : "SIGTERM";
  if (graceful === "stop-endpoint") {
    try {
      await new DaemonClient(ctx.paths).post("/daemon/stop");
    } catch {
      /* daemon 可能拒绝鉴权或刚退出；继续轮询 + 回退硬杀 */
    }
  } else {
    try {
      process.kill(lock.pid, "SIGTERM");
    } catch {
      /* 进程可能刚退出 */
    }
  }

  // 等待优雅退出，最多 10s
  for (let i = 0; i < 100; i += 1) {
    await sleep(100);
    if (!isProcessAlive(lock.pid)) {
      removeLock(ctx.paths);
      return { ok: true, stopped: lock.pid, signal: graceful };
    }
  }
  try {
    process.kill(lock.pid, "SIGKILL");
  } catch {
    /* ignore */
  }
  removeLock(ctx.paths);
  return { ok: true, stopped: lock.pid, signal: "SIGKILL" };
}

export async function daemonRestart(
  ctx: CliContext,
  args: unknown[],
  opts: StartOpts,
): Promise<unknown> {
  const lock = readLock(ctx.paths);
  const inheritedOpts: StartOpts = {
    ...opts,
    bind: opts.bind ?? lock?.bind,
    port: opts.port ?? (lock?.port ? String(lock.port) : undefined),
    logLevel: opts.logLevel ?? lock?.logLevel,
  };
  if (lock && isProcessAlive(lock.pid)) {
    await daemonStop(ctx);
  }
  return daemonStart(ctx, args, inheritedOpts);
}

export async function daemonReload(ctx: CliContext): Promise<unknown> {
  const state = daemonState(ctx.paths);
  if (!state.running) {
    return { ok: true, running: false, reloaded: false };
  }
  const client = new DaemonClient(ctx.paths);
  const res = await client.post("/daemon/reload");
  return res.body;
}

export async function daemonStatus(ctx: CliContext): Promise<unknown> {
  const state = daemonState(ctx.paths);
  if (!state.running) {
    throw new YoooclawError(
      "YOOOCLAW_DAEMON_NOT_RUNNING",
      "daemon 未运行",
      { stale: state.stale, hint: "yoooclaw daemon start" },
    );
  }
  const client = new DaemonClient(ctx.paths);
  const res = await client.get("/daemon/status");
  return res.body;
}

interface LogsOpts {
  follow?: boolean;
  lines?: string;
  level?: string;
}

function tailLines(file: string, n: number): string[] {
  if (!existsSync(file)) return [];
  const content = readFileSync(file, "utf-8").split("\n").filter(Boolean);
  return content.slice(-n);
}

export async function daemonLogs(
  ctx: CliContext,
  _args: unknown[],
  opts: LogsOpts,
): Promise<unknown> {
  const n = opts.lines ? Number(opts.lines) : 100;
  const file = ctx.paths.daemonLog;
  let lines = tailLines(file, n);
  if (opts.level) {
    const want = `[${opts.level.toUpperCase()}]`;
    lines = lines.filter((l) => l.includes(want));
  }

  if (!opts.follow) {
    return { ok: true, file, total: lines.length, lines };
  }

  // --follow：先打印 tail，再持续输出新增行（raw 到 stdout，不经 --format）
  for (const l of lines) process.stdout.write(l + "\n");
  let size = existsSync(file) ? statSync(file).size : 0;
  await new Promise<void>(() => {
    watchFile(file, { interval: 500 }, () => {
      if (!existsSync(file)) return;
      const cur = statSync(file).size;
      if (cur > size) {
        const chunk = readFileSync(file, "utf-8").slice(-(cur - size));
        size = cur;
        process.stdout.write(chunk);
      } else if (cur < size) {
        size = cur; // 轮转
      }
    });
  });
  return { ok: true };
}
