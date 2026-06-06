#!/usr/bin/env bash
# 交叉编译 Go CLI，并装配 npm 分发产物（主 launcher 包 + 各平台子包）。
#
# 用法:
#   scripts/build-go.sh [version]        # version 默认从 package.json 读，保持与 npm 版本同源
#   scripts/build-go.sh --current        # 只构当前平台（本地快速验证）
#
# 产物:
#   dist-native/yoooclaw-<os>-<arch>{,.exe}   原始二进制（GitHub Release / install.sh 用）
#   dist-npm/cli/                             主 launcher 包 @yoooclaw/cli
#   dist-npm/cli-<os>-<arch>/                 各平台子包 @yoooclaw/cli-<os>-<arch>
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

MODULE="github.com/YoooClaw/cli"

# 解析参数：可选 --current，以及可选 version。
CURRENT_ONLY=0
VERSION=""
for arg in "$@"; do
  case "$arg" in
    --current) CURRENT_ONLY=1 ;;
    *) VERSION="$arg" ;;
  esac
done
[ -n "$VERSION" ] || VERSION="$(node -p "require('./package.json').version")"

LDFLAGS="-s -w -X ${MODULE}/internal/version.Version=${VERSION}"

# 目标矩阵： GOOS:GOARCH:npmOs:npmCpu
ALL_TARGETS="darwin:arm64:darwin:arm64 darwin:amd64:darwin:x64 linux:amd64:linux:x64 linux:arm64:linux:arm64 windows:amd64:win32:x64"
if [ "$CURRENT_ONLY" -eq 1 ]; then
  goos="$(go env GOOS)"; goarch="$(go env GOARCH)"
  npmcpu="$goarch"; [ "$goarch" = "amd64" ] && npmcpu="x64"
  npmos="$goos"; [ "$goos" = "windows" ] && npmos="win32"
  TARGETS="${goos}:${goarch}:${npmos}:${npmcpu}"
else
  TARGETS="$ALL_TARGETS"
fi

rm -rf dist-native dist-npm
mkdir -p dist-native dist-npm

PLATFORM_PKGS=""

for t in $TARGETS; do
  GOOS="${t%%:*}";  rest="${t#*:}"
  GOARCH="${rest%%:*}"; rest="${rest#*:}"
  NPM_OS="${rest%%:*}"; NPM_CPU="${rest##*:}"

  ext=""; bin="yc"
  if [ "$GOOS" = "windows" ]; then ext=".exe"; bin="yc.exe"; fi
  asset="yoooclaw-${NPM_OS}-${NPM_CPU}${ext}"

  echo "==> go build $GOOS/$GOARCH -> dist-native/$asset"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -ldflags "$LDFLAGS" -o "dist-native/$asset" ./cmd/yc

  pkgdir="dist-npm/cli-${NPM_OS}-${NPM_CPU}"
  mkdir -p "$pkgdir/bin"
  cp "dist-native/$asset" "$pkgdir/bin/$bin"
  chmod +x "$pkgdir/bin/$bin"
  node scripts/gen-pkg.mjs platform "$pkgdir" "$VERSION" "$NPM_OS" "$NPM_CPU" "$bin"

  PLATFORM_PKGS="$PLATFORM_PKGS @yoooclaw/cli-${NPM_OS}-${NPM_CPU}"
done

# 主 launcher 包
maindir="dist-npm/cli"
mkdir -p "$maindir/bin"
cp npm/cli/bin/yc.js "$maindir/bin/yc.js"
cp npm/cli/README.md "$maindir/README.md"
node scripts/gen-pkg.mjs main "$maindir" "$VERSION" "$PLATFORM_PKGS"

echo
echo "完成 v${VERSION}："
echo "  dist-native/  原始二进制"
ls -1 dist-native | sed 's/^/    /'
echo "  dist-npm/     npm 包（主包 + 平台子包）"
ls -1 dist-npm | sed 's/^/    /'
