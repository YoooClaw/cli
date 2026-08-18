#!/usr/bin/env node
"use strict";

// Record and drain only standalone daemons that are actually running before
// npm replaces the native package. Hermes-owned installs have no standalone
// daemon, so a normal npm update leaves Hermes and its Gateway untouched.

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const root = process.env.YOOOCLAW_HOME || path.join(os.homedir(), ".yoooclaw");
const profilesRoot = path.join(root, "profiles");
const statePath = path.join(root, ".cli-install-handoff.json");
const activationRequested = process.env.YOOOCLAW_ACTIVATE_OWNER === "cli";
let profiles = [];
try {
  profiles = fs
    .readdirSync(profilesRoot, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => entry.name);
} catch {
  profiles = ["default"];
}

function oldCLIFor(profile) {
  try {
    const lock = JSON.parse(
      fs.readFileSync(path.join(profilesRoot, profile, "daemon.lock"), "utf8"),
    );
    if (lock.executable && fs.existsSync(lock.executable)) {
      return lock.executable;
    }
  } catch {
    // Missing/stale lock is normal when Hermes owns the runtime.
  }
  return process.platform === "win32" ? "yoooclaw.cmd" : "yoooclaw";
}

const runningProfiles = [];
for (const profile of profiles) {
  const oldCLI = oldCLIFor(profile);
  const status = spawnSync(
    oldCLI,
    ["--profile", profile, "daemon", "status", "--format", "json"],
    { stdio: "ignore", env: process.env, windowsHide: true },
  );
  if (status.status !== 0) continue;

  const stopped = spawnSync(
    oldCLI,
    ["--profile", profile, "daemon", "stop", "--format", "json"],
    { stdio: "inherit", env: process.env, windowsHide: true },
  );
  if (stopped.error || stopped.status !== 0) {
    process.stderr.write(
      `@yoooclaw/cli: failed to stop old daemon for profile ${profile}\n`,
    );
    process.exit(1);
  }
  runningProfiles.push(profile);
}

fs.mkdirSync(root, { recursive: true, mode: 0o700 });
fs.writeFileSync(
  statePath,
  JSON.stringify({ activationRequested, runningProfiles, preparedAt: new Date().toISOString() }),
  { mode: 0o600 },
);
