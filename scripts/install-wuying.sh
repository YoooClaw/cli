#!/usr/bin/env sh
# YoooClaw CLI unattended installer for Alibaba Cloud WUYING desktops.
#
# Usage:
#   curl -fsSL https://artifact.yoooclaw.com/cli/install-wuying.sh \
#     | sh -s -- --api-key "$YOOOCLAW_API_KEY" --skill claude --env production
#
# Required options:
#   --api-key <key>  Account API key. It is written to credentials.json via stdin.
#   --skill <agent>  Skill host passed to `yoooclaw skills install --agent`.
#                    Current CLI releases support claude and codex. Future adapters
#                    (for example DeepSeek Harness) work without changing this script.
#
# Optional options:
#   --version <v>    Install a specific CLI version (default: this script's release,
#                    which is always a stable one). Prereleases are only
#                    installable by naming them here explicitly.
#   --dir <path>     Binary directory (same default as install.sh).
#   --force          Replace an existing binary and refresh installed Skills.
#   --modify-path    Add the binary directory to the shell profile.
#   --no-modify-path Do not edit the shell profile (default).
#   --profile <name> Configure and start this CLI profile (default: default).
#   --env <name>     Cloud environment: development|test|production.
#                    Defaults to PHONE_NOTIFICATIONS_ENV, then production.
#
# This script installs the native CLI, writes the API key, initializes a default
# non-interactive config when needed, installs bundled Skills for the selected
# host, takes standalone ownership, and starts the daemon. It prefers login
# autostart and falls back to a detached daemon when a user service manager is
# unavailable.

set -eu

OSS_BASE_URL="__YOOOCLAW_CLI_OSS_BASE_URL__"
OSS_RENDERED="__YOOOCLAW_CLI_TEMPLATE_RENDERED__"
RELEASE_VERSION="__YOOOCLAW_CLI_RELEASE_VERSION__"

API_KEY=""
SKILL_AGENT=""
VERSION=""
INSTALL_DIR=""
PROFILE="default"
CLOUD_ENV="${PHONE_NOTIFICATIONS_ENV:-production}"
FORCE=0
MODIFY_PATH=0

err() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33mwarn:\033[0m %s\n' "$*" >&2; }

usage() {
  sed -n '2,29p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --api-key) API_KEY="${2:?--api-key 需要值}"; shift 2 ;;
    --skill) SKILL_AGENT="${2:?--skill 需要值}"; shift 2 ;;
    --version) VERSION="${2:?--version 需要值}"; shift 2 ;;
    --beta) err "beta 版必须显式指定版本号，例如 --version 0.10.0-beta.3" ;;
    --dir) INSTALL_DIR="${2:?--dir 需要值}"; shift 2 ;;
    --force) FORCE=1; shift ;;
    --modify-path) MODIFY_PATH=1; shift ;;
    --no-modify-path) MODIFY_PATH=0; shift ;;
    --profile) PROFILE="${2:?--profile 需要值}"; shift 2 ;;
    --env) CLOUD_ENV="${2:?--env 需要值}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) err "未知参数: $1" ;;
  esac
done

[ -n "$(printf '%s' "$API_KEY" | tr -d '[:space:]')" ] || err "--api-key 必填且不能为空"
[ -n "$(printf '%s' "$SKILL_AGENT" | tr -d '[:space:]')" ] || err "--skill 必填且不能为空（例如 claude 或 codex）"
[ -n "$(printf '%s' "$PROFILE" | tr -d '[:space:]')" ] || err "--profile 不能为空"
case "$CLOUD_ENV" in
  development) CLOUD_HOST="openclaw-service-dev.yoooclaw.com" ;;
  test) CLOUD_HOST="openclaw-service-test.yoooclaw.com" ;;
  production) CLOUD_HOST="openclaw-service.yoooclaw.com" ;;
  *) err "--env 仅支持 development、test 或 production" ;;
esac
PHONE_NOTIFICATIONS_ENV="$CLOUD_ENV"
export PHONE_NOTIFICATIONS_ENV

command -v curl >/dev/null 2>&1 || err "缺少依赖: curl"

configure_user_service_env() {
  [ "$(uname -s)" = "Linux" ] || return 0
  command -v id >/dev/null 2>&1 || return 0
  user_runtime_dir="/run/user/$(id -u)"
  if [ -z "${XDG_RUNTIME_DIR:-}" ] && [ -d "$user_runtime_dir" ]; then
    XDG_RUNTIME_DIR="$user_runtime_dir"
    export XDG_RUNTIME_DIR
  fi
  if [ -z "${DBUS_SESSION_BUS_ADDRESS:-}" ] && [ -S "${XDG_RUNTIME_DIR:-}/bus" ]; then
    DBUS_SESSION_BUS_ADDRESS="unix:path=${XDG_RUNTIME_DIR}/bus"
    export DBUS_SESSION_BUS_ADDRESS
  fi
}

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

