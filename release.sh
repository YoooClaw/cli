#!/usr/bin/env bash
#
# release.sh —— 开 release 分支 / 在 release 分支上 bump 版本并打 tag
#
# 发布流程：
#   1. 开分支：在 master（或任意非 release 分支）上运行，基于当前 HEAD 创建
#      release/<版本>。之后往这个分支 push 的每个提交，CI 通过后都会自动发布到
#      测试环境（版本号 <版本>-test.<run>）。
#   2. 发布：在 release/<版本> 分支上运行，改 package.json 版本、提交并打 tag；
#      tag 推送后由 release-cli.yml 发布到生产（beta 或正式版）。
#   3. 归档：把 release 分支合回 master，仅为代码归档，不触发任何发布。
#
# 用法：
#   在非 release 分支上（开分支）：
#     ./release.sh                 # patch +1（默认）：0.1.8 → release/0.1.9
#     ./release.sh minor|major     # 0.1.8 → release/0.2.0 / release/1.0.0
#     ./release.sh 1.2.3           # 指定确切版本 → release/1.2.3
#     ./release.sh 1.2.3 --push    # 顺带 push 分支（触发测试环境发布）
#
#   在 release/<版本> 分支上（打 tag 发生产）：
#     ./release.sh                 # 发布分支名对应的正式版（release/1.2.3 → 1.2.3）
#     ./release.sh 1.2.3-beta.0    # 发布 beta（npm beta tag，不动 live 安装脚本）
#     ./release.sh 1.2.3 --push    # 顺带 push 分支 + tag（触发生产发布）
#
# 提交信息 / tag 按包名自动选：
#   @yoooclaw/cli → 提交 "cli-<版本>"、tag "cli-v<版本>"（触发 release-cli.yml）
#   其它包（如插件）→ 提交 "<版本>"、tag "v<版本>"（触发插件 release.yml）
#
set -euo pipefail
cd "$(dirname "$0")"

[ -f package.json ] || { echo "✗ 当前目录没有 package.json"; exit 1; }
git rev-parse --git-dir >/dev/null 2>&1 || { echo "✗ 不在 git 仓库里"; exit 1; }

# ── 解析参数：第一个非 --push 的参数是 bump 类型/版本号 ──
bump=""
do_push="no"
for arg in "$@"; do
  case "$arg" in
    --push) do_push="yes" ;;
    *) bump="$arg" ;;
  esac
done

current=$(node -p "require('./package.json').version")
pkgname=$(node -p "require('./package.json').name")
base=$(git rev-parse --abbrev-ref HEAD)
semver_re='^[0-9]+\.[0-9]+\.[0-9]+([-.].*)?$'

# 计算 patch/minor/major 或校验显式版本号
resolve_version() {
  local spec="$1"
  if [[ "$spec" =~ $semver_re ]]; then
    echo "$spec"
    return
  fi
  local clean="${current%%-*}"           # 去掉可能的 -beta.0 之类后缀
  local major minor patch
  IFS='.' read -r major minor patch <<< "$clean"
  case "$spec" in
    major) major=$((major + 1)); minor=0; patch=0 ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
    *) echo "✗ 未知参数：$spec（用 patch|minor|major 或 x.y.z）" >&2; exit 1 ;;
  esac
  echo "$major.$minor.$patch"
}

# 本地分支落后远端时拒绝继续：旧 HEAD 打 tag 会漏掉已合入的 PR
ensure_up_to_date() {
  local branch="$1"
  git fetch --quiet origin
  if git show-ref --verify --quiet "refs/remotes/origin/$branch"; then
    if ! git merge-base --is-ancestor "origin/$branch" HEAD; then
      echo "✗ 本地 $branch 落后于 origin/$branch，先 git pull --ff-only"
      exit 1
    fi
  fi
}

if [[ "$base" != release/* ]]; then
  # ════ 开分支 ════
  next=$(resolve_version "${bump:-patch}")
  branch="release/$next"
  if git show-ref --verify --quiet "refs/heads/$branch" \
    || git ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1; then
    echo "✗ 分支 $branch 已存在"; exit 1
  fi
  ensure_up_to_date "$base"

  echo "→ ${pkgname}：从 ${base} 切 ${branch}"
  git checkout -b "$branch"
  echo "✓ 已创建 $branch（package.json 版本保持 ${current}，打 tag 时再 bump）"

  if [ "$do_push" = "yes" ]; then
    git push -u origin "$branch"
    echo "✓ 已 push origin/$branch → CI 通过后自动发布到测试环境"
  else
    echo "下一步：git push -u origin $branch   # 推分支，CI 通过后自动发布到测试环境"
  fi
  echo "发生产：在 $branch 上运行 ./release.sh [x.y.z-beta.N] --push"
  exit 0
fi

# ════ 在 release 分支上 bump + 打 tag ════
branch_version="${base#release/}"
if [ -n "$bump" ]; then
  next=$(resolve_version "$bump")
elif [[ "$branch_version" =~ $semver_re ]]; then
  next="$branch_version"
else
  echo "✗ 分支名 $base 不是版本号，请显式指定要发布的版本"; exit 1
fi

case "$pkgname" in
  @yoooclaw/cli) msg="cli-$next"; tag="cli-v$next" ;;
  *)             msg="$next";     tag="v$next" ;;
esac

if git rev-parse -q --verify "refs/tags/$tag" >/dev/null \
  || git ls-remote --exit-code --tags origin "refs/tags/$tag" >/dev/null 2>&1; then
  echo "✗ tag $tag 已存在"; exit 1
fi
if [ "${next%%-*}" != "${branch_version%%-*}" ]; then
  echo "⚠ 版本 $next 与分支 $base 的版本号不一致，确认无误后继续"
fi
ensure_up_to_date "$base"

echo "→ ${pkgname}：${current} → ${next}（在 ${base} 上打 tag ${tag}）"

# 改版本（仅替换首个 "version" 行，保证 1 行 diff）+ 只提交 package.json；版本已一致则直接在 HEAD 打 tag
if [ "$current" != "$next" ]; then
  perl -i -pe 'if (!$d && /"version"\s*:/) { s/("version"\s*:\s*")[^"]*(")/${1}'"$next"'${2}/; $d = 1 }' package.json
  git add package.json
  git commit -m "$msg"
fi
git tag -a "$tag" -m "$next"

echo "✓ 已打 tag：$tag @ $base"

if [ "$do_push" = "yes" ]; then
  # --follow-tags：连同 reachable 的 annotated tag 一起推；tag 一推即触发生产发布
  git push --follow-tags -u origin "$base"
  echo "✓ 已 push origin/$base + tag $tag → 生产发布 workflow 将自动触发"
else
  echo "下一步：git push --follow-tags -u origin $base   # 推分支 + tag $tag，触发生产发布"
fi
echo "归档（发布完成后）：gh pr create --base master --head $base --title \"$msg\""
