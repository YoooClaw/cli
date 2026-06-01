/**
 * daemon 日志读取 —— 解析 `daemon.log` + 轮转文件 `daemon.log.YYYY-MM-DD`。
 *
 * 行格式（与 daemon logger 写入一致）：`<ISO时间> [LEVEL] message`。
 */
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { basename, dirname, join } from "node:path";

export interface LogLine {
  date: string;
  level: string;
  time: string;
  message: string;
  raw: string;
}

const LINE_RE = /^(\d{4}-\d{2}-\d{2})(T\S+)?\s+\[(\w+)\]\s+(.*)$/;

export interface LogQuery {
  keyword?: string;
  level?: string;
  from?: string;
  to?: string;
  limit: number;
}

/** 收集 daemon 当前日志 + 轮转日志文件路径（按日期倒序，current 在最前）。 */
function logFiles(daemonLog: string): string[] {
  const dir = dirname(daemonLog);
  const base = basename(daemonLog);
  if (!existsSync(dir)) return [];
  const rotated = readdirSync(dir)
    .filter((f) => f.startsWith(`${base}.`) && /\.\d{4}-\d{2}-\d{2}$/.test(f))
    .sort((a, b) => b.localeCompare(a))
    .map((f) => join(dir, f));
  const files: string[] = [];
  if (existsSync(daemonLog)) files.push(daemonLog);
  files.push(...rotated);
  return files;
}

function parseLine(raw: string): LogLine | null {
  const m = LINE_RE.exec(raw);
  if (!m) return null;
  return {
    date: m[1],
    time: (m[1] + (m[2] ?? "")),
    level: m[3].toLowerCase(),
    message: m[4],
    raw,
  };
}

export function searchLogs(daemonLog: string, query: LogQuery): LogLine[] {
  const keyword = query.keyword?.toLowerCase();
  const level = query.level?.toLowerCase();
  const results: LogLine[] = [];

  for (const file of logFiles(daemonLog)) {
    const content = readFileSync(file, "utf-8");
    // 单文件内按出现顺序，整体最新文件在前 → 反转每个文件使最新行靠前
    const lines = content.split("\n").filter(Boolean).reverse();
    for (const raw of lines) {
      if (results.length >= query.limit) return results;
      const parsed = parseLine(raw);
      const date = parsed?.date;
      if (query.from && date && date < query.from) continue;
      if (query.to && date && date > query.to) continue;
      if (level && parsed && parsed.level !== level) continue;
      if (keyword && !raw.toLowerCase().includes(keyword)) continue;
      results.push(parsed ?? { date: "", time: "", level: "", message: raw, raw });
    }
  }
  return results;
}
