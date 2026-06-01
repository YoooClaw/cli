import type { LightPixelFramePixel, LightSegment } from "../types.js";
import { output, exitError } from "../cli/helpers.js";

export const VALID_MODES = [
  "wave",
  "breath",
  "strobe",
  "steady",
  "color_flow",
  "pixel_frame",
] as const;
export const MAX_SEGMENTS = 12;

export interface ValidationError {
  field: string;
  message: string;
}

export interface ValidationWarning {
  field: string;
  code: string;
  message: string;
}

export type ValidationResult =
  | { valid: true; segments: LightSegment[]; warnings: ValidationWarning[] }
  | { valid: false; errors: ValidationError[] };

export function validateSegments(segments: unknown): ValidationResult {
  if (!Array.isArray(segments)) {
    return { valid: false, errors: [{ field: "segments", message: "必须是数组" }] };
  }
  if (segments.length === 0) {
    return { valid: false, errors: [{ field: "segments", message: "不能为空" }] };
  }
  if (segments.length > MAX_SEGMENTS) {
    return {
      valid: false,
      errors: [{ field: "segments", message: `最多 ${MAX_SEGMENTS} 段` }],
    };
  }

  const errors: ValidationError[] = [];
  const warnings: ValidationWarning[] = [];
  for (let i = 0; i < segments.length; i++) {
    validateSegment(segments[i], `segments[${i}]`, errors, warnings);
  }

  if (errors.length > 0) return { valid: false, errors };
  return { valid: true, segments: segments as LightSegment[], warnings };
}

/**
 * 解析 JSON 字符串并校验 segments，失败时直接 exitError。
 * 适用于 CLI action 中的统一入口。
 */
export function parseAndValidateSegments(json: string): LightSegment[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(json);
  } catch {
    exitError("VALIDATION_FAILED", "segments 必须是合法的 JSON");
  }

  const result = validateSegments(parsed);
  if (!result.valid) {
    output({ ok: false, error: { code: "VALIDATION_FAILED", details: result.errors } });
    process.exit(1);
  }

  return result.segments;
}

function validateSegment(
  seg: unknown,
  prefix: string,
  errors: ValidationError[],
  warnings: ValidationWarning[],
): void {
  if (!isRecord(seg)) {
    errors.push({ field: prefix, message: "必须是对象" });
    return;
  }

  const mode = seg.mode;
  if (!VALID_MODES.includes(mode as (typeof VALID_MODES)[number])) {
    errors.push({
      field: `${prefix}.mode`,
      message: `不支持的模式 '${String(mode)}'，可选：${VALID_MODES.join("/")}`,
    });
  }

  validateNonNegativeNumber(seg.duration_s, `${prefix}.duration_s`, errors, "必须是 ≥0 的数字（0 表示无限时长）");

  switch (mode) {
    case "wave":
      validateForegroundSegment(seg, prefix, errors);
      validateOptionalNonNegativeNumber(seg.interval_ms, `${prefix}.interval_ms`, errors);
      validateOptionalDirection(seg.direction, `${prefix}.direction`, errors);
      validateOptionalWindow(seg.window, `${prefix}.window`, errors);
      validateOptionalBackground(seg.background, `${prefix}.background`, errors);
      break;
    case "color_flow":
      validateForegroundSegment(seg, prefix, errors);
      validateOptionalNonNegativeNumber(seg.interval_ms, `${prefix}.interval_ms`, errors);
      validateOptionalDirection(seg.direction, `${prefix}.direction`, errors);
      validateOptionalWindow(seg.window, `${prefix}.window`, errors);
      validateOptionalBackground(seg.background, `${prefix}.background`, errors);
      type RgbLike = Parameters<typeof hasNonZeroRgb>[0];
      if (!hasNonZeroRgb(seg.color as RgbLike) && !hasNonZeroRgb(seg.background as RgbLike)) {
        errors.push({
          field: prefix,
          message: "color_flow 至少需要一组非零颜色锚点（color 或 background）",
        });
      }
      detectColorFlowSingleAnchorMisuse(seg, prefix, warnings);
      break;
    case "breath":
      validateForegroundSegment(seg, prefix, errors);
      validateOptionalBreathTiming(seg.breath_timing, `${prefix}.breath_timing`, errors);
      break;
    case "strobe":
      validateForegroundSegment(seg, prefix, errors);
      validateOptionalNonNegativeNumber(seg.interval_ms, `${prefix}.interval_ms`, errors);
      break;
    case "steady":
      validateForegroundSegment(seg, prefix, errors);
      break;
    case "pixel_frame":
      validatePixelFrame(seg.pixels, `${prefix}.pixels`, errors);
      break;
    default:
      validateOptionalNonNegativeNumber(seg.brightness, `${prefix}.brightness`, errors);
      validateOptionalColor(seg.color, `${prefix}.color`, errors);
      validateOptionalNonNegativeNumber(seg.interval_ms, `${prefix}.interval_ms`, errors);
      validateOptionalDirection(seg.direction, `${prefix}.direction`, errors);
      validateOptionalWindow(seg.window, `${prefix}.window`, errors);
      validateOptionalBreathTiming(seg.breath_timing, `${prefix}.breath_timing`, errors);
      validateOptionalBackground(seg.background, `${prefix}.background`, errors);
  }
}

