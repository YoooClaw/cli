#!/usr/bin/env python3
"""Pack a NanoShell .nsp (manifest.json + app.wasm) for SD install.

Usage:
  python tools/pack_nsp.py hello_tiny
  python tools/pack_nsp.py hello_tiny --id com.example.hello --name "Hello Tiny"

Looks for guest/wasm/<name>.c, compiles with clang --target=wasm32 from
PATH or .tools/wasi-sdk|.tools/clang-wasm, writes dist/<name>.nsp/
Device limit: app.wasm must be <= 12288 bytes (NS_WASM_MAX_BYTES in
host/include/nanoshell/wasm_limits.h). Guest -z stack-size = NS_WASM_GUEST_STACK_BYTES.
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

# Keep in sync with host/include/nanoshell/wasm_limits.h
DEVICE_WASM_MAX = 12288  # NS_WASM_MAX_BYTES
GUEST_STACK_SIZE = 3072  # NS_WASM_GUEST_STACK_BYTES / -z stack-size
LINEAR_LIMIT = 8192  # NS_WASM_LINEAR_LIMIT_BYTES


def repo_root() -> Path:
    return Path(__file__).resolve().parents[1]


def _leb128(buf: bytes, i: int) -> tuple[int, int]:
    """Unsigned LEB128 → (value, next_index)."""
    result = 0
    shift = 0
    while True:
        if i >= len(buf):
            raise ValueError("truncated LEB128")
        b = buf[i]
        i += 1
        result |= (b & 0x7F) << shift
        if (b & 0x80) == 0:
            return result, i
        shift += 7
        if shift > 35:
            raise ValueError("LEB128 too long")


def _i32_const_offset(expr: bytes) -> int | None:
    """Parse Wasm init expr: i32.const <leb> end (0x0b). Return unsigned addr."""
    if not expr or expr[-1] != 0x0B:
        return None
    if expr[0] != 0x41:  # i32.const
        return None
    val = 0
    shift = 0
    i = 1
    while i < len(expr) - 1:
        b = expr[i]
        i += 1
        val |= (b & 0x7F) << shift
        shift += 7
        if (b & 0x80) == 0:
            # Sign bit is on the *last* LEB byte (bit 6), not the first.
            if (b & 0x40) and shift < 32:
                val |= (~0) << shift
            return val & 0xFFFFFFFF
    return None


def wasm_linear_footprint(raw: bytes) -> tuple[int, int]:
    """Return (max_data_end, data_segment_count) from the Data section.

    With --stack-first, guest stack sits at [0, GUEST_STACK_SIZE); .data/.rodata
    follow. Host linear must cover max(data_end, GUEST_STACK_SIZE).
    """
    if len(raw) < 8 or raw[0:4] != b"\0asm":
        raise ValueError("not a Wasm binary")
    i = 8
    max_end = 0
    nseg = 0
    while i < len(raw):
        if i + 1 > len(raw):
            break
        sec_id = raw[i]
        i += 1
        sec_len, i = _leb128(raw, i)
        sec_end = i + sec_len
        if sec_end > len(raw):
            raise ValueError("truncated section")
        if sec_id == 11:  # Data
            count, j = _leb128(raw, i)
            for _ in range(count):
                flags, j = _leb128(raw, j)
                if flags & 0x2:  # explicit mem index
                    _, j = _leb128(raw, j)
                # active: offset expr until 0x0b
                if (flags & 0x1) == 0:
                    expr_start = j
                    while j < sec_end and raw[j] != 0x0B:
                        j += 1
                    if j >= sec_end:
                        raise ValueError("bad data offset expr")
                    j += 1
                    off = _i32_const_offset(raw[expr_start:j])
                    size, j = _leb128(raw, j)
                    if off is not None:
                        end = off + size
                        if end > max_end:
                            max_end = end
                    j += size
                    nseg += 1
                else:  # passive — skip bytes
                    size, j = _leb128(raw, j)
                    j += size
                    nseg += 1
            if j > sec_end:
                raise ValueError("data section overflow")
        i = sec_end
    return max_end, nseg


def check_device_budget(wasm_path: Path, raw: bytes | None = None) -> None:
    """Fail like a build error if the module won't fit the device Host budget."""
    if raw is None:
        raw = wasm_path.read_bytes()
    # Strip NSP1 if re-checking a packaged file
    body = raw[16:] if raw[:4] == b"NSP1" else raw
    size = len(body)
    if size > DEVICE_WASM_MAX:
        raise SystemExit(
            f"ERROR: app.wasm is {size} bytes > device max {DEVICE_WASM_MAX} "
            f"(NS_WASM_MAX_BYTES). Shrink code / avoid libc — same as APK size gate."
        )
    try:
        data_end, nseg = wasm_linear_footprint(body)
    except ValueError as e:
        print(f"[pack-nsp] WARN: could not parse wasm layout ({e})", file=sys.stderr)
        return
    need = max(data_end, GUEST_STACK_SIZE)
    print(
        f"[pack-nsp] linear footprint: data_end={data_end} "
        f"stack={GUEST_STACK_SIZE} need={need} / limit={LINEAR_LIMIT} "
        f"(data segs={nseg})"
    )
    if need > LINEAR_LIMIT:
        raise SystemExit(
            f"ERROR: guest linear need {need} bytes > host limit {LINEAR_LIMIT} "
            f"(NS_WASM_LINEAR_LIMIT_BYTES). Reduce globals / stack "
            f"(stack-size={GUEST_STACK_SIZE}) — would fail on device as "
            f"'data segment out of bounds'."
        )


