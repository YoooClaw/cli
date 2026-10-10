#!/usr/bin/env python3
"""Version-locked, per-user NanoShell dependency registry (Linux/macOS)."""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time

SKILL = Path(__file__).resolve().parents[1]
LOCK = "nanoshell-deps.lock.json"


def write_json(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + f".{os.getpid()}.tmp")
    tmp.write_text(json.dumps(data, indent=2) + "\n")
    tmp.replace(path)


def pin(project, upgrade=False):
    path = project / LOCK
    if upgrade or not path.exists():
        if path.exists():
            shutil.copyfile(path, path.with_name(LOCK + f".{time.time_ns()}.bak"))
        write_json(path, json.loads((SKILL / "dependencies.json").read_text()))
    return json.loads(path.read_text())


@contextlib.contextmanager
def install_lock(directory):
    import fcntl
    directory.mkdir(parents=True, exist_ok=True)
    with (directory / ".install.lock").open("w") as f:
        until = time.monotonic() + 120
        while True:
            try:
                fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() > until:
                    raise RuntimeError("Another dependency install is still running; retry later")
                time.sleep(0.5)
        yield


def sdk_valid(root, version):
    try:
        if (root / "VERSION").read_text().splitlines()[0].strip() != version:
            return False
        with tempfile.TemporaryDirectory() as tmp:
            r = subprocess.run([str(root / "bin/clang"), "--target=wasm32", "-nostdlib",
                                "-Wl,--no-entry", "-Wl,--export=start", "-x", "c", "-",
                                "-o", str(Path(tmp) / "probe.wasm")],
                               input="void start(void){}", text=True, capture_output=True, timeout=30)
            return r.returncode == 0
    except (OSError, subprocess.TimeoutExpired, IndexError):
        return False


def pw_valid(root, version):
    try:
        if json.loads((root / "package.json").read_text())["version"] != version:
            return False
        r = subprocess.run(["node", "-e", "require(process.argv[1]).chromium", str(root)],
                           capture_output=True, timeout=15)
        return r.returncode == 0
    except (OSError, ValueError, KeyError, subprocess.TimeoutExpired):
        return False