configure_user_service_env

# Stop an older managed/detached installation before replacing its executable.
# Otherwise the generic installer may restore that runtime mid-upgrade and the
# WUYING owner handoff sees its Relay lock as a foreign owner.
if [ -x "$CLI" ]; then
  info "停止旧版 daemon/autostart…"
  if ! "$CLI" --profile "$PROFILE" daemon autostart disable; then
    err "无法停止旧版 daemon/autostart；未覆盖正在使用的 CLI"
  fi
fi

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
  if [ -z "$VERSION" ]; then
    VERSION="$RELEASE_VERSION"
  fi
else
  BASE_INSTALLER_URL="https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install.sh"
fi

info "下载 YoooClaw CLI 基础安装器…"
curl -fsSL -o "$TMP/install.sh" "$BASE_INSTALLER_URL"

set -- "$TMP/install.sh"
[ -n "$VERSION" ] && set -- "$@" --version "$VERSION"
[ -n "$INSTALL_DIR" ] && set -- "$@" --dir "$INSTALL_DIR"
[ "$FORCE" -eq 1 ] && set -- "$@" --force
if [ "$MODIFY_PATH" -eq 1 ]; then
  set -- "$@" --modify-path
else
  set -- "$@" --no-modify-path
fi
sh "$@"

[ -x "$CLI" ] || err "yoooclaw 不可执行: $CLI"

yc() {
  "$CLI" --profile "$PROFILE" "$@"
}

wait_for_daemon() {
  attempts="$1"
  while [ "$attempts" -gt 0 ]; do
    if yc daemon status >/dev/null 2>&1; then
      return 0
    fi
    attempts=$((attempts - 1))
    [ "$attempts" -gt 0 ] && sleep 1
  done
  return 1
}

info "写入 account API key…"
printf '%s\n' "$API_KEY" | yc auth set-api-key -
API_KEY=""

if yc --format json config show >/dev/null 2>&1; then
  info "profile ${PROFILE} 已初始化，保留现有配置"
else
  info "初始化 profile ${PROFILE}…"
  printf '{}\n' > "$TMP/config.json"
  yc config init --non-interactive --from-file "$TMP/config.json" \
    --no-start --no-autostart
fi

info "持久化云端环境: ${CLOUD_ENV}…"
yc config set cloud.host "$CLOUD_HOST"
yc config set relay.url "wss://${CLOUD_HOST}/message/messages/ws/plugin"

info "安装 ${SKILL_AGENT} 宿主的 Agent Skills…"
skill_args=""
[ "$FORCE" -eq 1 ] && skill_args="--force"
if [ -n "$skill_args" ]; then
  yc skills install --agent "$SKILL_AGENT" --force
else
  yc skills install --agent "$SKILL_AGENT"
fi

info "停止并清理旧 daemon/autostart…"
if ! yc daemon autostart disable; then
  err "无法清理旧 daemon/autostart；为避免双 Relay owner，安装已安全中止"
fi

info "切换到 standalone CLI owner…"
if ! yc owner activate cli --no-start; then
  err "standalone CLI owner 交接失败；请根据上方原始诊断处理"
fi

info "启动 daemon 并配置登录自启…"
if yc daemon autostart enable; then
  info "daemon 已启动，并已启用登录自启"
elif wait_for_daemon 10; then
  warn "autostart 命令返回失败，但 daemon 已确认运行；保留当前托管状态"
else
  warn "当前环境无法可靠配置用户级自启，正在回滚并改用 detached daemon"
  if ! yc daemon autostart disable; then
    err "autostart 失败后无法完成回滚；未继续启动第二个 daemon"
  fi
  if yc daemon start; then
    info "daemon 已以 detached 模式启动"
  elif wait_for_daemon 5; then
    warn "daemon start 返回失败，但已确认 daemon 正在运行"
  else
    err "daemon 启动失败；请运行: $CLI --profile $PROFILE daemon logs"
  fi
fi

if ! wait_for_daemon 5; then
  err "安装完成，但 daemon 状态检查失败；请运行: $CLI --profile $PROFILE daemon logs"
fi
yc daemon status --format json

info "无影云电脑初始化完成"
info "CLI: $CLI"
info "profile: $PROFILE"
info "Skill host: $SKILL_AGENT"
info "Cloud environment: $CLOUD_ENV"
