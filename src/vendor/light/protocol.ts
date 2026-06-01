import type { LightSegment } from "../types.js";
import { assertAncsRepeatTimes, normalizeRepeatTimes, type RepeatArgument } from "./repeat.js";
import { validateSegments } from "./validators.js";

export const MAX_LIGHT_SEGMENTS = 12;

const PROTOCOL_DIGITS = [
  "\u0080",
  "\u0081",
  "\u0082",
  "\u0083",
  "\u0084",
  "\u0091",
  "\u0092",
  "\u0093",
  "\u0094",
  "\u0095",
  "\u0096",
  "\u0097",
] as const;

/** \u009A = play once, \u009B = infinite loop */
const LED_SEPARATOR_ONCE = "\u009A";
const LED_SEPARATOR_LOOP = "\u009B";

/** v0–v10 mapped by index; duration_s=0 is special-cased to v11 (infinite) */
const DURATION_STEPS_S = [0.5, 1, 2, 3, 5, 6, 8, 16, 24, 32, 48] as const;
/** v0–v10 mapped by index; removed 400ms, added 50ms */
const INTERVAL_STEPS_MS = [50, 100, 200, 300, 500, 600, 800, 1600, 2400, 3200, 4800] as const;
const BREATH_STEPS_MS = [1040, 1560, 2080, 2600, 3100, 4160] as const;
const BRIGHTNESS_STEPS = [32, 64, 96, 128, 192, 255] as const;
const COLOR_STEPS = [0, 32, 64, 128, 192, 255] as const;
const BACKGROUND_BRIGHTNESS_STEPS = [0, 32, 64, 96, 128, 192, 255] as const;
const MULTI_CHANNEL_COLOR_COEFFICIENTS = { r: 1, g: 0.25, b: 0.25 } as const;
const PURE_WHITE_COLOR_COEFFICIENTS = { r: 1, g: 0.35, b: 0.35 } as const;

/**
 * mode → wire-protocol M value.
 * color_flow encodes to M=v4 (firmware enum APP_LED_CUSTOM_MODE_WAVE_RAINBOW).
 * "WAVE_RAINBOW" is a legacy firmware name and does NOT imply rainbow/multi-color semantics;
 * the TS-side enum uses color_flow to avoid that confusion. See imp-custom-led-ai-read.md
 * "模式选择易错对照" for the mapping rules.
 */
const MODE_TO_INDEX: Record<LightSegment["mode"], number> = {
  wave: 0,
  breath: 1,
  strobe: 2,
  steady: 3,
  color_flow: 4,
  pixel_frame: 5,
};

export function buildLightEffectApnsBody(
  segments: LightSegment[],
  repeatInput?: RepeatArgument,
  visibleTextOverride?: string,
): string {
  assertSegmentCount(segments);
  assertSegmentsValid(segments);

  const repeatTimes = normalizeRepeatTimes(repeatInput);
  assertAncsRepeatTimes(repeatTimes);

  const visibleText = resolveVisibleText(visibleTextOverride, segments);
  const separator = repeatTimes === 0 ? LED_SEPARATOR_LOOP : LED_SEPARATOR_ONCE;
  const payload = segments.map((segment) => encodeSegment(segment)).join("");
  return `${visibleText}${separator}${payload}`;
}

function resolveVisibleText(
  override: string | undefined,
  segments: LightSegment[],
): string {
  const trimmed = override?.trim();
  if (trimmed) return trimmed;
  return summarizeSegments(segments);
}

function assertSegmentCount(segments: LightSegment[]): void {
  if (segments.length < 1 || segments.length > MAX_LIGHT_SEGMENTS) {
    throw new Error(`light_control supports 1-${MAX_LIGHT_SEGMENTS} segments`);
  }
}

function summarizeSegments(segments: LightSegment[]): string {
  const modeDesc = segments.map((segment) => segment.mode).join("+");
  return `Effect: ${modeDesc} (${segments.length} segment${segments.length > 1 ? "s" : ""})`;
}

function assertSegmentsValid(segments: LightSegment[]): void {
  const validation = validateSegments(segments);
  if (!validation.valid) {
    throw new Error(
      validation.errors.map((error) => `${error.field}: ${error.message}`).join("; "),
    );
  }
}