function validateForegroundSegment(
  seg: Record<string, unknown>,
  prefix: string,
  errors: ValidationError[],
): void {
  validateNumberInRange(
    seg.brightness,
    `${prefix}.brightness`,
    errors,
    0,
    255,
    "必须是 0–255 的数字",
  );
  validateColor(seg.color, `${prefix}.color`, errors);

  if (seg.mode !== "steady" && seg.brightness === 0) {
    errors.push({
      field: `${prefix}.brightness`,
      message: "brightness=0 仅 steady 模式允许；其它模式会在固件侧被过滤",
    });
  }
}

function validatePixelFrame(
  value: unknown,
  field: string,
  errors: ValidationError[],
): void {
  if (!Array.isArray(value)) {
    errors.push({ field, message: "pixel_frame 必须提供 pixels 数组（1–7 项）" });
    return;
  }

  if (value.length < 1 || value.length > 7) {
    errors.push({ field, message: "pixels 必须为 1–7 项" });
  }

  const seen = new Set<number>();
  for (let i = 0; i < value.length; i++) {
    const pixel = value[i];
    const prefix = `${field}[${i}]`;
    if (!isRecord(pixel)) {
      errors.push({ field: prefix, message: "必须是对象" });
      continue;
    }

    const idx = pixel.index;
    if (!Number.isInteger(idx) || (idx as number) < 0 || (idx as number) > 6) {
      errors.push({ field: `${prefix}.index`, message: "index 必须是 0–6 的整数" });
    } else if (seen.has(idx as number)) {
      errors.push({ field: `${prefix}.index`, message: `index=${idx} 重复` });
    } else {
      seen.add(idx as number);
    }

    validateNumberInRange(
      pixel.brightness,
      `${prefix}.brightness`,
      errors,
      0,
      255,
      "必须是 0–255 的数字",
    );
    validateColor(pixel.color, `${prefix}.color`, errors);
  }
}

function validateOptionalBreathTiming(
  value: unknown,
  field: string,
  errors: ValidationError[],
): void {
  if (value === undefined) return;
  if (!isRecord(value)) {
    errors.push({ field, message: "必须是对象" });
    return;
  }

  validatePositiveNumber(
    value.rise_ms,
    `${field}.rise_ms`,
    errors,
    "rise_ms 必须是 >0 的数字（不支持 0ms）",
  );
  validateNonNegativeNumber(
    value.hold_ms,
    `${field}.hold_ms`,
    errors,
    "hold_ms 必须是 ≥0 的数字",
  );
  validatePositiveNumber(
    value.fall_ms,
    `${field}.fall_ms`,
    errors,
    "fall_ms 必须是 >0 的数字（不支持 0ms）",
  );
  validateNonNegativeNumber(
    value.off_ms,
    `${field}.off_ms`,
    errors,
    "off_ms 必须是 ≥0 的数字",
  );
}

function validateOptionalBackground(
  value: unknown,
  field: string,
  errors: ValidationError[],
): void {
  if (value === undefined) return;
  if (!isRecord(value)) {
    errors.push({ field, message: "必须包含 r/g/b/brightness 数值" });
    return;
  }

  validateColor(value, field, errors);
  validateNumberInRange(
    value.brightness,
    `${field}.brightness`,
    errors,
    0,
    255,
    "必须是 0–255 的数字",
  );
}

function validateOptionalColor(
  value: unknown,
  field: string,
  errors: ValidationError[],
): void {
  if (value === undefined) return;
  validateColor(value, field, errors);
}

function validateColor(
  value: unknown,
  field: string,
  errors: ValidationError[],
): void {
  if (!isRecord(value)) {
    errors.push({ field, message: "必须包含 r/g/b 数值" });
    return;
  }

  validateNumberInRange(value.r, `${field}.r`, errors, 0, 255, "必须是 0–255 的数字");
  validateNumberInRange(value.g, `${field}.g`, errors, 0, 255, "必须是 0–255 的数字");
  validateNumberInRange(value.b, `${field}.b`, errors, 0, 255, "必须是 0–255 的数字");
}

