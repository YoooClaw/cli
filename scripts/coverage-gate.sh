#!/usr/bin/env bash
# coverage-gate.sh —— 总覆盖率门禁：低于 MIN_COVERAGE 即失败（只许涨不许跌）。
#
# 用法：
#   scripts/coverage-gate.sh            # 用 coverage.out（需先 go test -coverprofile）
#   MIN_COVERAGE=60 scripts/coverage-gate.sh
#
# 提升阈值的节奏：补完一波测试后，把 MIN_COVERAGE 抬到略低于当前实际值。
set -euo pipefail

MIN_COVERAGE="${MIN_COVERAGE:-55}"
PROFILE="${1:-coverage.out}"

if [[ ! -f "$PROFILE" ]]; then
  echo "覆盖率文件不存在：$PROFILE（先跑 go test -coverprofile=$PROFILE ./...）" >&2
  exit 2
fi

total="$(go tool cover -func="$PROFILE" | awk '/^total:/ {gsub(/%/,"",$NF); print $NF}')"

awk -v t="$total" -v m="$MIN_COVERAGE" 'BEGIN {
  printf "总覆盖率 %.1f%%（门禁 %.1f%%）\n", t, m
  if (t + 0.001 < m) { print "✗ 覆盖率低于门禁"; exit 1 }
  print "✓ 覆盖率达标"
}'
