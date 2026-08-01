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
#   --dir <path>    安装目录（默认优先 ~/.local/bin，其次为可写的 /usr/local/bin）
#   --force         覆盖已存在二进制
#   --modify-path   将安装目录写入 shell 配置（默认不修改）
#   --no-modify-path 不修改 shell 配置（默认行为，兼容旧调用）
#
# 行为：
#   - 检测 OS/Arch，下载 yoooclaw-<os>-<arch>（OSS 渲染版从 OSS 下载，源码版从 GitHub Release）
#   - 校验 sha256（从同目录 checksums.txt 取）
#   - 写入 <dir>/yoooclaw，并 symlink yc -> yoooclaw
#   - 默认不修改 shell 配置；仅传入 --modify-path 时幂等写入 PATH

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
MODIFY_PATH=0

err() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33mwarn:\033[0m %s\n' "$*" >&2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:?--version 需要值}"; shift 2 ;;
    --beta)    BETA=1; shift ;;
    --dir)     INSTALL_DIR="${2:?--dir 需要值}"; shift 2 ;;
    --force)   FORCE=1; shift ;;
    --modify-path) MODIFY_PATH=1; shift ;;
    --no-modify-path) MODIFY_PATH=0; shift ;;
    -h|--help)
      sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'
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
    api_url="https://api.github.com/repos/${REPO}/releases?per_page=100"
    # 只截取 tag_name 字段，再用 cut 取 JSON 字符串值。这里刻意不用
    # sed 的 \s：macOS 自带的 BSD sed 不支持它，解析失败时会把整行 JSON
    # 原样传下去，最终污染下载 URL。
    release_tags=$(curl -fsSL "$api_url" \
      | grep -Eo '"tag_name"[[:space:]]*:[[:space:]]*"cli-v[^"]+"' \
      | cut -d '"' -f 4 || true)
    if [ "$BETA" -eq 1 ]; then
      latest_tag=$(printf '%s\n' "$release_tags" \
        | grep -E -- '-(beta|alpha|rc)([.-]|$)' \
        | head -1 || true)
    else
      latest_tag=$(printf '%s\n' "$release_tags" \
        | grep -Ev -- '-(beta|alpha|rc)([.-]|$)' \
        | head -1 || true)
    fi
    [ -z "$latest_tag" ] && err "无法解析最新 tag（API 限流？请用 --version 显式指定）"
    VERSION="${latest_tag#cli-v}"
  fi
  BASE="https://github.com/${REPO}/releases/download/cli-v${VERSION}"
fi

# 防止 API 响应或显式参数中的异常文本进入下载 URL。接受正式 semver、
# 预发布版本和 build metadata（例如 0.7.2-beta.1 / 0.7.2+build.3）。
if ! printf '%s\n' "$VERSION" \
  | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z][0-9A-Za-z.+-]*)?$'; then
  err "无效版本号: $VERSION"
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

# ---------- persist PATH ----------
escape_double_quoted() {
  printf '%s' "$1" | sed 's/[\\`"$]/\\&/g'
}

configure_path() {
  if [ "$MODIFY_PATH" -ne 1 ]; then
    warn "未修改 shell PATH 配置（如需自动配置，请传 --modify-path）"
    return
  fi

  login_shell=${SHELL:-}
  if [ -n "$login_shell" ]; then
    shell_name=${login_shell##*/}
  elif [ "$OS" = "darwin" ]; then
    shell_name=zsh
  else
    shell_name=sh
  fi
  case "$shell_name" in
    zsh)
      shell_profile="${ZDOTDIR:-$HOME}/.zshrc"
      ;;
    bash)
      if [ "$OS" = "darwin" ]; then
        if [ -f "$HOME/.bash_profile" ]; then
          shell_profile="$HOME/.bash_profile"
        elif [ -f "$HOME/.bash_login" ]; then
          shell_profile="$HOME/.bash_login"
        elif [ -f "$HOME/.profile" ]; then
          shell_profile="$HOME/.profile"
        else
          shell_profile="$HOME/.bash_profile"
        fi
      else
        shell_profile="$HOME/.bashrc"
      fi
      ;;
    fish)
      shell_profile="${XDG_CONFIG_HOME:-$HOME/.config}/fish/config.fish"
      ;;
    sh|dash|ksh)
      shell_profile="$HOME/.profile"
      ;;
    *)
      warn "无法识别当前 shell（${SHELL:-unknown}），未自动修改 PATH"
      warn "请将 $INSTALL_DIR 加入当前 shell 的 PATH"
      return
      ;;
  esac

  profile_dir=${shell_profile%/*}
  if ! mkdir -p "$profile_dir"; then
    warn "无法创建 shell 配置目录: $profile_dir"
    warn "请手动将 $INSTALL_DIR 加入 PATH"
    return
  fi

  case "$INSTALL_DIR" in
    "$HOME")
      profile_install_dir='$HOME'
      ;;
    "$HOME"/*)
      relative_install_dir=${INSTALL_DIR#"$HOME"/}
      escaped_relative_dir=$(escape_double_quoted "$relative_install_dir")
      profile_install_dir="\$HOME/$escaped_relative_dir"
      ;;
    *)
      profile_install_dir=$(escape_double_quoted "$INSTALL_DIR")
      ;;
  esac

  if [ "$shell_name" = "fish" ]; then
    path_line="fish_add_path \"$profile_install_dir\""
  else
    path_line="export PATH=\"$profile_install_dir:\$PATH\""
  fi
  case "$shell_name" in
    sh|dash|ksh) reload_command=". \"$shell_profile\"" ;;
    *) reload_command="source \"$shell_profile\"" ;;
  esac

  path_config_changed=0
  if [ -f "$shell_profile" ] && grep -Fqx "$path_line" "$shell_profile"; then
    info "PATH 已配置: $shell_profile"
  elif printf '\n# Added by yoooclaw installer\n%s\n' "$path_line" >> "$shell_profile"; then
    info "已将 $INSTALL_DIR 加入 PATH: $shell_profile"
    path_config_changed=1
  else
    warn "无法写入 shell 配置: $shell_profile"
    warn "请手动将 $INSTALL_DIR 加入 PATH"
    return
  fi

  if [ "$path_config_changed" -eq 1 ]; then
    info "PATH 将在新终端中生效；已打开的终端请重新打开，或执行: $reload_command"
  else
    case ":${PATH:-}:" in
      *:"$INSTALL_DIR":*) ;;
      *) warn "当前进程尚未加载 PATH 配置；请重新打开终端，或执行: $reload_command" ;;
    esac
  fi
}

configure_path

# 安装器是子进程，无法改变调用它的父 shell；这里仅保证本次验证按命令名执行。
PATH="$INSTALL_DIR:${PATH:-}"
export PATH

# ---------- verify ----------
resolved_yoooclaw=$(command -v yoooclaw 2>/dev/null || true)
[ -n "$resolved_yoooclaw" ] || err "安装完成但找不到 yoooclaw 命令"
installed_version=$(yoooclaw --version 2>/dev/null) || err "已写入文件但 --version 执行失败"
info "yoooclaw $installed_version ready"
info "命令验证通过: $resolved_yoooclaw"