function validateOptionalDirection(
  value: unknown,
  field: string,
  errors: ValidationError[],
): void {
  if (value === undefined) return;
  if (value !== "ltr" && value !== "rtl") {
    errors.push({ field, message: "direction 必须是 ltr 或 rtl" });
  }
}

function validateOptionalWindow(
  value: unknown,
  field: string,
  errors: ValidationError[],
): void {
  if (value === undefined) return;
  if (value !== 1 && value !== 2 && value !== 3) {
    errors.push({ field, message: "window 仅支持 1/2/3" });
  }
}

function validateOptionalNonNegativeNumber(
  value: unknown,
  field: string,
  errors: ValidationError[],
): void {
  if (value === undefined) return;
  validateNonNegativeNumber(value, field, errors, "必须是 ≥0 的数字");
}

function validatePositiveNumber(
  value: unknown,
  field: string,
  errors: ValidationError[],
  message: string,
): void {
  if (value === undefined) return;
  if (!isFiniteNumber(value) || value <= 0) {
    errors.push({ field, message });
  }
}

function validateNonNegativeNumber(
  value: unknown,
  field: string,
  errors: ValidationError[],
  message: string,
): void {
  if (!isFiniteNumber(value) || value < 0) {
    errors.push({ field, message });
  }
}

function validateNumberInRange(
  value: unknown,
  field: string,
  errors: ValidationError[],
  min: number,
  max: number,
  message: string,
): void {
  if (!isFiniteNumber(value) || value < min || value > max) {
    errors.push({ field, message });
  }
}

function hasNonZeroRgb(
  value: Pick<LightPixelFramePixel["color"], "r" | "g" | "b"> | { r?: unknown; g?: unknown; b?: unknown } | undefined,
): boolean {
  if (!value) return false;
  return [value.r, value.g, value.b].some((channel) => isFiniteNumber(channel) && channel > 0);
}

/**
 * color_flow 是「调色板沿圆周流动」，由 1~2 个颜色锚点插值得到。
 * 当用户/模型只给单一极端纯色锚点（如纯红 R=255、G=B=0）且没有底色锚点时，
 * 实际效果是「同色系亮暗环状流动」（红→暗红），而不是多色调色板流动。
 * 这是已知的 bug 模式（详见 imp-custom-led-ai-read.md「模式选择易错对照」）：
 * 上层往往是把用户的"单色波浪"误映射到 color_flow，应该改用 wave 模式。
 * 此处不阻塞，只 push 一条 warning 让前端/审计可见。
 */
function detectColorFlowSingleAnchorMisuse(
  seg: Record<string, unknown>,
  prefix: string,
  warnings: ValidationWarning[],
): void {
  const color = seg.color as { r?: unknown; g?: unknown; b?: unknown } | undefined;
  const background = seg.background as
    | { r?: unknown; g?: unknown; b?: unknown; brightness?: unknown }
    | undefined;

  const fgChannels = extractChannels(color);
  const bgChannels = extractChannels(background);
  if (!fgChannels) return;

  const bgBrightnessRaw = background?.brightness;
  const bgBrightness = isFiniteNumber(bgBrightnessRaw) ? bgBrightnessRaw : 0;
  const bgActive = !!bgChannels && bgChannels.some((c) => c > 0) && bgBrightness > 0;
  if (bgActive) return;

  const fgActiveChannels = fgChannels.filter((c) => c > 0);
  if (fgActiveChannels.length !== 1) return;
  if (fgActiveChannels[0] < 192) return;

  warnings.push({
    field: prefix,
    code: "COLOR_FLOW_SINGLE_ANCHOR_MISUSE",
    message:
      "color_flow 仅设置了单一极端纯色前景锚点（无有效底色锚点），实际效果是同色系亮暗环状流动，不是多色调色板流动。" +
      "若用户期望的是\"单色波浪\"，请改用 mode='wave'；若期望多色流动，请同时设置 background 作为第二锚点。",
  });
}

function extractChannels(
  value: { r?: unknown; g?: unknown; b?: unknown } | undefined,
): [number, number, number] | null {
  if (!value) return null;
  const r = isFiniteNumber(value.r) ? value.r : 0;
  const g = isFiniteNumber(value.g) ? value.g : 0;
  const b = isFiniteNumber(value.b) ? value.b : 0;
  return [r, g, b];
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
