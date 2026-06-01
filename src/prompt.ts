/**
 * 交互式输入工具（config init / setup-asr / 删除确认等）。
 *
 * 非 TTY 环境调用任何 prompt 都抛 NOT_INTERACTIVE，避免在管道里挂起。
 */
import { createInterface } from "node:readline/promises";
import { stdin, stdout } from "node:process";
import { YoooclawError } from "./errors.js";

export function isInteractive(): boolean {
  return Boolean(stdin.isTTY && stdout.isTTY);
}

function ensureInteractive(): void {
  if (!isInteractive()) {
    throw new YoooclawError(
      "YOOOCLAW_NOT_INTERACTIVE",
      "当前为非交互环境，无法进行交互式输入",
      { hint: "改用 --non-interactive --from-file，或在 TTY 中运行" },
    );
  }
}

/** 提一个问题，返回去空白后的回答；提供 defaultValue 时回车采用默认。 */
export async function ask(question: string, defaultValue?: string): Promise<string> {
  ensureInteractive();
  const suffix = defaultValue ? ` [${defaultValue}]` : "";
  const rl = createInterface({ input: stdin, output: stdout });
  try {
    const answer = (await rl.question(`${question}${suffix}: `)).trim();
    return answer || defaultValue || "";
  } finally {
    rl.close();
  }
}

/** Yes/No 确认，默认值由 defaultYes 决定。 */
export async function confirm(question: string, defaultYes = false): Promise<boolean> {
  const hint = defaultYes ? "Y/n" : "y/N";
  const answer = (await ask(`${question} (${hint})`)).toLowerCase();
  if (!answer) return defaultYes;
  return answer === "y" || answer === "yes";
}

/** 读取 stdin 全部内容（用于 `--from-file -` / `set-api-key -`）。 */
export async function readStdin(): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const chunk of stdin) {
    chunks.push(Buffer.from(chunk));
  }
  return Buffer.concat(chunks).toString("utf-8");
}
