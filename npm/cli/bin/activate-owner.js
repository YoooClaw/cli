#!/usr/bin/env node
"use strict";

// Restore the owner recorded by preinstall. Switching Hermes -> CLI is never
// inferred from an update: it requires YOOOCLAW_ACTIVATE_OWNER=cli.

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const root = process.env.YOOOCLAW_HOME || path.join(os.homedir(), ".yoooclaw");
const statePath = path.join(root, ".cli-install-handoff.json");
let state = { activationRequested: false, runningProfiles: [] };
try {
  state = JSON.parse(fs.readFileSync(statePath, "utf8"));
} catch {
  // A first install or an npm client that skipped preinstall has no owner to restore.
}
const activationRequested =
  process.env.YOOOCLAW_ACTIVATE_OWNER === "cli" || state.activationRequested === true;
const runningProfiles = Array.isArray(state.runningProfiles) ? state.runningProfiles : [];

if (!activationRequested && runningProfiles.length === 0) {
  try {
    fs.unlinkSync(statePath);
  } catch {}
  process.stderr.write("@yoooclaw/cli: installed; current Relay owner preserved\n");
  process.exit(0);
}

function resolveBinary() {
  const pkg = `@yoooclaw/cli-${process.platform}-${process.arch}`;
  const binName = process.platform === "win32" ? "yc.exe" : "yc";
  try {
    return require.resolve(`${pkg}/bin/${binName}`);
  } catch {
    return null;
  }
}

const bin = resolveBinary();
if (!bin) {
  process.stderr.write(
    "@yoooclaw/cli: native binary is unavailable; cannot restore Relay owner\n",
  );
  process.exit(1);
}

function run(args) {
  const result = spawnSync(bin, args, {
    stdio: "inherit",
    env: process.env,
    windowsHide: true,
  });
  if (result.error || result.status !== 0) {
    process.exit(typeof result.status === "number" ? result.status : 1);
  }
}

if (activationRequested) {
  const args = ["owner", "activate", "cli", "--format", "json"];
  if (process.env.HERMES_PROFILE) {
    args.push("--hermes-profile", process.env.HERMES_PROFILE);
  }
  run(args);
} else {
  for (const profile of runningProfiles) {
    run(["--profile", profile, "daemon", "start", "--format", "json"]);
  }
}

try {
  fs.unlinkSync(statePath);
} catch {}
