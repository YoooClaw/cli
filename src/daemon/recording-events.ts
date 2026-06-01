/**
 * RecordingEventLog —— 录音状态事件 append-only JSONL 日志。
 *
 * 落 ~/.yoooclaw/profiles/<profile>/recordings/state/events.jsonl，每行一个 JSON：
 *   { ts: ISO8601, recordingId, transfer_status, audioFile?, transcriptFile?, summary?, error? }
 *
 * 设计取舍：
 * - daemon 没有面向客户端的广播通道，handler 内部状态机变化通过 notifyStatus 回调对外暴露；
 *   把它落成 JSONL 是最简单的「可观察」方案，CLI / Agent / tail -f 都能消费。
 * - 不做轮转 / 不做大小上限：一条录音事件几条，文件很小；轮转策略等真有量再说。
 */
import { appendFileSync, existsSync, mkdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import type { RecordingStatusEvent } from "../shared.js";

export interface RecordingEventRecord extends RecordingStatusEvent {
  ts: string;
}

export class RecordingEventLog {
  readonly path: string;

  constructor(recordingsDir: string) {
    this.path = join(recordingsDir, "state", "events.jsonl");
    mkdirSync(dirname(this.path), { recursive: true });
  }

  append(event: RecordingStatusEvent): void {
    const record: RecordingEventRecord = { ts: new Date().toISOString(), ...event };
    appendFileSync(this.path, JSON.stringify(record) + "\n", "utf-8");
  }

  /** 读取全部事件（按追加顺序）。文件不存在返回空数组。 */
  readAll(): RecordingEventRecord[] {
    if (!existsSync(this.path)) return [];
    const raw = readFileSync(this.path, "utf-8");
    const out: RecordingEventRecord[] = [];
    for (const line of raw.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        out.push(JSON.parse(trimmed) as RecordingEventRecord);
      } catch {
        // 忽略损坏行（理论上 append-only 不应该出现）
      }
    }
    return out;
  }
}
