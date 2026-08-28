#!/usr/bin/env sh
# YoooClaw CLI unattended installer for Alibaba Cloud WUYING desktops.
#
# Usage:
#   curl -fsSL https://artifact.yoooclaw.com/cli/install-wuying.sh \
#     | sh -s -- --api-key "$YOOOCLAW_API_KEY" --skill claude
#
# Required options:
#   --api-key <key>  Account API key. It is written to credentials.json via stdin.
#   --skill <agent>  Skill host passed to `yoooclaw skills install --agent`.
#                    Current CLI releases support claude and codex. Future adapters
#                    (for example DeepSeek Harness) work without changing this script.
#
# Optional options:
#   --version <v>    Install a specific CLI version (default: latest stable).
#   --beta           Install the latest prerelease when --version is omitted.
#   --dir <path>     Binary directory (same default as install.sh).
#   --force          Replace an existing binary and refresh installed Skills.
#   --modify-path    Add the binary directory to the shell profile.
#   --no-modify-path Do not edit the shell profile (default).
#   --profile <name> Configure and start this CLI profile (default: default).
#
# This script installs the native CLI, writes the API key, initializes a default
# non-interactive config when needed, installs bundled Skills for the selected
# host, takes standalone ownership, and starts the daemon. It prefers login
# autostart and falls back to a detached daemon when a user service manager is
# unavailable.

set -eu

OSS_BASE_URL="__YOOOCLAW_CLI_OSS_BASE_URL__"
OSS_RENDERED="__YOOOCLAW_CLI_TEMPLATE_RENDERED__"

API_KEY=""
SKILL_AGENT=""
VERSION=""
INSTALL_DIR=""
PROFILE="default"
BETA=0
FORCE=0
MODIFY_PATH=0

err() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33mwarn:\033[0m %s\n' "$*" >&2; }

usage() {
  sed -n '2,27p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --api-key) API_KEY="${2:?--api-key 需要值}"; shift 2 ;;
    --skill) SKILL_AGENT="${2:?--skill 需要值}"; shift 2 ;;
    --version) VERSION="${2:?--version 需要值}"; shift 2 ;;
    --beta) BETA=1; shift ;;
    --dir) INSTALL_DIR="${2:?--dir 需要值}"; shift 2 ;;
    --force) FORCE=1; shift ;;
    --modify-path) MODIFY_PATH=1; shift ;;
    --no-modify-path) MODIFY_PATH=0; shift ;;
    --profile) PROFILE="${2:?--profile 需要值}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) err "未知参数: $1" ;;
  esac
done

[ -n "$(printf '%s' "$API_KEY" | tr -d '[:space:]')" ] || err "--api-key 必填且不能为空"
[ -n "$(printf '%s' "$SKILL_AGENT" | tr -d '[:space:]')" ] || err "--skill 必填且不能为空（例如 claude 或 codex）"
[ -n "$(printf '%s' "$PROFILE" | tr -d '[:space:]')" ] || err "--profile 不能为空"

command -v curl >/dev/null 2>&1 || err "缺少依赖: curl"

TMP=$(mktemp -d)
cleanup() {
  cleanup_status=$?
  trap - EXIT
  API_KEY=""
  rm -rf "$TMP"
  exit "$cleanup_status"
}
trap cleanup EXIT

if [ "$OSS_RENDERED" = "1" ]; then
  BASE_INSTALLER_URL="${OSS_BASE_URL}/install.sh"
else
  BASE_INSTALLER_URL="https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install.sh"
fi

info "下载 YoooClaw CLI 基础安装器…"
curl -fsSL -o "$TMP/install.sh" "$BASE_INSTALLER_URL"

set -- "$TMP/install.sh"
[ -n "$VERSION" ] && set -- "$@" --version "$VERSION"
[ "$BETA" -eq 1 ] && set -- "$@" --beta
[ -n "$INSTALL_DIR" ] && set -- "$@" --dir "$INSTALL_DIR"
[ "$FORCE" -eq 1 ] && set -- "$@" --force
if [ "$MODIFY_PATH" -eq 1 ]; then
  set -- "$@" --modify-path
else
  set -- "$@" --no-modify-path
fi
sh "$@"

if [ -n "$INSTALL_DIR" ]; then
  EFFECTIVE_INSTALL_DIR="$INSTALL_DIR"
else
  case ":${PATH:-}:" in
    *:"$HOME/.local/bin":*) EFFECTIVE_INSTALL_DIR="$HOME/.local/bin" ;;
    *)
      if [ -w "/usr/local/bin" ]; then
        EFFECTIVE_INSTALL_DIR="/usr/local/bin"
      else
        EFFECTIVE_INSTALL_DIR="$HOME/.local/bin"
      fi
      ;;
  esac
fi
CLI="$EFFECTIVE_INSTALL_DIR/yoooclaw"
[ -x "$CLI" ] || err "yoooclaw 不可执行: $CLI"

yc() {
  "$CLI" --profile "$PROFILE" "$@"
}

info "写入 account API key…"
printf '%s\n' "$API_KEY" | yc auth set-api-key - >/dev/null
API_KEY=""

if yc --format json config show >/dev/null 2>&1; then
  info "profile ${PROFILE} 已初始化，保留现有配置"
else
  info "初始化 profile ${PROFILE}…"
  printf '{}\n' > "$TMP/config.json"
  yc config init --non-interactive --from-file "$TMP/config.json" \
    --no-start --no-autostart >/dev/null
fi

info "安装 ${SKILL_AGENT} 宿主的 Agent Skills…"
skill_args=""
[ "$FORCE" -eq 1 ] && skill_args="--force"
if [ -n "$skill_args" ]; then
  yc skills install --agent "$SKILL_AGENT" --force >/dev/null
else
  yc skills install --agent "$SKILL_AGENT" >/dev/null
fi

info "切换到 standalone CLI owner…"
yc owner activate cli --no-start >/dev/null

info "启动 daemon 并配置登录自启…"
if yc daemon autostart enable >/dev/null; then
  info "daemon 已启动，并已启用登录自启"
else
  warn "当前环境无法配置用户级自启，改用 detached daemon"
  if yc daemon status >/dev/null 2>&1; then
    info "daemon 已在运行"
  else
    yc daemon start >/dev/null || err "daemon 启动失败；请运行: $CLI --profile $PROFILE daemon logs"
    info "daemon 已以 detached 模式启动"
  fi
fi

yc daemon status >/dev/null 2>&1 || err "安装完成，但 daemon 状态检查失败；请运行: $CLI --profile $PROFILE daemon logs"

info "无影云电脑初始化完成"
info "CLI: $CLI"
info "profile: $PROFILE"
info "Skill host: $SKILL_AGENT"
