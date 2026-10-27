import { test } from 'node:test';
import assert from 'node:assert/strict';
import { draftPayload, registerDraft } from './register-ota-draft.mjs';
test('maps all shipped native binaries to OTA platform keys', () => {
  const p = draftPayload('0.13.0-alpha.123', 'https://test.example/cli/');
  assert.deepEqual(p.platformArtifacts, Object.fromEntries([
    ['macos-arm64','darwin-arm64'],['macos-x64','darwin-x64'],['linux-arm64','linux-arm64'],
    ['linux-x64','linux-x64'],['windows-x64','win32-x64.exe'],
  ].map(([k, v]) => [k, { url: `https://test.example/cli/v0.13.0-alpha.123/yoooclaw-${v}` }])));
  assert.equal(p.component, 'CLI');
  assert.equal(p.stages, undefined);
});
test('accepts the draft ID and sends token only in headers', async () => {
  const payload = draftPayload('0.13.0-alpha.123', 'https://test.example/cli');
  const id = await registerDraft('https://test.example/api', 'secret', payload, async (_, options) => {
    assert.equal(options.headers['X-CI-Token'], 'secret');
    assert.equal(options.body.includes('secret'), false);
    return { ok: true, json: async () => ({ success: true, code: '000000', data: 5 }) };
  });
  assert.equal(id, 5);
});
test('rejects business failure without retry or raw response logging', async () => {
  let calls = 0;
  await assert.rejects(registerDraft('https://test.example/api', 'secret', {}, async () => {
    calls++;
    return { ok: true, json: async () => ({ success: false, code: 'OTA00004', message: 'secret' }) };
  }), /OTA00004/);
  assert.equal(calls, 1);
});
test('accepts alpha, beta and stable versions', () => {
  for (const version of ['0.13.1-alpha.448', '0.13.1-beta.0', '0.13.1']) {
    const p = draftPayload(version, 'https://prod.example/cli');
    assert.equal(p.version, version);
    assert.equal(p.platformArtifacts['windows-x64'].url, `https://prod.example/cli/v${version}/yoooclaw-win32-x64.exe`);
  }
});
test('rejects unsupported or malformed version formats', () => {
  for (const version of ['v0.13.1', '0.13.1-test.1', '0.13.1-beta', '0.13.1-alpha.01', '00.13.1', '0.13.1+build']) {
    assert.throws(() => draftPayload(version, 'https://prod.example/cli'), /Expected/);
  }
});