def resolve(project, kind, install=False):
    spec = pin(project)
    key = platform.system().lower() + "-" + platform.machine().lower()
    dep = spec["wasi_sdk" if kind == "sdk" else "playwright"]
    version = dep["version"]
    if not version or any(c not in "0123456789." for c in version):
        raise ValueError("Invalid pinned dependency version")
    cache = Path(os.environ.get("NS_DEPENDENCY_HOME", Path.home() / ".local/share/nanoshell"))
    folder = cache / ("wasi-sdk" if kind == "sdk" else "playwright") / version / key
    validator = sdk_valid if kind == "sdk" else pw_valid
    env_key = "NS_CLANG" if kind == "sdk" else "NS_PLAYWRIGHT_MODULE"
    explicit = os.environ.get(env_key)
    if explicit:
        path = Path(explicit).absolute()
        if kind == "sdk": path = path.parent.parent
        if not validator(path, version):
            raise RuntimeError(f"{env_key} does not match working pinned version {version}; clear or correct it")
        with install_lock(folder):
            write_json(folder / "reference.json", {"version": version, "platform": key,
                       "path": str(path.resolve()), "source": "explicit"})
        return path
    with install_lock(folder):
        receipt = folder / "reference.json"
        candidates = []
        if receipt.exists():
            candidates.append(Path(json.loads(receipt.read_text())["path"]))
        candidates.append(folder / ("sdk" if kind == "sdk" else "node_modules/playwright"))
        relative = ".tools/wasi-sdk" if kind == "sdk" else ".qa/node_modules/playwright"
        candidates.append(project / relative)
        # Nearby projects from older skills can register their existing install once.
        candidates.extend(sorted(project.parent.glob("*/" + relative)))
        for path in candidates:
            if validator(path, version):
                write_json(receipt, {"version": version, "platform": key, "path": str(path.resolve()), "source": "reused"})
                return path.resolve()
        if not install:
            raise RuntimeError(f"Missing {kind} {version}; run dependencies.py setup --project {project}")
        with tempfile.TemporaryDirectory(prefix="install-", dir=folder) as tmp:
            stage = Path(tmp)
            if kind == "sdk":
                asset = dep["platforms"].get(key)
                if not asset:
                    raise RuntimeError(f"No pinned WASI SDK archive for {key}")
                archive = stage / "sdk.tar.gz"
                success = False
                for url in asset["urls"]:
                    r = subprocess.run(["curl", "-fL", "--retry", "2", "--connect-timeout", "15",
                                        "--max-time", "550", "-o", str(archive), url],
                                       stdout=sys.stderr, stderr=sys.stderr)
                    if r.returncode == 0:
                        if hashlib.sha256(archive.read_bytes()).hexdigest() != asset["sha256"]:
                            raise RuntimeError("SDK SHA-256 mismatch; archive rejected")
                        success = True
                        break
                if not success: raise RuntimeError("All pinned SDK download URLs failed")
                unpack = stage / "unpacked"
                unpack.mkdir()
                with tarfile.open(archive) as tar:
                    # Python 3.10+ supports an explicit backport-safe containment check.
                    for item in tar.getmembers():
                        target = (unpack / item.name).resolve()
                        if not target.is_relative_to(unpack.resolve()) or item.isdev():
                            raise RuntimeError("Unsafe archive member")
                        if item.issym() or item.islnk():
                            link = (target.parent if item.issym() else unpack) / item.linkname
                            if not link.resolve().is_relative_to(unpack.resolve()):
                                raise RuntimeError("Unsafe archive link")
                    if hasattr(tarfile, "data_filter"):
                        tar.extractall(unpack, filter="data")
                    else:
                        tar.extractall(unpack)
                roots = list(unpack.iterdir())
                if len(roots) != 1 or not sdk_valid(roots[0], version):
                    raise RuntimeError("Downloaded SDK failed version/compile/link checks")
                ready = roots[0]
                target = folder / "sdk"
            else:
                write_json(stage / "package.json", {"private": True})
                env = dict(os.environ, PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD="1")
                subprocess.run(["npm", "install", "--save-exact", f"playwright@{version}",
                                "--no-audit", "--no-fund"], cwd=stage, env=env,
                               stdout=sys.stderr, check=True, timeout=300)
                if not pw_valid(stage / "node_modules/playwright", version):
                    raise RuntimeError("Installed Playwright failed version/load checks")
                ready = stage / "node_modules"
                target = folder / "node_modules"
            if target.exists():
                target.rename(folder / (target.name + f".invalid-{time.time_ns()}"))
            ready.rename(target)
            path = target if kind == "sdk" else target / "playwright"
            write_json(receipt, {"version": version, "platform": key, "path": str(path), "source": "installed"})
            return path


def environment(project, install=False, only="all"):
    result = {}
    if only in ("all", "sdk"):
        result["NS_CLANG"] = str(resolve(project, "sdk", install) / "bin/clang")
    if only in ("all", "playwright"):
        result["NS_PLAYWRIGHT_MODULE"] = str(resolve(project, "playwright", install))
    chrome = os.environ.get("NS_CHROMIUM_EXECUTABLE")
    if chrome: result["NS_CHROMIUM_EXECUTABLE"] = chrome
    elif os.access("/opt/google/chrome/chrome", os.X_OK):
        result["NS_CHROMIUM_EXECUTABLE"] = "/opt/google/chrome/chrome"
    return result


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("command", choices=["setup", "env", "upgrade"])
    p.add_argument("--project", required=True, type=Path)
    p.add_argument("--only", choices=["all", "sdk", "playwright"], default="all")
    args = p.parse_args()
    project = args.project.resolve()
    if not (project / "snapshot.json").is_file(): p.error("Initialize a project first")
    if args.command == "upgrade": pin(project, upgrade=True)
    print(json.dumps(environment(project, install=args.command != "env", only=args.only)))


if __name__ == "__main__":
    try: main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as e:
        print("ERROR:", e, file=sys.stderr)
        sys.exit(1)
