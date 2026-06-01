/**
 * OS keychain 适配（可选加固，默认不强制）。
 *
 * - macOS：`security`（generic password）
 * - Linux：`secret-tool`（libsecret / Secret Service）
 * - Windows：暂无可明文取回的内置 CLI → 视为不可用，调用方降级到文件
 *
 * 任何工具缺失 / 调用失败都返回 `unavailable`，绝不抛崩溃，保证 CI 无 keychain 时的确定性。
 */
import { spawnSync } from "node:child_process";
import { platform } from "node:os";

export interface KeychainResult {
  available: boolean;
  value?: string;
}

function hasCommand(cmd: string): boolean {
  const probe = spawnSync(process.platform === "win32" ? "where" : "which", [cmd], {
    encoding: "utf-8",
  });
  return probe.status === 0;
}

/** keychain 在当前平台是否可用（工具存在）。 */
export function keychainAvailable(): boolean {
  switch (platform()) {
    case "darwin":
      return hasCommand("security");
    case "linux":
      return hasCommand("secret-tool");
    default:
      return false;
  }
}

/** 读取 keychain 条目；不可用或未命中返回 { available, value: undefined }。 */
export function keychainGet(service: string, account: string): KeychainResult {
  switch (platform()) {
    case "darwin": {
      if (!hasCommand("security")) return { available: false };
      const r = spawnSync(
        "security",
        ["find-generic-password", "-s", service, "-a", account, "-w"],
        { encoding: "utf-8" },
      );
      if (r.status !== 0) return { available: true, value: undefined };
      return { available: true, value: r.stdout.replace(/\n$/, "") };
    }
    case "linux": {
      if (!hasCommand("secret-tool")) return { available: false };
      const r = spawnSync(
        "secret-tool",
        ["lookup", "service", service, "account", account],
        { encoding: "utf-8" },
      );
      if (r.status !== 0) return { available: true, value: undefined };
      return { available: true, value: r.stdout.replace(/\n$/, "") };
    }
    default:
      return { available: false };
  }
}

/** 写入 keychain 条目；返回是否成功。 */
export function keychainSet(
  service: string,
  account: string,
  value: string,
): boolean {
  switch (platform()) {
    case "darwin": {
      if (!hasCommand("security")) return false;
      const r = spawnSync(
        "security",
        ["add-generic-password", "-U", "-s", service, "-a", account, "-w", value],
        { encoding: "utf-8" },
      );
      return r.status === 0;
    }
    case "linux": {
      if (!hasCommand("secret-tool")) return false;
      const r = spawnSync(
        "secret-tool",
        ["store", "--label", `${service}:${account}`, "service", service, "account", account],
        { encoding: "utf-8", input: value },
      );
      return r.status === 0;
    }
    default:
      return false;
  }
}
