#!/usr/bin/env python3
"""Prepare, build, preview and release a portable NanoShell app project."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
import zipfile
from dependencies import pin, environment

SKILL = Path(__file__).resolve().parents[1]


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["init", "build", "serve", "release"])
    parser.add_argument("--project", required=True, type=Path)
    parser.add_argument("--name")
    parser.add_argument("--id")
    parser.add_argument("--title")
    parser.add_argument("--version", type=int, default=1)
    parser.add_argument("--port", type=int, default=18766)
    parser.add_argument("--approved", action="store_true")
    args = parser.parse_args()
    root = args.project.resolve()
    if args.command == "init":
        if root.exists():
            parser.error("init requires a new project directory")
        shutil.copytree(SKILL / "assets/sdk", root)
        pin(root)
        print(root)
        return
    if not (root / "snapshot.json").is_file():
        parser.error("project is missing SDK snapshot.json; use init")
    if args.command == "serve":
        subprocess.run([sys.executable, "-u", str(root / "tools/web-preview/serve.py"),
                        "--port", str(args.port)], check=True)
        return
    if not args.name or not re.fullmatch(r"[a-z][a-z0-9_]{0,39}", args.name):
        parser.error("--name must match [a-z][a-z0-9_]{0,39}")
    name = args.name
    package = root / "dist" / (name + ".nsp")
    report_path = root / "qa" / name / "report.json"
    if args.command == "build":
        # Invalidate previous QA before attempting any rebuild.
        report_path.unlink(missing_ok=True)
        if args.version < 1:
            parser.error("--version must be positive")
        cmd = [sys.executable, str(root / "tools/pack_nsp.py"), name, "--nsp1", "--version", str(args.version)]
        if args.id:
            cmd += ["--id", args.id]
        if args.title:
            cmd += ["--name-title", args.title]
        import os
        subprocess.run(cmd, cwd=root, env=dict(os.environ, **environment(root, only="sdk")), check=True)
        manifest = json.loads((package / "manifest.json").read_text())
        catalog = {"demos": [{"id": manifest["id"], "name": manifest["name"],
                              "file": name + ".nsp/app.wasm", "web": True}]}
        (root / "dist/catalog.json").write_text(json.dumps(catalog, ensure_ascii=False, indent=2))
        print("Candidate prepared:", package)
        return
    if not args.approved:
        parser.error("release requires --approved after the user accepts this preview")
    report = json.loads(report_path.read_text())
    if report.get("status") != "passed" or report.get("app") != name or not report.get("requirements"):
        parser.error("a passing report with app-specific acceptance tests is required")
    inputs = report.get("inputs", {})
    required = [f"dist/{name}.nsp/app.wasm", f"dist/{name}.nsp/manifest.json",
                f"guest/wasm/{name}.c", "guest/sdk/ns_guest.h", "acceptance.md", "snapshot.json",
                "dist/catalog.json", "tools/pack_nsp.py", "nanoshell-deps.lock.json"]
    if not all(key in inputs for key in required):
        parser.error("incomplete QA input inventory")
    for rel, digest in inputs.items():
        path = (root / rel).resolve()
        if not path.is_relative_to(root) or not path.is_file() or sha(path) != digest:
            parser.error("QA input changed or missing: " + rel)
    for path in package.iterdir():
        if path.name not in {"app.wasm", "manifest.json", "README-INSTALL.txt"} or not path.is_file():
            parser.error("unexpected package member: " + path.name)
    release = root / "release"
    release.mkdir(exist_ok=True)
    archive = release / (name + ".zip")
    with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as z:
        for path in sorted(package.iterdir()):
            info = zipfile.ZipInfo(f"{name}.nsp/{path.name}", (1980, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.create_system = 3
            info.external_attr = 0o100644 << 16
            z.writestr(info, path.read_bytes())
        # QA remains beside the project; volatile timestamps do not enter the device ZIP.
    archive.with_suffix(".zip.sha256").write_text(sha(archive) + "  " + archive.name + "\n")
    print(archive)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print("ERROR:", exc, file=sys.stderr)
        sys.exit(1)
