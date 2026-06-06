#!/usr/bin/env bash
#
# release.sh —— 拉 release 分支并 bump 版本（cli 与 plugin 通用）
#
# 用法：
#   ./release.sh              # patch +1（默认）：0.1.8 → 0.1.9
#   ./release.sh patch        # 同上
#   ./release.sh minor        # 0.1.8 → 0.2.0
#   ./release.sh major        # 0.1.8 → 1.0.0
#   ./release.sh 1.2.3        # 指定确切版本
#   ./release.sh patch --push # bump 后顺带 push 分支 + tag 到 origin（触发发布）
#
# 行为：基于当前 HEAD 创建 release/<新版本> 分支，改 package.json 版本，
# 只提交 package.json（1 行 diff），并在该提交上打 annotated tag。
# 提交信息 / tag 按包名自动选：
#   @yoooclaw/cli → 提交 "cli-<版本>"、tag "cli-v<版本>"（触发 release-cli.yml）
#   其它包（如插件）→ 提交 "<版本>"、tag "v<版本>"（触发插件 release.yml）
# 默认不 push；末尾打印 push / PR 提示。加 --push 则 push 分支 + tag。
#
# 注意：发布的唯一触发器是 tag 推送（不是分支/PR 合并）。tag 落在 release/<版本>
# 分支的 bump 提交上，满足 workflow 的「tag commit 必须在 release 分支」前置检查；
# 合回 master 的 PR 仅为同步主干，与是否发包无关。
#
set -euo pipefail
cd "$(dirname "$0")"

[ -f package.json ] || { echo "✗ 当前目录没有 package.json"; exit 1; }

# ── 解析参数：第一个非 --push 的参数是 bump 类型/版本号 ──
bump="patch"
do_push="no"
for arg in "$@"; do
  case "$arg" in
    --push) do_push="yes" ;;
    *) bump="$arg" ;;
  esac
done

current=$(node -p "require('./package.json').version")
pkgname=$(node -p "require('./package.json').name")

# ── 计算新版本 ──
if [[ "$bump" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.].*)?$ ]]; then
  next="$bump"
else
  clean="${current%%-*}"                 # 去掉可能的 -beta.0 之类后缀
  IFS='.' read -r major minor patch <<< "$clean"
  case "$bump" in
    major) major=$((major + 1)); minor=0; patch=0 ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
    *) echo "✗ 未知参数：$bump（用 patch|minor|major 或 x.y.z）"; exit 1 ;;
  esac
  next="$major.$minor.$patch"
fi

branch="release/$next"

# ── 提交信息 / tag 前缀：cli 包带 cli-/cli-v 前缀，其它包用裸版本号 / v 前缀 ──
case "$pkgname" in
  @yoooclaw/cli) msg="cli-$next"; tag="cli-v$next" ;;
  *)             msg="$next";     tag="v$next" ;;
esac

# ── 前置校验 ──
git rev-parse --git-dir >/dev/null 2>&1 || { echo "✗ 不在 git 仓库里"; exit 1; }
if git show-ref --verify --quiet "refs/heads/$branch"; then
  echo "✗ 分支 $branch 已存在"; exit 1
fi
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  echo "✗ tag $tag 已存在"; exit 1
fi

base=$(git rev-parse --abbrev-ref HEAD)
echo "→ $pkgname：$current → $next（从 $base 切 $branch）"

# ── 创建分支 + 改版本（仅替换首个 "version" 行，保证 1 行 diff）+ 提交 + 打 tag ──
git checkout -b "$branch"
perl -i -pe 'if (!$d && /"version"\s*:/) { s/("version"\s*:\s*")[^"]*(")/${1}'"$next"'${2}/; $d = 1 }' package.json
git add package.json
git commit -m "$msg"
git tag -a "$tag" -m "$next"

echo "✓ 已提交并打 tag：$msg / $tag @ $branch"

if [ "$do_push" = "yes" ]; then
  # --follow-tags：连同 reachable 的 annotated tag 一起推；tag 一推即触发发布 workflow
  git push --follow-tags -u origin "$branch"
  echo "✓ 已 push origin/$branch + tag $tag → 发布 workflow 将自动触发"
  echo "下一步（可选，合回主干）：gh pr create --base master --head $branch --title \"$msg\""
else
  echo "下一步：git push --follow-tags -u origin $branch   # 推分支 + tag $tag，触发发布"
  echo "       （可选，合回主干）gh pr create --base master --head $branch --title \"$msg\""
fi
