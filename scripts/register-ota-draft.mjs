#!/usr/bin/env node
import { appendFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

export function draftPayload(version, baseUrl) {
  if (!/^\d+\.\d+\.\d+-alpha\.\d+$/.test(version)) throw new Error('Expected alpha test version');
  if (new URL(baseUrl).protocol !== 'https:') throw new Error('Artifact base URL must use HTTPS');
  const files = {
    'macos-arm64': 'yoooclaw-darwin-arm64', 'macos-x64': 'yoooclaw-darwin-x64',
    'linux-arm64': 'yoooclaw-linux-arm64', 'linux-x64': 'yoooclaw-linux-x64',
    'windows-x64': 'yoooclaw-win32-x64.exe',
  };
  return {
    releaseName: `CLI ${version}`, component: 'CLI', version,
    platformArtifacts: Object.fromEntries(Object.entries(files).map(([platform, file]) =>
      [platform, { url: `${baseUrl.replace(/\/+$/, '')}/v${version}/${file}` }])),
    releaseNotes: `CLI ${version} 测试构建；灰度策略及发布由平台操作。`,
  };
}

export async function registerDraft(endpoint, token, payload, fetchImpl = fetch) {
  if (!token || new URL(endpoint).protocol !== 'https:') throw new Error('Missing token or invalid OTA endpoint');
  // Creation is not idempotent: after an uncertain response, inspect the platform before retrying.
  const response = await fetchImpl(endpoint, {
    method: 'POST', redirect: 'error', signal: AbortSignal.timeout(180_000),
    headers: { 'Content-Type': 'application/json', 'X-CI-Token': token }, body: JSON.stringify(payload),
  });
  if (!response.ok) throw new Error(`OTA draft HTTP ${response.status}`);
  const result = await response.json();
  if (result.success !== true || result.code !== '000000' || !Number.isSafeInteger(result.data) || result.data <= 0) {
    const code = /^[A-Z0-9_]{1,64}$/.test(result.code ?? '') ? result.code : 'INVALID_RESPONSE';
    throw new Error(`OTA draft rejected (${code}); check platform before retrying`);
  }
  return result.data;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const payload = draftPayload(process.env.VERSION, process.env.OTA_ARTIFACT_BASE_URL);
    const id = await registerDraft(process.env.OTA_DRAFT_URL, process.env.OTA_PLUGIN_RELEASE_PIPELINE_TOKEN, payload);
    console.log(`OTA draft registered: releaseId=${id}, version=${payload.version}`);
    if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY,
      `\n### CLI test draft\n\nVersion: ${payload.version}\n\nRelease ID: ${id}\n\nStatus: DRAFT\n`);
  } catch (error) {
    console.error(error.message.replaceAll(process.env.OTA_PLUGIN_RELEASE_PIPELINE_TOKEN || '\0', '[REDACTED]'));
    process.exitCode = 1;
  }
}
