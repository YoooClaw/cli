/**
 * daemon 文件 logger —— 写 `daemon.log`，按日期轮转为 `daemon.log.YYYY-MM-DD`。
 * 行格式：`<ISO本地时间> [LEVEL] message`（与 src/log/reader.ts 解析一致）。
 */
import { appendFileSync, existsSync, renameSync, statSync } from "node:fs";
import { dirname } from "node:path";
import { ensureDir } from "../fs-utils.js";
import type { LogLevel } from "../config/schema.js";

const LEVEL_ORDER: Record<string, number> = {
  error: 0,
  warn: 1,
  info: 2,
  debug: 3,
  trace: 4,
};

function isoLocal(d: Date): string {
  const pad = (n: number, w = 2) => String(n).padStart(w, "0");
  const off = -d.getTimezoneOffset();
  const sign = off >= 0 ? "+" : "-";
  const abs = Math.abs(off);
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    `T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}` +
    `${sign}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}`
  );
}

function dateKey(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

export class DaemonLogger {
  private currentDay: string;

  constructor(
    private readonly logFile: string,
    private readonly level: LogLevel = "info",
    private readonly alsoStderr = false,
  ) {
    ensureDir(dirname(logFile));
    this.currentDay = dateKey(new Date());
    this.rotateIfNeeded(new Date());
  }

  private enabled(level: string): boolean {
    return (LEVEL_ORDER[level] ?? 2) <= (LEVEL_ORDER[this.level] ?? 2);
  }

  private rotateIfNeeded(now: Date): void {
    if (!existsSync(this.logFile)) return;
    let fileDay: string | undefined;
    try {
      fileDay = dateKey(statSync(this.logFile).mtime);
    } catch {
      return;
    }
    const today = dateKey(now);
    if (fileDay !== today) {
      try {
        renameSync(this.logFile, `${this.logFile}.${fileDay}`);
      } catch {
        // 轮转失败不影响写入
      }
    }
  }

  private write(level: string, msg: string): void {
    if (!this.enabled(level)) return;
    const now = new Date();
    if (dateKey(now) !== this.currentDay) {
      this.rotateIfNeeded(now);
      this.currentDay = dateKey(now);
    }
    const line = `${isoLocal(now)} [${level.toUpperCase()}] ${msg}\n`;
    try {
      appendFileSync(this.logFile, line);
    } catch {
      // 忽略写文件失败
    }
    if (this.alsoStderr) process.stderr.write(line);
  }

  debug(msg: string): void { this.write("debug", msg); }
  info(msg: string): void { this.write("info", msg); }
  warn(msg: string): void { this.write("warn", msg); }
  error(msg: string): void { this.write("error", msg); }
}
