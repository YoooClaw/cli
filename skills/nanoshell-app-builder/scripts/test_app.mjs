#!/usr/bin/env node
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import assert from 'node:assert/strict';
import {spawn, execFileSync} from 'node:child_process';
import {createRequire} from 'node:module';
import {pathToFileURL, fileURLToPath} from 'node:url';

const args = {};
for (let i = 2; i < process.argv.length; i += 2) args[process.argv[i]] = process.argv[i + 1];
assert(args['--project'] && /^[a-z][a-z0-9_]{0,39}$/.test(args['--name'] || ''),
  'Usage: test_app.mjs --project DIR --name NAME --scenario tests/NAME.mjs');
const root = path.resolve(args['--project']);
const name = args['--name'];
const out = path.join(root, 'qa', name);
fs.mkdirSync(out, {recursive: true});
const reportPath = path.join(out, 'report.json');
const report = {status: 'failed', app: name, testedAt: new Date().toISOString(),
  scope: 'Browser Host; device not tested', checks: [], requirements: [], inputs: {}};
const sha = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
const file = rel => path.join(root, rel);
function inventory(rel) {
  const p = file(rel);
  if (fs.statSync(p).isDirectory()) {
    for (const child of fs.readdirSync(p).sort()) inventory(rel + '/' + child);
  } else report.inputs[rel] = sha(fs.readFileSync(p));
}
let server, browser, page;
const deadline = setTimeout(() => {
  report.error = 'Overall test deadline exceeded (60 seconds)';
  fs.writeFileSync(reportPath, JSON.stringify(report, null, 2));
  server?.kill();
  browser?.close().finally(() => process.exit(1));
  setTimeout(() => process.exit(1), 1000);
}, 60000);
try {
  assert(args['--scenario'], 'An app-specific acceptance scenario is required');
  const python = process.env.NS_PYTHON || (process.platform === 'win32' ? 'python' : 'python3');
  const depEnv = JSON.parse(execFileSync(python,
    [fileURLToPath(new URL('./dependencies.py', import.meta.url)), 'env', '--project', root, '--only', 'playwright'],
    {encoding: 'utf8', timeout: 150000}));
  Object.assign(process.env, depEnv);
  inventory('nanoshell-deps.lock.json');
  report.dependencies = JSON.parse(fs.readFileSync(file('nanoshell-deps.lock.json')));
  const scenario = path.resolve(root, args['--scenario']);
  assert(scenario.startsWith(root + path.sep), 'Scenario must live inside the project');
  inventory('acceptance.md');
  inventory('snapshot.json');
  inventory('tools/pack_nsp.py');
  inventory('host/include/nanoshell/wasm_limits.h');
  inventory('dist/catalog.json');
  inventory('guest/wasm');
  inventory('guest/sdk');
  inventory('tools/web-preview');
  inventory(`dist/${name}.nsp`);
  inventory(path.relative(root, scenario).split(path.sep).join('/'));
  const pkg = `dist/${name}.nsp/`;
  const bytes = fs.readFileSync(file(pkg + 'app.wasm'));
  const manifest = JSON.parse(fs.readFileSync(file(pkg + 'manifest.json')));
  let wasm = bytes;
  if (bytes.subarray(0, 4).toString() === 'NSP1') {
    assert(bytes.length >= 16, 'Truncated NSP1 header');
    assert.equal(bytes.readUInt32LE(8), bytes.length - 16, 'NSP1 payload length mismatch');
    assert(bytes[12] <= 240 && bytes[13] <= 120, 'NSP1 requires a larger display');
    wasm = bytes.subarray(16);
  }
  const limits = fs.readFileSync(file('host/include/nanoshell/wasm_limits.h'), 'utf8');
  const maxWasm = Number(limits.match(/#define\s+NS_WASM_MAX_BYTES\s+(\d+)U?/)?.[1]);
  assert(maxWasm > 0, 'Missing device Wasm budget');
  assert(wasm.length > 0 && wasm.length <= maxWasm, `Device wasm exceeds ${maxWasm} bytes or is empty`);
  report.wasmBytes = wasm.length;
  report.packageBytes = bytes.length;
  assert.equal(manifest.entry, 'app.wasm');
  assert(typeof manifest.id === 'string' && manifest.id.length > 0);
  assert(typeof manifest.name === 'string' && manifest.name.length > 0);
  assert(Number.isInteger(manifest.version) && manifest.version > 0);
  const catalog = JSON.parse(fs.readFileSync(file('dist/catalog.json')));
  const entry = catalog.demos.find(d => d.id === manifest.id);
  assert(entry && entry.file === `${name}.nsp/app.wasm`, 'Catalog must select the device package');
  const module = await WebAssembly.compile(wasm);
  const allowed = new Set(fs.readFileSync(file('guest/wasm/allowed_imports.txt'), 'utf8')
    .split(/\r?\n/).map(s => s.trim()).filter(s => s && !s.startsWith('#')));
  for (const imp of WebAssembly.Module.imports(module)) {
    assert(imp.module === 'ns' && imp.kind === 'function' && allowed.has(imp.name),
      `Forbidden import ${imp.module}.${imp.name}`);
  }
  assert(WebAssembly.Module.exports(module).some(e => e.name === 'start' && e.kind === 'function'));
  report.checks.push('package, size, wasm validation, imports, entry, preview parity');
  const require = createRequire(import.meta.url);
  let pw;
  if (process.env.NS_PLAYWRIGHT_MODULE) pw = require(process.env.NS_PLAYWRIGHT_MODULE);
  else {
    try { pw = require(path.join(root, '.qa/node_modules/playwright')); }
    catch { pw = require('playwright'); }
  }
  server = spawn(python, ['-u', file('tools/web-preview/serve.py'), '--port', '0'],
    {stdio: ['ignore', 'pipe', 'pipe']});
  const url = await new Promise((resolve, reject) => {
    let output = '';
    const timeout = setTimeout(() => reject(new Error('Preview server startup timeout')), 8000);
    server.on('error', e => { clearTimeout(timeout); reject(e); });
    server.on('exit', code => { clearTimeout(timeout); reject(new Error(`Server exited ${code}: ${output}`)); });
    server.stderr.on('data', b => { output += b; });
    server.stdout.on('data', b => {
      output += b;
      const match = output.match(/http:\/\/127\.0\.0\.1:([1-9][0-9]*)\//);
      if (match) { clearTimeout(timeout); resolve(match[0]); }
    });
  });
  browser = await pw.chromium.launch({headless: true,
    ...(process.env.NS_CHROMIUM_EXECUTABLE ? {executablePath: process.env.NS_CHROMIUM_EXECUTABLE} : {})});
  report.browser = browser.version();
  report.node = process.version;
  page = await browser.newPage();
  page.setDefaultTimeout(6000);
  const errors = [];
  page.on('pageerror', e => errors.push(String(e)));
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  await page.addInitScript(() => {
    const NativeWorker = window.Worker;
    window.__qa = {frames: 0, errors: [], statuses: [], stopped: 0, key: null};
    window.Worker = class extends NativeWorker {
      constructor(...params) {
        super(...params);
        this.addEventListener('message', ({data}) => {
          if (data.type === 'frame') window.__qa.frames++;
          if (data.type === 'error') window.__qa.errors.push(data.text);
          if (data.type === 'status') window.__qa.statuses.push(data.text);
          if (data.type === 'stopped') window.__qa.stopped++;
        });
        this.addEventListener('error', e => window.__qa.errors.push(e.message));
      }
      postMessage(message, ...rest) {
        if (message.type === 'init') window.__qa.key = new Int32Array(message.keySab);
        return super.postMessage(message, ...rest);
      }
    };
  });
  const response = await page.goto(url);
  assert.equal(response.status(), 200);
  const packageResponse = await page.request.get(new URL('/dist/' + entry.file, url).href);
  assert.equal(packageResponse.status(), 200);
  assert.equal(sha(await packageResponse.body()), sha(bytes), 'HTTP preview package differs from tested bytes');
  assert(await page.evaluate(() => crossOriginIsolated && typeof SharedArrayBuffer !== 'undefined'));
  const index = catalog.demos.filter(d => d.web).findIndex(d => d.id === manifest.id);
  const run = page.locator('.demo-row').nth(index).getByRole('button', {name: '运行'});
  await run.click();
  await page.waitForFunction(() => window.__qa.frames >= 3);
  const nonBlank = await page.evaluate(() => {
    const c = document.querySelector('#screen');
    const d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
    for (let i = 4; i < d.length; i += 4) {
      if (d[i] !== d[0] || d[i+1] !== d[1] || d[i+2] !== d[2]) return true;
    }
    return false;
  });
  assert(nonBlank, 'Application must render non-uniform visible content');
  await page.locator('#screen').screenshot({path: path.join(out, 'startup.png')});
  report.checks.push('isolated page, continuous frames, nonblank screen');
  const consume = async (hold) => {
    const before = await page.evaluate(() => Atomics.load(window.__qa.key, 1));
    await page.keyboard.down('Enter');
    if (hold) await page.waitForTimeout(240);
    await page.keyboard.up('Enter');
    await page.waitForFunction(prior => Atomics.load(window.__qa.key, 1) > prior, before);
  };
  await consume(false);
  await consume(true);
  report.checks.push('OK tap and hold input consumed');
  // Restart so app-specific assertions begin with a clean session.
  await page.keyboard.press('Escape');
  await page.waitForFunction(() => window.__qa.stopped >= 1);
  assert(await page.evaluate(() => window.__qa.statuses.includes('已退出')),
    'BACK should invoke normal guest exit');
  const frames = await page.evaluate(() => window.__qa.frames);
  await run.click();
  await page.waitForFunction(n => window.__qa.frames > n + 2, frames);
  const {default: scenarioFn} = await import(pathToFileURL(scenario).href);
  report.requirements = await scenarioFn({page, assert});
  assert(Array.isArray(report.requirements) && report.requirements.length > 0 &&
    report.requirements.every(x => typeof x === 'string' && x.length > 0),
    'Scenario must return asserted requirement IDs');
  await page.locator('#screen').screenshot({path: path.join(out, 'interaction.png')});
  const stopped = await page.evaluate(() => window.__qa.stopped);
  await page.keyboard.press('Escape');
  await page.waitForFunction(n => window.__qa.stopped > n, stopped);
  assert.deepEqual(await page.evaluate(() => window.__qa.errors), []);
  assert.deepEqual(errors, []);
  report.checks.push('BACK normal exit, relaunch, acceptance scenario, no browser or Worker errors');
  for (const [rel, digest] of Object.entries(report.inputs)) {
    assert.equal(sha(fs.readFileSync(file(rel))), digest, `Input changed during test: ${rel}`);
  }
  report.hardwareSimulation = await page.evaluate(() => window.nsHardware.snapshot());
  assert.equal(report.hardwareSimulation.dropped, 0, 'Hardware call log overflow: narrow or shorten scenario');
  report.hardwareValidation = {scope: 'browser simulation only', realDevice: 'not tested'};
  report.status = 'passed';
} catch (e) {
  report.error = String(e.stack || e);
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  if (page && !page.isClosed()) {
    try { report.hardwareSimulation = await page.evaluate(() => window.nsHardware?.snapshot()); } catch {}
  }
  await browser?.close();
  server?.kill();
  fs.writeFileSync(reportPath, JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify({status: report.status, report: reportPath, error: report.error || null}));
}
