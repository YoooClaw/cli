/**
 * 监控任务定义存储（state/monitors.json）。
 *
 * 本 build 负责定义的 CRUD 与启用状态；cron 实时触发为后续迭代（daemon 当前只持久化定义）。
 */
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import type { ProfilePaths } from "../paths.js";
import { ensureDir, writeJsonFile } from "../fs-utils.js";

export interface MonitorTask {
  name: string;
  description: string;
  matchRules: unknown;
  schedule: string;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
  lastRunAt: string | null;
  lastResult: unknown;
}

export interface CreateMonitorInput {
  name: string;
  description: string;
  matchRules: unknown;
  schedule: string;
}

function storePath(paths: ProfilePaths): string {
  return join(paths.state, "monitors.json");
}

export function listMonitors(paths: ProfilePaths): MonitorTask[] {
  const p = storePath(paths);
  if (!existsSync(p)) return [];
  try {
    const raw = JSON.parse(readFileSync(p, "utf-8"));
    return Array.isArray(raw?.monitors) ? raw.monitors : [];
  } catch {
    return [];
  }
}

function save(paths: ProfilePaths, monitors: MonitorTask[]): void {
  ensureDir(paths.state);
  writeJsonFile(storePath(paths), { monitors });
}

export function createMonitor(paths: ProfilePaths, input: CreateMonitorInput): MonitorTask {
  if (!input.name || !input.description || !input.schedule || input.matchRules === undefined) {
    throw new Error("name / description / matchRules / schedule 均必填");
  }
  const monitors = listMonitors(paths);
  if (monitors.some((m) => m.name === input.name)) {
    throw new Error(`监控任务已存在：${input.name}`);
  }
  const now = new Date().toISOString();
  const task: MonitorTask = {
    name: input.name,
    description: input.description,
    matchRules: input.matchRules,
    schedule: input.schedule,
    enabled: true,
    createdAt: now,
    updatedAt: now,
    lastRunAt: null,
    lastResult: null,
  };
  monitors.push(task);
  save(paths, monitors);
  return task;
}

export function deleteMonitor(paths: ProfilePaths, name: string): boolean {
  const monitors = listMonitors(paths);
  const next = monitors.filter((m) => m.name !== name);
  if (next.length === monitors.length) return false;
  save(paths, next);
  return true;
}

export function setMonitorEnabled(paths: ProfilePaths, name: string, enabled: boolean): boolean {
  const monitors = listMonitors(paths);
  const task = monitors.find((m) => m.name === name);
  if (!task) return false;
  task.enabled = enabled;
  task.updatedAt = new Date().toISOString();
  save(paths, monitors);
  return true;
}

export function getMonitor(paths: ProfilePaths, name: string): MonitorTask | undefined {
  return listMonitors(paths).find((m) => m.name === name);
}
