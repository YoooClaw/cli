#!/usr/bin/env python3
"""HTTP server for NanoShell web preview (COOP/COEP → SharedArrayBuffer).

Serves this directory for UI, and repo dist/ under /dist/ (same .nsp as Win sim).
"""
from __future__ import annotations

import argparse
import functools
import posixpath
import sys
import urllib.parse
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


PREVIEW_ROOT = Path(__file__).resolve().parent
REPO_ROOT = PREVIEW_ROOT.parent.parent
DIST_ROOT = REPO_ROOT / "dist"


class Handler(SimpleHTTPRequestHandler):
    extensions_map = {
        **getattr(SimpleHTTPRequestHandler, "extensions_map", {}),
        ".wasm": "application/wasm",
        ".js": "text/javascript",
        ".mjs": "text/javascript",
        ".css": "text/css",
        ".json": "application/json",
        ".html": "text/html",
    }

    def end_headers(self) -> None:
        # Needed so SharedArrayBuffer is available and transferable to Worker.
        self.send_header("Cross-Origin-Opener-Policy", "same-origin")
        self.send_header("Cross-Origin-Embedder-Policy", "require-corp")
        self.send_header("Cross-Origin-Resource-Policy", "same-origin")
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-NanoShell-Preview", "1")
        super().end_headers()

    def translate_path(self, path: str) -> str:
        parsed = urllib.parse.urlparse(path).path
        parsed = posixpath.normpath(urllib.parse.unquote(parsed))
        parts = [p for p in parsed.split("/") if p and p != ".."]
        if parts and parts[0] == "dist":
            rel = Path(*parts[1:]) if len(parts) > 1 else Path()
            target = (DIST_ROOT / rel).resolve()
            try:
                target.relative_to(DIST_ROOT.resolve())
            except ValueError:
                return str(DIST_ROOT / "__denied__")
            return str(target)
        return super().translate_path(path)

    def log_message(self, fmt: str, *args) -> None:
        sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=18765)
    ap.add_argument("--bind", default="127.0.0.1")
    args = ap.parse_args()
    handler = functools.partial(Handler, directory=str(PREVIEW_ROOT))
    httpd = ThreadingHTTPServer((args.bind, args.port), handler)
    print(f"NanoShell web preview: http://{args.bind}:{httpd.server_port}/")
    print(f"  packages: {DIST_ROOT}  →  /dist/*.nsp/")
    print("  COOP + COEP + CORP enabled (required for keys)")
    if not DIST_ROOT.is_dir():
        print("  WARN: dist/ missing — run build.bat first", file=sys.stderr)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\nbye")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