def clang_supports_wasm32(clang: str) -> bool:
    try:
        r = subprocess.run(
            [clang, "--target=wasm32", "-c", "-x", "c", "-", "-o", os.devnull],
            input=b"void start(void){}\n",
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=30,
            check=False,
        )
        return r.returncode == 0
    except (OSError, subprocess.TimeoutExpired):
        return False


def find_clang(root: Path) -> str | None:
    env = os.environ.get("NS_CLANG")
    ordered: list[str] = []
    if env:
        ordered.append(env)
    for rel in (
        ("wasi-sdk", "bin", "clang.exe"),
        ("wasi-sdk", "bin", "clang"),
        ("clang-wasm", "bin", "clang.exe"),
        ("clang-wasm", "bin", "clang"),
        ("llvm", "bin", "clang.exe"),
        ("llvm", "bin", "clang"),
    ):
        ordered.append(str(root / ".tools" / Path(*rel)))
    for name in ("clang", "clang.exe"):
        found = shutil.which(name)
        if found:
            ordered.append(found)
    seen: set[str] = set()
    for c in ordered:
        if not c or c in seen:
            continue
        seen.add(c)
        if not Path(c).exists():
            continue
        if clang_supports_wasm32(c):
            return c
    return None


def compile_wasm(clang: str, src: Path, out_wasm: Path, wasm_dir: Path) -> None:
    minic = wasm_dir / "minic.c"
    sdk = wasm_dir.parent / "sdk"
    cmd = [
        clang,
        "--target=wasm32",
        "-nostdlib",
        "-Os",
        "-fno-builtin",
        "-Wl,--no-entry",
        "-Wl,--export=start",
        "-Wl,--export-memory",
        "-Wl,--allow-undefined",
        # Guest C-stack at low addresses so .data fits Host linear (8KiB).
        # Without --stack-first, some toolchains park data at high offsets →
        # LOAD "data segment out of bounds" even when app.wasm is only ~3KiB.
        f"-Wl,-z,stack-size={GUEST_STACK_SIZE}",
        "-Wl,--stack-first",
        f"-I{sdk}",
        f"-I{wasm_dir}",
        str(src),
        str(minic),
        "-o",
        str(out_wasm),
    ]
    print("[pack-nsp]", " ".join(cmd))
    subprocess.check_call(cmd)


