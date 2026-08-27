#!/usr/bin/env bash
# Build the OpenHarmony/arm64 CLI and optionally pack it as an HNP.
#
# Requirements:
#   OHOS_GO      Go executable from OpenHarmony-SIG/ohos_golang_go
#   OHOS_GOROOT  Optional toolchain root when OHOS_GO is not under <root>/bin
#   OHOS_HNPCLI  Optional hnpcli from the OpenHarmony SDK. When omitted, the
#                script emits the same ZIP-based HNP layout with the host zip.
#
# Usage:
#   OHOS_GO=/path/to/ohos-go OHOS_HNPCLI=/path/to/hnpcli scripts/build-openharmony.sh
#   OHOS_GO=/path/to/ohos-go scripts/build-openharmony.sh --stage-only 0.6.3
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION=""
STAGE_ONLY=0
for arg in "$@"; do
  case "$arg" in
    --stage-only) STAGE_ONLY=1 ;;
    *) VERSION="$arg" ;;
  esac
done
[ -n "$VERSION" ] || VERSION="$(node -p "require('./package.json').version")"

OHOS_GO_BIN="${OHOS_GO:-}"
if [ -z "$OHOS_GO_BIN" ] || [ ! -x "$OHOS_GO_BIN" ]; then
  echo "error: 请用 OHOS_GO 指定 ohos_golang_go 的可执行文件" >&2
  exit 1
fi
OHOS_GOROOT_DIR="${OHOS_GOROOT:-$(cd "$(dirname "$OHOS_GO_BIN")/.." && pwd)}"
if [ ! -f "$OHOS_GOROOT_DIR/src/internal/goos/zgoos_openharmony.go" ]; then
  echo "error: $OHOS_GOROOT_DIR 不是 ohos_golang_go GOROOT（可用 OHOS_GOROOT 显式指定）" >&2
  exit 1
fi
if ! GOROOT="$OHOS_GOROOT_DIR" GOTOOLCHAIN=local \
  "$OHOS_GO_BIN" tool dist list | grep -qx 'openharmony/arm64'; then
  echo "error: $OHOS_GO_BIN 不支持 openharmony/arm64" >&2
  exit 1
fi

OHOS_GO_VERSION="$(GOROOT="$OHOS_GOROOT_DIR" GOTOOLCHAIN=local \
  "$OHOS_GO_BIN" env GOVERSION)"
MODULE_GO_VERSION="${OHOS_GO_VERSION#go}"
MODULE_GO_VERSION="${MODULE_GO_VERSION%%.*}.${MODULE_GO_VERSION#*.}"
MODULE_GO_VERSION="${MODULE_GO_VERSION%.*}.0"

BUILD_TMP="$(mktemp -d "${TMPDIR:-/tmp}/yoooclaw-openharmony.XXXXXX")"
trap 'rm -rf "$BUILD_TMP"' EXIT
MODFILE="$BUILD_TMP/yoooclaw.mod"
cp go.mod "$MODFILE"
cp go.sum "$BUILD_TMP/yoooclaw.sum"
awk -v version="$MODULE_GO_VERSION" \
  '$1 == "go" { $0 = "go " version } { print }' \
  "$MODFILE" > "$BUILD_TMP/yoooclaw.mod.next"
mv "$BUILD_TMP/yoooclaw.mod.next" "$MODFILE"

OUT="dist-openharmony"
STAGE="$OUT/stage/yoooclaw"
rm -rf "$OUT"
mkdir -p "$STAGE/bin"

echo "==> go build openharmony/arm64 -> $STAGE/bin/yoooclaw"
GOROOT="$OHOS_GOROOT_DIR" GOTOOLCHAIN=local \
  GOOS=openharmony GOARCH=arm64 CGO_ENABLED=0 \
  "$OHOS_GO_BIN" build \
  -modfile="$MODFILE" \
  -trimpath \
  -ldflags "-s -w -X github.com/YoooClaw/cli/internal/version.Version=$VERSION" \
  -o "$STAGE/bin/yoooclaw" \
  ./cmd/yc
chmod 755 "$STAGE/bin/yoooclaw"
node scripts/gen-hnp.mjs "$STAGE" "$VERSION"

if [ "$STAGE_ONLY" -eq 1 ]; then
  echo "完成 OpenHarmony HNP staging: $STAGE"
  exit 0
fi

HNP_OUT="$OUT/hnp/arm64-v8a"
mkdir -p "$HNP_OUT"
HNPCLI_BIN="${OHOS_HNPCLI:-}"
if [ -z "$HNPCLI_BIN" ]; then
  HNPCLI_BIN="$(command -v hnpcli || true)"
fi
if [ -n "$HNPCLI_BIN" ] && [ -x "$HNPCLI_BIN" ]; then
  "$HNPCLI_BIN" pack -i "$STAGE" -o "$HNP_OUT"
else
  if ! command -v zip >/dev/null 2>&1; then
    echo "error: 找不到 hnpcli 或 zip；也可加 --stage-only 只生成 staging" >&2
    exit 1
  fi
  echo "warn: 未找到 hnpcli，按官方 HNP ZIP 布局打包" >&2
  (
    cd "$OUT/stage"
    COPYFILE_DISABLE=1 zip -q -9 -r "$ROOT/$HNP_OUT/yoooclaw.hnp" yoooclaw
  )
fi
test -f "$HNP_OUT/yoooclaw.hnp"
echo "完成 OpenHarmony HNP: $HNP_OUT/yoooclaw.hnp"
