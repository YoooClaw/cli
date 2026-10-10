#!/usr/bin/env python3
"""Integration test for the distributable skill. Needs NS_CLANG and Playwright."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import zipfile

repo = Path(__file__).resolve().parents[1]
skill = repo / "skills/nanoshell-app-builder"
pipeline = skill / "scripts/pipeline.py"
runner = skill / "scripts/test_app.mjs"


def run(cmd, ok=True):
    result = subprocess.run(cmd, capture_output=True, text=True, timeout=90)
    if (result.returncode == 0) != ok:
        raise AssertionError(result.stdout + result.stderr)
    return result


with tempfile.TemporaryDirectory(prefix="nanoshell-skill-test-") as tmp:
    root = Path(tmp) / "project"
    base = [sys.executable, str(pipeline)]
    options = ["--project", str(root), "--name", "counter"]
    run(base + ["init", "--project", str(root)])
    run(base + ["init", "--project", str(root)], ok=False)
    run([sys.executable, str(skill / "scripts/dependencies.py"), "setup", "--project", str(root)])
    for demo in ("chrono", "pulse", "quake", "hello_tiny", "hop", "snake"):
        run(base + ["build", "--project", str(root), "--name", demo])
    run(base + ["build", *options], ok=True)
    (root / "acceptance.md").write_text("R1: Starts at zero.\nR2: One tap adds one.\n")
    tests = root / "tests"
    tests.mkdir()
    scenario = tests / "counter.mjs"
    scenario.write_text('''export default async function ({page, assert}) {
  const read = () => page.evaluate(() => {
    const c = document.querySelector('#screen').getContext('2d');
    let bits = '';
    for (let y=0;y<5;y++) for (let x=0;x<3;x++) {
      const pixel = c.getImageData(41+x*3,33+y*3,1,1).data;
      bits += pixel[0]>180 ? '1' : '0';
    }
    return bits;
  });
  assert.equal(await read(), '111101101101111', 'R1: initial zero');
  await page.keyboard.press('Enter');
  await page.waitForTimeout(120);
  assert.equal(await read(), '010010010010010', 'R2: one tap -> one');
  return ['R1','R2'];
}
''')
    test = [os.environ.get("NS_NODE", "node"), str(runner), *options,
            "--scenario", "tests/counter.mjs"]
    run(test)
    report = root / "qa/counter/report.json"
    assert json.loads(report.read_text())["status"] == "passed"
    assert (root / "dist/counter.nsp/app.wasm").read_bytes()[:4] == b"NSP1"
    assert not (root / "tools/web-preview/samples").exists()
    run(base + ["release", *options], ok=False)
    # Simulate acceptance only in this temporary integration fixture.
    run(base + ["release", *options, "--approved"])
    with zipfile.ZipFile(root / "release/counter.zip") as z:
        assert z.read("counter.nsp/app.wasm") == (root / "dist/counter.nsp/app.wasm").read_bytes()
        assert "counter.nsp/README-INSTALL.txt" in z.namelist()
    archive = root / "release/counter.zip"
    released = archive.read_bytes()
    with zipfile.ZipFile(archive) as z:
        assert "verification/report.json" not in z.namelist()
    # QA execution time and filesystem mtimes do not change the install package ID.
    data = json.loads(report.read_text())
    data["testedAt"] = "2099-01-01T00:00:00Z"
    report.write_text(json.dumps(data))
    os.utime(root / "dist/counter.nsp/app.wasm", None)
    run(base + ["release", *options, "--approved"])
    assert archive.read_bytes() == released
    # Optional end-to-end check with a freshly built CLI, using isolated credentials/storage.
    cli = os.environ.get("NS_YOOOCLAW")
    if cli:
        home = Path(tmp) / "cli-home"
        home.mkdir()
        (home / "credentials.json").write_text(json.dumps({"apiKeys": [
            {"label": "phone-a", "key": "integration-test", "default": True}]}))
        env = dict(os.environ, YOOOCLAW_HOME=str(home))
        def cli_call(*args):
            result = subprocess.run([cli, "--format", "json", *args], env=env,
                                    capture_output=True, text=True, timeout=30)
            assert result.returncode == 0, result.stdout + result.stderr
            return result.stdout
        first = cli_call("nanoshell", "publish", "--package", str(archive), "--client", "phone-a")
        import hashlib
        assert hashlib.sha256((root / "dist/counter.nsp/app.wasm").read_bytes()).hexdigest() in first
        second = cli_call("nanoshell", "publish", "--package", str(archive), "--client", "phone-a")
        assert '"duplicated":true' in second.replace(" ", "")
        assert hashlib.sha256((root / "dist/counter.nsp/app.wasm").read_bytes()).hexdigest() in cli_call("nanoshell", "list", "--client", "phone-a")
        assert (home / "profiles/default/nanoshell").is_dir()
    source = root / "guest/wasm/counter.c"
    original = source.read_bytes()
    source.write_bytes(original + b"\n/* modified after QA */\n")
    run(base + ["release", *options, "--approved"], ok=False)
    source.write_bytes(original)
    package = root / "dist/counter.nsp/app.wasm"
    original_wasm = package.read_bytes()
    package.write_bytes(original_wasm + b"changed")
    run(base + ["release", *options, "--approved"], ok=False)
    package.write_bytes(original_wasm)
    scenario.write_text("export default async ({assert}) => { assert.fail('Deliberate acceptance failure'); };\n")
    run(test, ok=False)
    assert json.loads(report.read_text())["status"] == "failed"
    run(base + ["release", *options, "--approved"], ok=False)
    # WebAssembly remains valid but an imported module is no longer ns.
    poisoned = original_wasm.replace(b"\x02ns", b"\x02xx", 1)
    assert poisoned != original_wasm
    package.write_bytes(poisoned)
    run(test, ok=False)
    assert "Forbidden import" in json.loads(report.read_text())["error"]
    # Exercise both raw and wrapped modules at the 12 KiB boundary.
    def uleb(n):
        result = bytearray()
        while n >= 128:
            result.append((n & 127) | 128)
            n >>= 7
        result.append(n)
        return bytes(result)

    # Verify the new active-data limit independently of browser page-sized memory.
    import importlib.util
    spec = importlib.util.spec_from_file_location("pack_budget", root / "tools/pack_nsp.py")
    pack = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(pack)
    for data_size, accepted in ((5120, True), (5121, False)):
        # Active data at offset 3072 (stack-first): ends at 8192 / 8193.
        data = b"\x01\x00\x41\x80\x18\x0b" + uleb(data_size) + b"x" * data_size
        module = b"\x00asm\x01\x00\x00\x00" + b"\x0b" + uleb(len(data)) + data
        try:
            pack.check_device_budget(package, module)
        except SystemExit as exc:
            assert not accepted and "host limit 8192" in str(exc), str(exc)
        else:
            assert accepted, "Over-limit active data must be rejected"

    raw = original_wasm[16:]
    extra = b"\x00" + b"x" * (12288 - len(raw) - 4)
    boundary = raw + b"\x00" + uleb(len(extra)) + extra
    assert len(boundary) == 12288
    # Reuse a passing scenario for the boundary run.
    scenario.write_text("export default async () => ['boundary-runtime'];\n")
    package.write_bytes(boundary)
    run(test)
    assert json.loads(report.read_text())["wasmBytes"] == 12288
    header = bytearray(original_wasm[:16])
    header[8:12] = (12288).to_bytes(4, "little")
    package.write_bytes(bytes(header) + boundary)
    run(test)
    assert json.loads(report.read_text())["packageBytes"] == 12304
    package.write_bytes(boundary + b"x")
    run(test, ok=False)
    assert "12288" in json.loads(report.read_text())["error"]
    package.write_bytes(original_wasm[:-1])
    run(test, ok=False)
    assert "NSP1 payload length mismatch" in json.loads(report.read_text())["error"]
    run(base + ["build", *options])
    assert not report.exists(), "Rebuild must invalidate the old report"
    run(base + ["build", *options, "--version", "2"])
    assert json.loads((root / "dist/counter.nsp/manifest.json").read_text())["version"] == 2
    print("PASS: portable init/build, browser acceptance, release byte parity, missing approval,")
    print("      source/package mutation, failed scenario, forbidden import, stale-report invalidation,")
    print("      7 SDK demos compiled, raw/NSP1 12 KiB boundary, oversize and truncated package rejection")