def main() -> int:
    ap = argparse.ArgumentParser(description="Build dist/<name>.nsp for device SD")
    ap.add_argument("name", help="guest/wasm/<name>.c stem, e.g. hello_tiny")
    ap.add_argument("--id", default=None, help="manifest id")
    ap.add_argument("--name-title", default=None, dest="title", help="display name")
    ap.add_argument("--version", type=int, default=1)
    ap.add_argument("--src", default=None, help="override source .c path")
    ap.add_argument(
        "--nsp1",
        action="store_true",
        help="prepend 16-byte NSP1 header to app.wasm (timers/flags metadata)",
    )
    ap.add_argument("--timers", type=int, default=6, help="NSP1 max_timers (1..6)")
    ap.add_argument("--need-ui", action="store_true", help="NSP1 require NS_CAP_UI")
    ap.add_argument("--need-gfx", action="store_true", help="NSP1 require NS_CAP_GFX")
    ap.add_argument("--min-w", type=int, default=0, help="NSP1 min width (0=any, ≤255)")
    ap.add_argument("--min-h", type=int, default=0, help="NSP1 min height (0=any, ≤255)")
    ap.add_argument("--wd-ms", type=int, default=0, help="NSP1 watchdog ms (0=host default)")
    args = ap.parse_args()

    root = repo_root()
    src = Path(args.src) if args.src else root / "guest" / "wasm" / f"{args.name}.c"
    if not src.is_file():
        print(f"ERROR: missing {src}", file=sys.stderr)
        print("  Put WASM guest sources under guest/wasm/<name>.c", file=sys.stderr)
        return 1

    clang = find_clang(root)
    if not clang:
        print("ERROR: clang with wasm32 not found.", file=sys.stderr)
        print("  Unzip WASI SDK / clang into .tools/wasi-sdk or .tools/clang-wasm", file=sys.stderr)
        print("  See tools/README-TOOLS.md", file=sys.stderr)
        return 1

    out_dir = root / "dist" / f"{args.name}.nsp"
    if out_dir.exists():
        shutil.rmtree(out_dir)
    out_dir.mkdir(parents=True)
    out_wasm = out_dir / "app.wasm"

    try:
        compile_wasm(clang, src, out_wasm, root / "guest" / "wasm")
    except subprocess.CalledProcessError as e:
        print(f"ERROR: clang failed ({e.returncode})", file=sys.stderr)
        return 1
    except FileNotFoundError:
        print(f"ERROR: cannot run clang at {clang}", file=sys.stderr)
        return 1

    raw = out_wasm.read_bytes()
    size = len(raw)
    print(
        f"[pack-nsp] app.wasm = {size} bytes "
        f"(device max {DEVICE_WASM_MAX}; host linear ≤{LINEAR_LIMIT})"
    )
    try:
        check_device_budget(out_wasm, raw)
    except SystemExit as e:
        print(str(e), file=sys.stderr)
        return 1

    if args.nsp1:
        timers = max(1, min(6, int(args.timers)))
        # NSP1: magic + flags(u16LE) + max_timers + cap_lo + wasm_len(u32LE) + reserved
        flags = 0x0003  # has_ui | has_draw
        if args.need_ui:
            flags |= 0x0008
        if args.need_gfx:
            flags |= 0x0020
        min_w = max(0, min(255, int(args.min_w)))
        min_h = max(0, min(255, int(args.min_h)))
        wd_ms = max(0, min(65535, int(args.wd_ms)))
        hdr = bytearray(16)
        hdr[0:4] = b"NSP1"
        hdr[4] = flags & 0xFF
        hdr[5] = (flags >> 8) & 0xFF
        hdr[6] = timers
        hdr[7] = 0
        wl = len(raw)
        hdr[8] = wl & 0xFF
        hdr[9] = (wl >> 8) & 0xFF
        hdr[10] = (wl >> 16) & 0xFF
        hdr[11] = (wl >> 24) & 0xFF
        hdr[12] = min_w
        hdr[13] = min_h
        hdr[14] = wd_ms & 0xFF
        hdr[15] = (wd_ms >> 8) & 0xFF
        packaged = bytes(hdr) + raw
        # Payload already gated by DEVICE_WASM_MAX; header is host-side only.
        out_wasm.write_bytes(packaged)
        print(
            f"[pack-nsp] NSP1 prepended (timers={timers} flags=0x{flags:x} "
            f"min={min_w}x{min_h} wd={wd_ms} total={len(packaged)})"
        )

    app_id = args.id or f"com.example.{args.name}"
    title = args.title or args.name.replace("_", " ").title()
    manifest = {
        "id": app_id,
        "name": title,
        "version": args.version,
        "entry": "app.wasm",
    }
    man_path = out_dir / "manifest.json"
    man_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")

    readme = out_dir / "README-INSTALL.txt"
    readme.write_text(
        f"NanoShell SD install package: {args.name}\n"
        f"\n"
        f"1. Copy this whole folder to the device SD card as:\n"
        f"     /SD:/ns_data/packages/{args.name}.nsp/\n"
        f"   (folder name should end with .nsp)\n"
        f"2. On the host PC app: install packages (nanoshell install RPC).\n"
        f"3. Enter NanoShell hall and open \"{title}\".\n"
        f"\n"
        f"This is NOT a Windows program. app.wasm size={size} bytes.\n",
        encoding="utf-8",
    )

    print(f"[pack-nsp] wrote {out_dir}")
    print("[pack-nsp] Copy to SD: /SD:/ns_data/packages/ then install on device.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
