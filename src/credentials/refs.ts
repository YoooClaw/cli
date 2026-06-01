/**
 * 凭据抽象引用（`*Ref`）解析与写入。
 *
 * 支持 scheme（PRD「config.json 结构」）：
 *   - env:<VAR>                  从环境变量读（最高优先级覆盖）
 *   - file:<path>#<jsonField>    从 JSON 文件某字段读/写（默认落点，0600）
 *   - keychain:<service>/<acct>  从 OS keychain 读/写（可选加固）
 *   - inline:<base64>            直接内嵌（既不支持 keychain 也不便用文件时）
 */
import { homedir } from "node:os";
import { join } from "node:path";
import { readJsonFile, writeJsonFile, SECRET_FILE_MODE } from "../fs-utils.js";
import { YoooclawError } from "../errors.js";
import { keychainGet, keychainSet } from "./keychain.js";

export type RefSource = "env" | "file" | "keychain" | "inline";

export interface ResolvedRef {
  value?: string;
  source: RefSource;
  /** file: scheme 时的具体路径，用于错误诊断。 */
  location?: string;
}

function expandHome(path: string): string {
  if (path === "~") return homedir();
  if (path.startsWith("~/")) return join(homedir(), path.slice(2));
  return path;
}

interface ParsedFileRef {
  path: string;
  field: string;
}
interface ParsedKeychainRef {
  service: string;
  account: string;
}

function parseFileRef(rest: string): ParsedFileRef {
  const hashIdx = rest.lastIndexOf("#");
  if (hashIdx < 0) {
    throw new YoooclawError(
      "YOOOCLAW_CONFIG_INVALID",
      `file: 引用缺少 #字段：${rest}`,
      { hint: "格式应为 file:<path>#<jsonField>" },
    );
  }
  return {
    path: expandHome(rest.slice(0, hashIdx)),
    field: rest.slice(hashIdx + 1),
  };
}

function parseKeychainRef(rest: string): ParsedKeychainRef {
  const slashIdx = rest.indexOf("/");
  if (slashIdx < 0) {
    throw new YoooclawError(
      "YOOOCLAW_CONFIG_INVALID",
      `keychain: 引用缺少 /account：${rest}`,
      { hint: "格式应为 keychain:<service>/<account>" },
    );
  }
  return {
    service: rest.slice(0, slashIdx),
    account: rest.slice(slashIdx + 1),
  };
}

/** 解析一个 ref，返回当前值（可能 undefined）与来源。 */
export function resolveRef(ref: string): ResolvedRef {
  const colonIdx = ref.indexOf(":");
  if (colonIdx < 0) {
    throw new YoooclawError(
      "YOOOCLAW_CONFIG_INVALID",
      `非法凭据引用：${ref}`,
      { hint: "支持 env: / file: / keychain: / inline:" },
    );
  }
  const scheme = ref.slice(0, colonIdx);
  const rest = ref.slice(colonIdx + 1);

  switch (scheme) {
    case "env": {
      const value = process.env[rest]?.trim();
      return { source: "env", value: value || undefined, location: rest };
    }
    case "file": {
      const { path, field } = parseFileRef(rest);
      const data = readJsonFile<Record<string, unknown>>(path);
      const value = data?.[field];
      return {
        source: "file",
        value: typeof value === "string" && value ? value : undefined,
        location: path,
      };
    }
    case "keychain": {
      const { service, account } = parseKeychainRef(rest);
      const r = keychainGet(service, account);
      return { source: "keychain", value: r.value, location: `${service}/${account}` };
    }
    case "inline": {
      const decoded = Buffer.from(rest, "base64").toString("utf-8");
      return { source: "inline", value: decoded || undefined };
    }
    default:
      throw new YoooclawError(
        "YOOOCLAW_CONFIG_INVALID",
        `不支持的凭据引用 scheme：${scheme}`,
        { hint: "支持 env: / file: / keychain: / inline:" },
      );
  }
}

/** 把值写入 ref 指向的后端。env/inline 不可持久写入（抛错）。 */
export function writeRef(ref: string, value: string): ResolvedRef {
  const colonIdx = ref.indexOf(":");
  const scheme = ref.slice(0, colonIdx);
  const rest = ref.slice(colonIdx + 1);

  switch (scheme) {
    case "file": {
      const { path, field } = parseFileRef(rest);
      const data = readJsonFile<Record<string, unknown>>(path) ?? {};
      data[field] = value;
      writeJsonFile(path, data, SECRET_FILE_MODE);
      return { source: "file", value, location: path };
    }
    case "keychain": {
      const { service, account } = parseKeychainRef(rest);
      if (!keychainSet(service, account, value)) {
        throw new YoooclawError(
          "YOOOCLAW_KEYCHAIN_UNAVAILABLE",
          `keychain 写入失败：${service}/${account}`,
          { hint: "当前平台可能没有可用的 keychain 工具，请改用 file: 引用" },
        );
      }
      return { source: "keychain", value, location: `${service}/${account}` };
    }
    case "env":
      throw new YoooclawError(
        "YOOOCLAW_INVALID_ARGUMENT",
        "env: 引用无法持久化写入；请改用 file: 或 keychain:",
      );
    case "inline":
      throw new YoooclawError(
        "YOOOCLAW_INVALID_ARGUMENT",
        "inline: 引用需直接写进 config，不通过 writeRef",
      );
    default:
      throw new YoooclawError(
        "YOOOCLAW_CONFIG_INVALID",
        `不支持的凭据引用 scheme：${scheme}`,
      );
  }
}
