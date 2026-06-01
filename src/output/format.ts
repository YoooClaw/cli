/**
 * 统一输出层 —— 所有命令的 stdout 走这里序列化。
 *
 * 支持 `--format json|pretty|table|ndjson`：
 *   - json    JSON.stringify(data) 单行          （非 TTY / 管道默认）
 *   - pretty  JSON.stringify(data, null, 2)       （TTY 默认；后续可加颜色）
 *   - table   表格输出（结果为数组时）
 *   - ndjson  每条结果一行 JSON，无包裹数组        （流式 / Agent 消费）
 *
 * 错误与正常输出共用同一 schema：`{ ok: false, error: { code, message, ... } }`。
 */
import { YoooclawError, type ErrorCodeValue } from "../errors.js";

export type OutputFormat = "json" | "pretty" | "table" | "ndjson";
export const OUTPUT_FORMATS: OutputFormat[] = [
  "json",
  "pretty",
  "table",
  "ndjson",
];

/** 解析用户传入的 --format；`auto` / 缺省时按 TTY 判定。 */
export function resolveFormat(
  requested: string | undefined,
  isTty = process.stdout.isTTY ?? false,
): OutputFormat {
  if (requested && requested !== "auto") {
    if ((OUTPUT_FORMATS as string[]).includes(requested)) {
      return requested as OutputFormat;
    }
    throw new YoooclawError(
      "YOOOCLAW_INVALID_ARGUMENT" as ErrorCodeValue,
      `不支持的输出格式：${requested}`,
      { hint: `可选值：${OUTPUT_FORMATS.join(" | ")} | auto` },
    );
  }
  return isTty ? "pretty" : "json";
}

interface RenderOptions {
  format: OutputFormat;
  /** 写出目标，默认 process.stdout。便于测试。 */
  stream?: NodeJS.WritableStream;
}

function write(stream: NodeJS.WritableStream, text: string): void {
  stream.write(text.endsWith("\n") ? text : text + "\n");
}

/** 渲染一次成功结果。data 任意结构；table/ndjson 在数组时逐行展开。 */
export function renderResult(data: unknown, opts: RenderOptions): void {
  const stream = opts.stream ?? process.stdout;
  switch (opts.format) {
    case "json":
      write(stream, JSON.stringify(data));
      return;
    case "pretty":
      write(stream, JSON.stringify(data, null, 2));
      return;
    case "ndjson": {
      const rows = Array.isArray(data) ? data : [data];
      for (const row of rows) stream.write(JSON.stringify(row) + "\n");
      return;
    }
    case "table":
      write(stream, renderTable(data));
      return;
  }
}

/**
 * 渲染错误。统一 schema：{ ok: false, error: {...} }。
 * 默认写 stdout —— 与正常输出同一通道，让 `--format json | jq` 等 Agent 管道能读到错误体；
 * 失败由非零退出码表达。
 */
export function renderError(err: unknown, opts: RenderOptions): void {
  const stream = opts.stream ?? process.stdout;
  const error =
    err instanceof YoooclawError
      ? err.toErrorPayload()
      : {
          code: "YOOOCLAW_UNKNOWN",
          message: err instanceof Error ? err.message : String(err),
        };
  const payload = { ok: false, error };
  const text =
    opts.format === "pretty"
      ? JSON.stringify(payload, null, 2)
      : JSON.stringify(payload);
  write(stream, text);
}

/** 极简表格渲染：对象数组 → 列对齐文本；其余 fallback 到 pretty JSON。 */
function renderTable(data: unknown): string {
  if (!Array.isArray(data) || data.length === 0) {
    return JSON.stringify(data, null, 2);
  }
  const rows = data.filter(
    (r): r is Record<string, unknown> =>
      typeof r === "object" && r !== null && !Array.isArray(r),
  );
  if (rows.length !== data.length) {
    return JSON.stringify(data, null, 2);
  }
  const columns = Array.from(
    rows.reduce<Set<string>>((set, row) => {
      for (const key of Object.keys(row)) set.add(key);
      return set;
    }, new Set()),
  );
  const cell = (v: unknown): string =>
    v === null || v === undefined
      ? ""
      : typeof v === "object"
        ? JSON.stringify(v)
        : String(v);
  const widths = columns.map((col) =>
    Math.max(col.length, ...rows.map((row) => cell(row[col]).length)),
  );
  const pad = (text: string, width: number) => text.padEnd(width);
  const header = columns.map((col, i) => pad(col, widths[i])).join("  ");
  const divider = widths.map((w) => "-".repeat(w)).join("  ");
  const body = rows
    .map((row) =>
      columns.map((col, i) => pad(cell(row[col]), widths[i])).join("  "),
    )
    .join("\n");
  return [header, divider, body].join("\n");
}
