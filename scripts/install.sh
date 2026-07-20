#!/usr/bin/env sh
# @yoooclaw/cli install script
#
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/YoooClaw/cli/master/scripts/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- --version 0.0.5
#   curl -fsSL .../install.sh | sh -s -- --dir ~/bin
#
# 选项：
#   --version <v>   指定版本（默认最新 stable）
#   --beta          未指定 --version 时安装最新预发布版
#   --dir <path>    安装目录（默认 ~/.local/bin，已在 PATH 则用之；否则提示）
#   --force         覆盖已存在二进制
#
# 行为：
#   - 检测 OS/Arch，下载 yoooclaw-<os>-<arch>（OSS 渲染版从 OSS 下载，源码版从 GitHub Release）
#   - 校验 sha256（从同目录 checksums.txt 取）
#   - 写入 <dir>/yoooclaw，并 symlink yc -> yoooclaw

set -eu

# 以下两个占位符由 tools/oss-upload 在发布时渲染。渲染后的脚本从阿里云 OSS
# 解析版本并下载制品；未渲染的源码原版继续走 GitHub API + Release。
OSS_BASE_URL="__YOOOCLAW_CLI_OSS_BASE_URL__"
OSS_RENDERED="__YOOOCLAW_CLI_TEMPLATE_RENDERED__"

REPO="YoooClaw/cli"
VERSION=""
INSTALL_DIR=""
FORCE=0
BETA=0

err() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33mwarn:\033[0m %s\n' "$*" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:?--version 需要值}"; shift 2 ;;
    --beta)    BETA=1; shift ;;
    --dir)     INSTALL_DIR="${2:?--dir 需要值}"; shift 2 ;;
    --force)   FORCE=1; shift ;;
    -h|--help)
      sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) err "未知参数: $1" ;;
  esac
done

# ---------- detect platform ----------
uname_s=$(uname -s)
uname_m=$(uname -m)

case "$uname_s" in
  Darwin) OS="darwin" ;;
  Linux)  OS="linux"  ;;
  *) err "暂不支持的操作系统: $uname_s（一阶段仅 darwin/linux，windows 走 npm i -g @yoooclaw/cli）" ;;
esac

case "$uname_m" in
  arm64|aarch64) ARCH="arm64" ;;
  x86_64|amd64)  ARCH="x64"   ;;
  *) err "暂不支持的架构: $uname_m" ;;
esac

ASSET="yoooclaw-${OS}-${ARCH}"

# ---------- resolve version ----------
need_cmd() { command -v "$1" >/dev/null 2>&1 || err "缺少依赖: $1"; }
need_cmd curl

if [ "$OSS_RENDERED" = "1" ]; then
  # OSS 渲染版：latest / beta 标记文件里存的是纯版本号
  if [ -z "$VERSION" ]; then
    CHANNEL="latest"
    [ "$BETA" -eq 1 ] && CHANNEL="beta"
    info "查询 ${CHANNEL} 版本…"
    VERSION=$(curl -fsSL "${OSS_BASE_URL}/${CHANNEL}" 2>/dev/null | tr -d '[:space:]' || true)
    [ -z "$VERSION" ] && err "无法从 ${OSS_BASE_URL}/${CHANNEL} 解析版本（请用 --version 显式指定）"
  fi
  BASE="${OSS_BASE_URL}/v${VERSION}"
else
  if [ -z "$VERSION" ]; then
    info "查询最新 release tag…"
    # 优先用 GitHub API；不带 token 也能匿名用，但有 60 req/h 限制
    api_url="https://api.github.com/repos/${REPO}/releases"
    if [ "$BETA" -eq 1 ]; then
      latest_tag=$(curl -fsSL "$api_url" \
        | grep -E '"tag_name":\s*"cli-v' \
        | head -1 \
        | sed -E 's/.*"tag_name":\s*"(cli-v[^"]+)".*/\1/')
    else
      latest_tag=$(curl -fsSL "$api_url" \
        | grep -E '"tag_name":\s*"cli-v' \
        | grep -v 'beta\|alpha\|rc' \
        | head -1 \
        | sed -E 's/.*"tag_name":\s*"(cli-v[^"]+)".*/\1/')
    fi
    [ -z "$latest_tag" ] && err "无法解析最新 tag（API 限流？请用 --version 显式指定）"
    VERSION="${latest_tag#cli-v}"
  fi
  BASE="https://github.com/${REPO}/releases/download/cli-v${VERSION}"
fi

info "目标 ${ASSET}  版本 ${VERSION}"

# ---------- resolve install dir ----------
if [ -z "$INSTALL_DIR" ]; then
  if echo "${PATH:-}" | tr ':' '\n' | grep -qx "$HOME/.local/bin"; then
    INSTALL_DIR="$HOME/.local/bin"
  elif [ -w "/usr/local/bin" ]; then
    INSTALL_DIR="/usr/local/bin"
  else
    INSTALL_DIR="$HOME/.local/bin"
    warn "$INSTALL_DIR 不在 PATH 中；安装后请把它加进 PATH"
  fi
fi
mkdir -p "$INSTALL_DIR"

TARGET="$INSTALL_DIR/yoooclaw"
if [ -e "$TARGET" ] && [ "$FORCE" -ne 1 ]; then
  err "$TARGET 已存在；用 --force 覆盖，或先卸载旧版本"
fi

# ---------- download + verify ----------
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

info "下载 ${BASE}/${ASSET}"
curl -fL --progress-bar -o "$TMP/$ASSET" "${BASE}/${ASSET}"

info "下载 checksums.txt"
if curl -fsSL -o "$TMP/checksums.txt" "${BASE}/checksums.txt"; then
  expected=$(grep " $ASSET\$" "$TMP/checksums.txt" | awk '{print $1}')
  [ -z "$expected" ] && err "checksums.txt 中未找到 $ASSET"
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$TMP/$ASSET" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$TMP/$ASSET" | awk '{print $1}')
  else
    warn "找不到 sha256sum/shasum，跳过校验"
    actual="$expected"
  fi
  [ "$actual" = "$expected" ] || err "sha256 不匹配：期望 $expected 实得 $actual"
  info "sha256 ✓ $actual"
else
  warn "未找到 checksums.txt，跳过校验"
fi

# ---------- install ----------
chmod +x "$TMP/$ASSET"
mv "$TMP/$ASSET" "$TARGET"

YC_LINK="$INSTALL_DIR/yc"
if [ -L "$YC_LINK" ] || [ -e "$YC_LINK" ]; then
  [ "$FORCE" -eq 1 ] && rm -f "$YC_LINK"
fi
if [ ! -e "$YC_LINK" ]; then
  ln -s "$TARGET" "$YC_LINK" || cp "$TARGET" "$YC_LINK"
fi

info "已安装: $TARGET"
info "        $YC_LINK -> yoooclaw"

# ---------- verify ----------
if "$TARGET" --version >/dev/null 2>&1; then
  installed_version=$("$TARGET" --version)
  info "yoooclaw $installed_version ready"
else
  warn "已写入文件但 --version 执行失败"
fi

case ":${PATH:-}:" in
  *:"$INSTALL_DIR":*) ;;
  *) warn "请把 $INSTALL_DIR 加到 PATH，例如：echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.zshrc" ;;
esac