function encodeSegment(segment: LightSegment): string {
  const common = [
    MODE_TO_INDEX[segment.mode],
    quantizeDuration(segment.duration_s),
  ];

  let values: number[];
  switch (segment.mode) {
    case "wave":
    case "color_flow":
      const color = normalizeProtocolColor(segment.color);
      const background = normalizeProtocolColor(segment.background);
      values = [
        ...common,
        quantize(segment.interval_ms ?? 200, INTERVAL_STEPS_MS),
        quantizeBrightnessValue(segment.brightness ?? 0),
        quantize(color.r, COLOR_STEPS),
        quantize(color.g, COLOR_STEPS),
        quantize(color.b, COLOR_STEPS),
        segment.direction === "rtl" ? 1 : 0,
        quantizeWindow(segment.window ?? 2),
        quantize(background.r, COLOR_STEPS),
        quantize(background.g, COLOR_STEPS),
        quantize(background.b, COLOR_STEPS),
        quantize(segment.background?.brightness ?? 0, BACKGROUND_BRIGHTNESS_STEPS),
      ];
      break;
    case "breath":
      const breathColor = normalizeProtocolColor(segment.color);
      values = [
        ...common,
        quantizeBreathRiseFall(segment.breath_timing?.rise_ms),
        quantizeBreathHoldOff(segment.breath_timing?.hold_ms),
        quantizeBreathRiseFall(segment.breath_timing?.fall_ms),
        quantizeBreathHoldOff(segment.breath_timing?.off_ms),
        quantizeBrightnessValue(segment.brightness ?? 0),
        quantize(breathColor.r, COLOR_STEPS),
        quantize(breathColor.g, COLOR_STEPS),
        quantize(breathColor.b, COLOR_STEPS),
      ];
      break;
    case "strobe":
      const strobeColor = normalizeProtocolColor(segment.color);
      values = [
        ...common,
        quantize(segment.interval_ms ?? 200, INTERVAL_STEPS_MS),
        quantizeBrightnessValue(segment.brightness ?? 0),
        quantize(strobeColor.r, COLOR_STEPS),
        quantize(strobeColor.g, COLOR_STEPS),
        quantize(strobeColor.b, COLOR_STEPS),
      ];
      break;
    case "steady":
      const steadyColor = normalizeProtocolColor(segment.color);
      values = [
        ...common,
        quantizeBrightnessValue(segment.brightness ?? 0),
        quantize(steadyColor.r, COLOR_STEPS),
        quantize(steadyColor.g, COLOR_STEPS),
        quantize(steadyColor.b, COLOR_STEPS),
      ];
      break;
    case "pixel_frame":
      values = encodePixelFrameValues(common, segment);
      break;
  }

  return values.map((value) => PROTOCOL_DIGITS[value]).join("");
}

function encodePixelFrameValues(common: number[], segment: LightSegment): number[] {
  const pixels = segment.pixels ?? [];
  return [
    ...common,
    pixels.length - 1,
    ...pixels.flatMap((pixel) => {
      const color = normalizeProtocolColor(pixel.color);
      return [
        pixel.index,
        quantize(color.r, COLOR_STEPS),
        quantize(color.g, COLOR_STEPS),
        quantize(color.b, COLOR_STEPS),
        quantizeBrightnessValue(pixel.brightness),
      ];
    }),
  ];
}

export function normalizeProtocolColor(
  color:
    | Pick<NonNullable<LightSegment["color"]>, "r" | "g" | "b">
    | Pick<NonNullable<LightSegment["background"]>, "r" | "g" | "b">
    | undefined,
): { r: number; g: number; b: number } {
  const normalized = {
    r: color?.r ?? 0,
    g: color?.g ?? 0,
    b: color?.b ?? 0,
  };

  if (isPureWhiteProtocolColor(normalized)) {
    return applyProtocolColorCoefficients(normalized, PURE_WHITE_COLOR_COEFFICIENTS);
  }

  if (countActiveProtocolColorChannels(normalized) <= 1) {
    return normalized;
  }

  return applyProtocolColorCoefficients(normalized, MULTI_CHANNEL_COLOR_COEFFICIENTS);
}

function countActiveProtocolColorChannels(color: { r: number; g: number; b: number }): number {
  return Number(color.r > 0) + Number(color.g > 0) + Number(color.b > 0);
}

function isPureWhiteProtocolColor(color: { r: number; g: number; b: number }): boolean {
  return color.r === 255 && color.g === 255 && color.b === 255;
}

function applyProtocolColorCoefficients(
  color: { r: number; g: number; b: number },
  coefficients: { r: number; g: number; b: number },
): { r: number; g: number; b: number } {
  return {
    r: scaleProtocolColorChannel(color.r, coefficients.r),
    g: scaleProtocolColorChannel(color.g, coefficients.g),
    b: scaleProtocolColorChannel(color.b, coefficients.b),
  };
}

function scaleProtocolColorChannel(value: number, coefficient: number): number {
  return Math.max(0, Math.min(255, Math.round(value * coefficient)));
}

function quantize(value: number, steps: readonly number[]): number {
  let bestIndex = 0;
  let bestDistance = Number.POSITIVE_INFINITY;

  for (const [index, step] of steps.entries()) {
    const distance = Math.abs(value - step);
    if (distance < bestDistance) {
      bestIndex = index;
      bestDistance = distance;
    }
  }

  return bestIndex;
}

/** duration_s=0 maps to v11 (infinite); otherwise quantize to DURATION_STEPS_S */
function quantizeDuration(duration_s: number): number {
  if (duration_s === 0) return 11;
  return quantize(duration_s, DURATION_STEPS_S);
}

/**
 * brightness=0 maps to v11 (explicit off / pixel-off).
 * Otherwise quantize to BRIGHTNESS_STEPS (v0–v5).
 */
function quantizeBrightnessValue(brightness: number): number {
  if (brightness === 0) return 11;
  return quantize(brightness, BRIGHTNESS_STEPS);
}

function quantizeBreathRiseFall(value: number | undefined): number {
  return quantize(value ?? 1040, BREATH_STEPS_MS);
}

function quantizeBreathHoldOff(value: number | undefined): number {
  if (value === 0) {
    return 5;
  }

  return quantize(value ?? 1040, BREATH_STEPS_MS.slice(0, 5));
}

function quantizeWindow(value: 1 | 2 | 3): number {
  return value - 1;
}
