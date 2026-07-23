# @yoooclaw/cli

English | [简体中文](README.zh-CN.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![npm version](https://img.shields.io/npm/v/@yoooclaw/cli.svg)](https://www.npmjs.com/package/@yoooclaw/cli)
[![Go native](https://img.shields.io/badge/native-Go-00ADD8.svg)](https://go.dev/)

yoooclaw is a standalone CLI — **it ships its own background daemon and does not depend on the openclaw client being online** — built for both humans and AI agents. It receives phone notifications locally, manages the Relay tunnel, handles recording ASR and light-effect rule evaluation, exposes unified `--format` output, and ships with an Agent Skill that works out of the box.

Service-oriented command tree, a three-tier command system, Agent-Native.

[Install](#installation--quickstart) · [Agent Skills](#agent-skills) · [Auth](#authentication) · [Commands](#three-tier-command-system) · [Advanced](#advanced-usage) · [Security](#security--risk-notice) · [Contributing](#development--contributing)

## Why yoooclaw?

- **Ships its own daemon** — a local daemon receives notifications, evaluates rules, and connects to Relay, independent of whether the openclaw client is online
- **Agent-Native** — the bundled [Skill](skills/) works out of the box; agents can call `yoooclaw` commands with zero extra config
- **Three-tier command system** — Shortcuts (human/AI friendly) → Service Commands (structured) → Raw API (full coverage), pick the granularity you need
- **Disk-only queries** — notification / recording / image queries read directly from `~/.yoooclaw`, no daemon required
- **Unified output contract** — `--format json|pretty|table|ndjson`, success and failure share one channel with predictable structure; local CLI errors return a non-zero exit code, and Raw/daemon HTTP responses should be checked against both `ok` and HTTP status
- **Credential security** — OS keychain storage by default, multi api-key management, gateway token auth for local ingest
- **Native Go binary** — a thin npm launcher + platform subpackages, or install the native binary directly; full macOS / Linux / Windows support

## Capabilities at a Glance

| Domain                    | Capabilities                                                                                             | daemon |
| -------------------------- | ---------------------------------------------------------------------------------------------------------- | ------ |
| 📱 Notification            | Query by time/app/sender/keyword, today/recent summaries, multi-dimension aggregate stats, chunked summarization for large batches | 🟢     |
| 🔄 Sync                     | Scan/iterate unprocessed notifications, fetch details by date, commit batches — feeds memory systems       | 🟢     |
| 🎙️ Recording                | List/query recordings, ASR transcription config (api/model-proxy; local mode deprecated), follow status event stream | 🟢     |
| 🖼️ Image                    | List/query images, resolve local paths / thumbnails                                                        | 🟢     |
| 💡 Light                    | Send light-effect commands to hardware (segment / preset / rule — pick one), connectivity self-check       | 🟡     |
| 📐 Lightrule                | CRUD for persistent "notification → light effect" rules, enable / disable                                  | 🟡     |
| ⏰ Monitor                  | cron-driven scheduled notification monitoring jobs                                                          | 🟡     |
| 🔌 Tunnel                   | Relay tunnel status, force reconnect, local ingest loopback self-check                                      | 🟡     |
| 🛡️ Gateway                  | Simulate phone-side calls into the daemon, verify local connectivity & auth                                 | 🟢/🟡  |
| 📋 Log                      | daemon log search & error-level filtering                                                                   | 🟢     |
| ⚙️ Infrastructure           | config / profile / auth / daemon / migrate / update / doctor / uninstall                                    | 🟢/🔵  |
| 🧩 Skills                   | Install bundled SKILL.md into the agent's discovery directory                                               | 🟢     |

> daemon legend: 🟢 no daemon needed · 🟡 daemon must be running · 🔵 manages the daemon itself.

## Installation & Quickstart

Two distribution channels with **identical functionality** — pick whichever fits. The npm package is now a very thin Node launcher; the actual work is done by the native Go binary installed via optionalDependencies. The direct-install channel skips Node entirely and downloads the same Go binary.

**Platform support**: the npm channel supports `darwin/linux` `x64+arm64` and `win32-x64` (the launcher needs Node ≥ 18); `install.sh` / GitHub Release direct install supports `darwin/linux` `x64+arm64`. OpenHarmony/HarmonyOS PC requires the host application to install the native CLI as an HNP embedded in its signed HAP; an app sandbox cannot execute an ELF downloaded only through `npm i -g`. See "OpenHarmony / HarmonyOS PC" below. On Windows, credentials are stored in plaintext at `~/.yoooclaw/credentials.json` (no OS keychain hardening — `yoooclaw doctor` will warn about this), and the daemon shuts down gracefully over HTTP.

### Channel A — npm (thin Node launcher + platform Go binary)

```bash
# No install needed (always pulls the latest version)
npx @yoooclaw/cli --help

# Global install (provides both the yoooclaw and yc commands)
npm i -g @yoooclaw/cli
yoooclaw --help
yc --help
```

### Channel B — Native binary (no Node required)

A single-file Go executable — lighter cold-start and resource footprint than the older TS/Bun implementation.

```bash
# Auto-detect platform, download, verify sha256, install to ~/.local/bin
curl -fsSL https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install.sh | sh

# Pin a version / install directory / force overwrite
curl -fsSL https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install.sh \
  | sh -s -- --version 0.2.0-beta.2 --dir ~/bin --force
```

Direct-install supported platforms: `darwin-arm64` / `darwin-x64` / `linux-x64` / `linux-arm64`. The Windows Go binary currently ships only via the npm platform subpackage. You can also download manually from [GitHub Releases](https://github.com/YoooClaw/cli/releases?q=cli-v) (verify against the `checksums.txt` in the same release).

> `yoooclaw update self` gives you the right upgrade command for how you installed (npm → `npm update -g`, binary → install.sh).

### OpenHarmony / HarmonyOS PC

The application sandbox rejects native ELF files downloaded into app data by npm. Changing file mode, copying the Linux ARM64 optional package, or mapping `process.platform` to `linux` does not bypass that policy. The supported delivery path is an OpenHarmony Native Package (HNP) embedded and signed into the WorkBuddy/OpenClaw host HAP.

Build the `openharmony/arm64` binary with the OpenHarmony-SIG Go toolchain and pack it with the SDK's `hnpcli`:

```bash
OHOS_GO=/path/to/ohos_golang_go/bin/go \
OHOS_HNPCLI=/path/to/openharmony-sdk/toolchains/hnpcli \
scripts/build-openharmony.sh
```

The result is `dist-openharmony/hnp/arm64-v8a/yoooclaw.hnp`. Copy `dist-openharmony/hnp` into the HAP project root and declare it under the module in `entry/src/main/module.json5`:

```json5
"hnpPackages": [
  {
    "package": "yoooclaw.hnp",
    "type": "private",
    "independentSign": false
  }
]
```

The npm launcher discovers the versioned private HNP path automatically. A host can also set `YOOOCLAW_NATIVE_BIN` to the installed `bin/yoooclaw` path. Merely downloading the `.hnp` into a user directory does not install it; it must be packaged and signed with the HAP.

For the complete WorkBuddy host integration procedure, see [docs/workbuddy-openharmony.md](docs/workbuddy-openharmony.md).

### Quickstart (Human Users)

```bash
# 1. Interactive first-run wizard: generates config + gateway token and starts the daemon
yoooclaw config init

# 2. Check auth and environment readiness
yoooclaw auth status
yoooclaw doctor

# 3. Start using it: notification queries read straight from disk, no daemon required
yoooclaw notification +today
```

### Quickstart (AI Agent)

> The steps below are for AI agents; some may need a human to confirm in the terminal. Query commands are disk-only; control commands require the daemon to be running.

```bash
# 1. Initialize via stdin config injection (no interactive prompts), which also starts the daemon
yoooclaw config init --non-interactive --from-file -

# 2. Confirm the daemon is running and the tunnel is connected
yoooclaw daemon status
yoooclaw tunnel status

# 3. Stream notification queries (always based on the latest disk data — don't rely on memory)
yoooclaw notification summary --app WeChat --from 2026-06-01T00:00:00+08:00 --format json

# 4. Install the bundled Skill into the current agent's discovery directory
yoooclaw skills install
```

## Agent Skills

> **Recommended pairing: [YoooClaw/skills](https://github.com/YoooClaw/skills).** The `yoooclaw-cli` Skill distributed from that repo packages command routing, the `--format` output contract, daemon dependencies, and error handling into an agent-facing "instruction manual", so Codex / Claude Code and similar agents understand how to call `yoooclaw` out of the box. Install the CLI first, then the Skill:

```bash
# 1. Install the CLI (prerequisite)
npm install -g @yoooclaw/cli

# 2. Install the yoooclaw-cli Skill — Codex + Claude Code
npx skills@latest add YoooClaw/skills --skill yoooclaw-cli --global --agent codex --agent claude-code --copy --yes

# Install for just one agent: pass a single --agent
npx skills@latest add YoooClaw/skills --skill yoooclaw-cli --global --agent claude-code --copy --yes
```

> For Hermes Agent, use `hermes skills install https://raw.githubusercontent.com/YoooClaw/skills/main/yoooclaw-cli/SKILL.md`. Restart the agent session after installing so it can be discovered.

### Built-in Skill (bundled in this repo)

This repo ships a SKILL.md under [skills/](skills/) that teaches agents to call `yoooclaw` commands directly. In the openclaw plugin it is auto-registered via `openclaw.plugin.json`; in the standalone CLI form, use `yoooclaw skills install` to symlink it into the agent's skills discovery directory.

| Skill                          | Description                                                                                                                                |
| ------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `yoooclaw-notification-query`  | Query/aggregate/summarize phone notifications: "show me recent notifications", "who's contacted me", "summarize the last N notifications". Small batches use `summary`, large batches use `summary-job`; disk-only, no daemon required |
| `yoooclaw-lightrule-create`    | Create/manage persistent "notification → light effect" rules from natural language; rules are compiled, stored, and evaluated by the cloud Notification Intelligence Service |
| `yoooclaw-tunnel-debug`        | Debug the phone-side push path: combine auth / daemon / tunnel / gateway status to pinpoint local config, ingest auth, and Relay WebSocket issues (🟡) |

```bash
yoooclaw skills list                 # List Skills bundled with this package
yoooclaw skills targets              # View supported agent targets and detection results
yoooclaw skills install              # Auto-detect the sole agent and symlink-install
yoooclaw skills install --agent claude
yoooclaw skills install --copy       # Copy instead of symlink (use on Windows without admin rights)
```

Symlinking is the default rather than copying: after `yoooclaw update self` upgrades the CLI, the Skill content updates automatically along with it. Restart the agent session after installing so it can be discovered.

## Authentication

yoooclaw's authentication revolves around two kinds of credentials: **api-key** (account-level, signs phone-side upstream ingest) and **gateway token** (local daemon HTTP auth). Most commands are local checks (🟢); `auth check` performs an end-to-end call to the daemon (🟡).

| Command                             | Description                                                       |
| ------------------------------------ | ------------------------------------------------------------------- |
| `auth set-api-key <key>`             | Set/rotate the account-level default api-key (`-` reads from stdin) |
| `auth add-api-key <key>`             | Add a multi-key api-key entry, optionally with `--label` / `--default` |
| `auth list-api-keys`                 | List api-key entries (keys are automatically masked)                |
| `auth set-default-api-key <label>`   | Switch the default api-key                                          |
| `auth remove-api-key <label>`        | Remove the api-key entry with the given label                       |
| `auth token-rotate`                  | Generate a new gateway token; restart the daemon afterward if it's running |
| `auth status`                        | Show auth status (local check, does not call the daemon)            |
| `auth check`                         | End-to-end auth health check (calls daemon `/daemon/status`)        |

```bash
# Write the default api-key securely from stdin, stored in the OS keychain
echo "ock-xxxx" | yoooclaw auth set-api-key - --keychain

# Multi-device/multi-client: manage multiple api-keys by label
yoooclaw auth add-api-key - --label phone-a --default
yoooclaw auth list-api-keys

# Rotate the gateway token, then restart the daemon so it takes effect
yoooclaw auth token-rotate
yoooclaw daemon restart
```

## Daemon lifecycle protocol

`yoooclaw daemon` exposes lifecycle metadata so external orchestrators such as
the Hermes plugin can keep the daemon and their websocket connections in the
same generation.

```bash
yoooclaw daemon run-foreground --owner hermes-plugin --generation abc123
yoooclaw daemon stop --owner hermes-plugin --generation abc123 --wait
yoooclaw daemon status --format json
```

`daemon status` includes `pid`, `version`, `executable`, `profile`,
`relay.env`, and a nested lifecycle object:

```json
{
  "ok": true,
  "version": "0.3.0",
  "executable": "/Users/me/.yoooclaw/hermes-plugin/bin/0.3.0/yoooclaw",
  "profile": "test",
  "lifecycle": {
    "owner": "hermes-plugin",
    "generation": "abc123",
    "startedAt": "2026-06-18T10:34:07Z"
  }
}
```

The lifecycle flags are optional; normal human `daemon start`, `stop`, and
`restart` usage remains unchanged. When supplied to `daemon stop`, owner and
generation are checked before the process is terminated.

### Ingress Modes (optional, proxyable daemon connection)

Once the "connection to the phone" is layered, `--ingress` selects the **single** owner, avoiding a double connection / double ingest when the standalone CLI and a host plugin (e.g. hermes-plugin) both try to connect to Relay. Priority order: `--ingress` flag > `YOOOCLAW_INGRESS` env var > `config.ingress.mode`, defaulting to `standalone`.

| Mode | Owner of the phone connection | Relay tunnel | Ingest auth | Outbound events |
| --- | --- | --- | --- | --- |
| `standalone` (default) | the Go daemon's own tunnel | enabled | gateway token / local | pushed back to the phone via Relay |
| `proxied` (embedded in a host plugin) | the host plugin proxies it | **off** | **api-key required** | POSTed back to the host callback URL |
| `direct` (LAN / testing) | the caller POSTs directly | off | api-key / token | discarded (disk write only) |

Under `proxied`, the daemon doesn't connect to the tunnel; it only exposes the ingest API (`POST /notifications` `/recordings`
`/images`, with `Authorization: Bearer <api-key>`) so the host can feed phone data in. Outbound events
(such as `recording.status`) are posted back to the host via `--egress-callback-url`, and the host forwards them to the phone.

```bash
# Embedded in a host: turn off the Go daemon's own tunnel, let the host proxy the connection and receive callback events
yoooclaw daemon run-foreground --ingress proxied \
  --egress-callback-url http://127.0.0.1:8765/yoooclaw/egress \
  --egress-callback-token <token>
```

`daemon status` output now includes an `ingressMode` field. See the full layering design at
[docs/design/ingress-layering.md](docs/design/ingress-layering.md).

## Three-Tier Command System

Ranging from quick shortcuts to fully custom calls, covering everyday operations up to any daemon endpoint:

### 1. Shortcuts

Prefixed with `+`, friendly to both humans and AI, with smart defaults and table output built in.

```bash
yoooclaw notification +today          # Today's notification summary
yoooclaw notification +recent         # Notifications from the last hour
yoooclaw recording +latest            # Details of the most recent recording
yoooclaw light +blink                 # Light-effect connectivity test (red-strobe-3)
yoooclaw lightrule +on                # Enable all light-effect rules
yoooclaw tunnel +test                 # Daemon local ingest + auth self-check
yoooclaw log +errors                  # Error-level logs since yesterday
```

Run `yoooclaw <service> --help` to see all shortcuts for a given service.

### 2. Service Commands

`yoooclaw <service> <subcommand> [...flags]` — structured access to each domain's capabilities; see the service list via `yoooclaw --help`.

```bash
yoooclaw notification search --app WeChat --keyword meeting --limit 50
yoooclaw notification stats --dim app --from 2026-05-26
yoooclaw notification summary-job create --from 2026-06-01T00:00:00+08:00 --chunk-size 150  # Chunked summarization for large notification batches: create→next→commit→result
yoooclaw recording list --status synced
yoooclaw recording setup-asr --mode api --language auto --non-interactive
yoooclaw recording setup-asr --mode api --language zh-TW --non-interactive   # Traditional Chinese / Taiwan hint
yoooclaw recording setup-asr --mode api --language zh-Hant --non-interactive # Traditional Chinese script hint
yoooclaw lightrule create --intent "Flash red when my boss messages me on WeChat"  # Compiled & stored by the cloud service
yoooclaw monitor create daily-standup --schedule "0 9 * * 1-5" --match-rules '{"keyword":"standup"}'
```

For ASR language hints, `auto` keeps provider-side detection. Use `zh-TW` for Traditional Chinese in a Taiwan context, or `zh-Hant` when only the Traditional Chinese script preference matters.

### 3. Raw API

`yoooclaw api <METHOD> <PATH> [--data ...]` reaches daemon HTTP endpoints directly, covering anything not wrapped by a service command.

```bash
yoooclaw api GET /daemon/status
yoooclaw api POST /images --data @image.json
echo '{"...":"..."}' | yoooclaw api POST /recordings --data -
```

## Advanced Usage

### Global flags

| flag                | Description                                                    |
| -------------------- | ------------------------------------------------------------------ |
| `--profile <name>`   | Switch profile (default: `default`)                                |
| `--format <fmt>`     | `json\|pretty\|table\|ndjson` (defaults to pretty on a TTY, json when piped) |
| `--quiet`             | Suppress progress logs, output only the final result               |
| `--no-color`          | Disable terminal colors                                            |

### Output Formats

```bash
--format json      # Full JSON (default when piped)
--format pretty    # Human-friendly formatted output (default on a TTY)
--format table     # Readable table
--format ndjson    # Newline-delimited JSON, convenient for line-by-line piping
```

### Output Contract

Success and failure share the same channel (stdout) with a predictable structure. Local CLI validation / runtime errors are additionally expressed via a non-zero exit code; Raw HTTP commands like `api` preserve the daemon's original response as much as possible, so scripts should check both `ok` and HTTP status:

```json
{
  "ok": false,
  "error": {
    "code": "YOOOCLAW_DAEMON_NOT_RUNNING",
    "message": "...",
    "hint": "..."
  }
}
```

Error codes share a uniform `YOOOCLAW_*` prefix (see [internal/errs/errors.go](internal/errs/errors.go)).

### Multiple Profiles

`--profile <name>` switches between accounts/devices; data is isolated under `~/.yoooclaw/profiles/<profile>/`.

```bash
yoooclaw profile list
yoooclaw profile create work
yoooclaw --profile work notification +today
```

### Recording & Relay

The standalone daemon uses a Go implementation of recording storage, the state machine, OSS download, and ASR scheduling, and receives the app/cloud's `recordings.result.write` (writing transcripts/summaries, optionally downloading audio) via `RelayClient + RelayDispatcher`.

```bash
yoooclaw recording events --since 1h --limit 50
yoooclaw recording events --id <recording-id> --watch
```

Recording config and events live under the current profile at `recordings/asr-config.json` and `recordings/state/events.jsonl` respectively.

### Data Directory

`~/.yoooclaw/` (can be overridden with `YOOOCLAW_HOME`, useful for testing / multiple instances). See the layout in [internal/paths/paths.go](internal/paths/paths.go) and the PRD's "Data Model" section.

## Security & Risk Notice

This tool can be invoked by AI agents to automate operations against the local daemon and the phone-side link, which carries inherent risks such as model hallucination, unpredictable execution, and prompt injection. Once authorized, the agent will act within your identity and authorization scope, which may lead to sensitive data exposure or unintended operations — use with caution.

To reduce risk, the tool enables multiple layers of protection by default: the daemon only listens on a local port, local ingest is authenticated via gateway token, credentials are stored in the OS keychain by default, and sensitive fields are masked in terminal output. **We strongly recommend against relaxing these default security settings**; doing so significantly increases risk, and you assume full responsibility for the consequences. Please fully understand the risks involved — using this tool constitutes voluntary acceptance of the associated responsibility.

## Development & Contributing

```bash
go test ./...
go vet ./...
scripts/build-go.sh --current
dist-native/yoooclaw-darwin-arm64 --help
```

Full documentation lives at [yc-docs/src/cli](https://github.com/YoooClaw/yc-docs/tree/master/src/cli). Issues and PRs are welcome; for larger changes, please open an issue to discuss first.

### Source Layout

| File / Directory                                     | Responsibility |
| ------------------------------------------------------ | -------------- |
| [cmd/yc/main.go](cmd/yc/main.go)                        | Go binary entry point |
| [internal/cli/root.go](internal/cli/root.go)            | cobra root, global flags, service command wiring |
| [internal/cli/handler.go](internal/cli/handler.go)      | handler wrapping, output and error rendering |
| [internal/output/output.go](internal/output/output.go)  | unified `--format` serialization |
| [internal/errs/errors.go](internal/errs/errors.go)      | `YOOOCLAW_*` error codes |
| [internal/paths/paths.go](internal/paths/paths.go)      | `~/.yoooclaw/` directory layout resolution |
| [internal/daemon/server.go](internal/daemon/server.go)  | daemon HTTP server, auth, Relay wiring |
| [internal/daemon/server_ingest.go](internal/daemon/server_ingest.go) | notifications / recordings / images ingest |
| [internal/relay/dispatcher.go](internal/relay/dispatcher.go) | in-process dispatch from inbound Relay frames to daemon HTTP/gateway |
| [internal/recording](internal/recording)                | recording OSS download, state machine, ASR, transcript storage |
| [internal/image](internal/image)                        | image OSS download and indexing |
| [internal/light](internal/light)                        | light-effect wire protocol, presets, sender |
| [internal/skills](internal/skills)                      | built-in Skill listing / installation into agent skills directories |

All release artifacts are generated from the Go source via `scripts/build-go.sh` (npm platform subpackages + native binaries); the earlier TypeScript implementation has been retired and remains available only in git history.

## License

MIT — see [LICENSE](LICENSE).
