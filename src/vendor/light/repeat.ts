export interface RepeatConfig {
  repeat?: boolean;
  repeat_times?: number;
}

export type RepeatArgument = RepeatConfig | boolean | number | undefined;

export function normalizeRepeatTimes(input?: RepeatArgument): number {
  if (typeof input === "boolean") {
    return input ? 0 : 1;
  }

  if (typeof input === "number") {
    return validateRepeatTimes(input);
  }

  if (!input) {
    return 1;
  }

  if (input.repeat_times !== undefined) {
    return validateRepeatTimes(input.repeat_times);
  }

  if (input.repeat !== undefined) {
    return input.repeat ? 0 : 1;
  }

  return 1;
}

export function assertAncsRepeatTimes(repeatTimes: number): void {
  if (repeatTimes !== 0 && repeatTimes !== 1) {
    throw new Error(
      "当前 ANCS 路径仅支持 repeat_times=0（无限循环）或 1（播放一轮）；N>=2 需非 ANCS 路径",
    );
  }
}

function validateRepeatTimes(value: number): number {
  if (!Number.isInteger(value) || value < 0) {
    throw new Error("repeat_times 必须是 >=0 的整数");
  }

  return value;
}
