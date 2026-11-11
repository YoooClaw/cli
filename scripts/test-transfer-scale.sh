#!/usr/bin/env bash
# Shared local/CI gate: a successful command with zero tests is not a pass.
set -euo pipefail
cd "$(dirname "$0")/.."
output=$(mktemp)
trap 'rm -f "$output"' EXIT
go test -count=1 -v ./internal/transfer -run '^TestTransferScale$' | tee "$output"
if ! grep -Eq '^--- PASS: TestTransferScale \(' "$output"; then
  echo '容量门禁失败：TestTransferScale 未实际通过。请检查 GOFLAGS、构建标签及测试名称。' >&2
  exit 1
fi
