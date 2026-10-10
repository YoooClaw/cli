#ifndef NANOSHELL_WASM_LIMITS_H
#define NANOSHELL_WASM_LIMITS_H

/*
 * Device Wasm3 budget (AiNote / no-PSRAM) — single source of truth.
 *
 * Keep in sync with:
 *   sdk-6095/.../nanoshell/CMakeLists.txt  (d_m3FixedHeap / d_m3LinearMemoryLimit)
 *   tools/pack_nsp.py                     (DEVICE_WASM_MAX, -z stack-size)
 *   tools/host_tool.py                    (DEVICE_WASM_MAX)
 *   host/runtime/wasm_runner.c            (operand stack; file buf)
 *   third_party/wasm3/.../m3_env.c        (s_m3LinearStore side buffer)
 *
 * Layout:
 *   FixedHeap 16 KiB BSS  — env + parse + runtime + operand + code pages
 *   Linear 8 KiB static   — separate from FixedHeap (see m3_env.c patch)
 *   File load ≤12 KiB+16  — Ainote: BT1 heap (not main-SRAM BSS)
 *   Guest C-stack         — inside linear (-z stack-size); must fit with .data
 *
 * Do not raise FixedHeap to 24 KiB — overflows product SRAM.
 * Do not put NS_WASM_FILE_BUF back in main .bss — overflows hp4570n noinit.
 * Upstream m3_SetResourceLimit only caps; under FixedHeap linear still needs
 * the NanoShell side-buffer patch (GuardedMemory is incompatible with FixedHeap).
 */

#ifndef NS_WASM_MAX_BYTES
#define NS_WASM_MAX_BYTES 12288U
#endif

#ifndef NS_WASM_FIXED_HEAP_BYTES
#define NS_WASM_FIXED_HEAP_BYTES 16384U
#endif

#ifndef NS_WASM_LINEAR_LIMIT_BYTES
#define NS_WASM_LINEAR_LIMIT_BYTES 8192U
#endif

#ifndef NS_WASM_OPERAND_STACK_BYTES
#define NS_WASM_OPERAND_STACK_BYTES 2048U
#endif

#ifndef NS_WASM_GUEST_STACK_BYTES
#define NS_WASM_GUEST_STACK_BYTES 3072U
#endif

/* NSP1 header (16) + payload ≤ NS_WASM_MAX_BYTES */
#ifndef NS_WASM_FILE_BUF_BYTES
#define NS_WASM_FILE_BUF_BYTES (NS_WASM_MAX_BYTES + 16U)
#endif

/* Session quotas (policy ceiling; NSP1 may request less). */
#ifndef NS_WASM_MAX_TIMERS
#define NS_WASM_MAX_TIMERS 6U
#endif

#ifndef NS_WASM_DEFAULT_WATCHDOG_MS
#define NS_WASM_DEFAULT_WATCHDOG_MS 3000U
#endif

#endif /* NANOSHELL_WASM_LIMITS_H */
