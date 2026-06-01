/**
 * daemon 进程锁（单实例保护）。
 *
 * daemon.lock 内容：{ pid, startedAt, bind, port, logLevel }。
 * 通过 `process.kill(pid, 0)` 探活；陈旧锁（进程已死）会被视为未运行。
 */
import { existsSync, rmSync } from "node:fs";
import type { ProfilePaths } from "../paths.js";
import { readJsonFile, writeJsonFile } from "../fs-utils.js";

export interface DaemonLock {
  pid: number;
  startedAt: string;
  bind: string;
  port: number;
  logLevel?: string;
}

/** 进程是否存活（signal 0 探测）。 */
export function isProcessAlive(pid: number): boolean {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    // EPERM = 存在但无权限（仍算存活）；ESRCH = 不存在
    return (err as NodeJS.ErrnoException).code === "EPERM";
  }
}

export function readLock(paths: ProfilePaths): DaemonLock | undefined {
  if (!existsSync(paths.daemonLock)) return undefined;
  return readJsonFile<DaemonLock>(paths.daemonLock);
}

export interface DaemonRunningState {
  running: boolean;
  lock?: DaemonLock;
  /** 锁存在但进程已死。 */
  stale: boolean;
}

export function daemonState(paths: ProfilePaths): DaemonRunningState {
  const lock = readLock(paths);
  if (!lock) return { running: false, stale: false };
  const alive = isProcessAlive(lock.pid);
  return { running: alive, lock, stale: !alive };
}

export function writeLock(paths: ProfilePaths, lock: DaemonLock): void {
  writeJsonFile(paths.daemonLock, lock);
}

export function removeLock(paths: ProfilePaths): void {
  rmSync(paths.daemonLock, { force: true });
}
