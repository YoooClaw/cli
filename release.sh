#!/usr/bin/env bash
#
# release.sh —— 开 release 分支 / 创建版本 PR / 在已合入版本上打 tag
#
# 发布流程：
#   1. 开分支：在 master（或任意非 release 分支）上运行，基于当前 HEAD 创建
#      release/<版本>。之后往这个分支 push 的每个提交，CI 通过后都会自动发布到
#      测试环境（版本号 <版本>-test.<run>）。
#   2. 发布：--push 会自动创建版本 PR、等待 CI、squash 合入，再打 tag；无需手动合入。
#      仅已同步远端 release 的 HEAD 可以打 tag；tag 推送触发生产发布。
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
#     ./release.sh 1.2.3 --push    # 自动处理版本 PR 和 CI，最后仅推送目标 tag
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
  echo "✗ tag $tag 已存在"
  echo "若上次推送失败，先用 git ls-remote --tags origin refs/tags/$tag 核对远端"
  echo "确认该 tag 仅在本地且未发布后，可 git tag -d $tag 再重跑；不要删除或重建已发布的 tag"
  exit 1
fi
if [ "${next%%-*}" != "${branch_version%%-*}" ]; then
  echo "⚠ 版本 $next 与分支 $base 的版本号不一致，确认无误后继续"
fi
ensure_up_to_date "$base"
if ! git show-ref --verify --quiet "refs/remotes/origin/$base" \
  || [ "$(git rev-parse HEAD)" != "$(git rev-parse "origin/$base")" ]; then
  echo "✗ 必须在与 origin/$base 完全一致的提交上准备发布；本地改动请先通过 PR 合入"
  exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
  echo "✗ 工作区不干净，请先提交或暂存改动"
  exit 1
fi

echo "→ ${pkgname}：${current} → ${next}（目标 ${base}，tag ${tag}）"

# release 分支必须经 PR 更新，版本提交也不例外。
if [ "$current" != "$next" ]; then
  version_branch="codex/release-${next}-version"
  if git show-ref --verify --quiet "refs/heads/$version_branch" \
    || git ls-remote --exit-code --heads origin "refs/heads/$version_branch" >/dev/null 2>&1; then
    echo "✗ 版本分支 $version_branch 已存在；本次未修改版本或创建 tag"
    echo "请先查看该分支或已有 PR，合入后切回 $base、同步远端再运行 release.sh；不要覆盖现有分支"
    exit 1
  fi
  if [ "$do_push" = "yes" ]; then
    command -v gh >/dev/null 2>&1 || { echo "✗ --push 创建版本 PR 需要 gh"; exit 1; }
  fi
  git checkout -b "$version_branch"
  perl -i -pe 'if (!$d && /"version"\s*:/) { s/("version"\s*:\s*")[^"]*(")/${1}'"$next"'${2}/; $d = 1 }' package.json
  git add package.json
  git commit -m "$msg"
  if [ "$do_push" != "yes" ]; then
    echo "✓ 已准备本地版本分支；未创建 PR 或 tag"
    echo "自动发版入口：在同步后的 $base 上运行 ./release.sh $next --push"
    exit 0
  fi
  git push -u origin "$version_branch"
  version_head="$(git rev-parse HEAD)"
  pr_url="$(gh pr create --base "$base" --head "$version_branch" --title "$msg" \
    --body "将 package.json 版本更新为 ${next}。release.sh 将等待必需 CI，通过后自动 squash 合入并推送 ${tag}。")"
  echo "→ 等待版本 PR 的必需 CI：$pr_url"
  # PR 创建后，Actions 注册检查可能稍有延迟，先等检查出现。
  checks_ready="no"
  for ((attempt = 0; attempt < 30; attempt++)); do
    check_count="$(gh pr checks "$pr_url" --required --json name --jq 'length' 2>/dev/null || true)"
    if [[ "$check_count" =~ ^[1-9][0-9]*$ ]]; then
      checks_ready="yes"
      break
    fi
    sleep 2
  done
  if [ "$checks_ready" != "yes" ]; then
    echo "✗ 未能读取版本 PR 的必需 CI；已停止，未合并或打 tag：$pr_url"
    exit 1
  fi
  if ! gh pr checks "$pr_url" --required --watch --fail-fast; then
    echo "✗ 版本 PR 的 CI 未通过；已停止，未合并或打 tag：$pr_url"
    exit 1
  fi
  # 不使用 --admin；GitHub 仍校验 PR、CI、目标分支和精确 head。
  if ! gh pr merge "$pr_url" --squash --subject "$msg" --match-head-commit "$version_head"; then
    echo "✗ 版本 PR 未合并；已停止，未打 tag：$pr_url"
    exit 1
  fi
  merged_head="$(gh pr view "$pr_url" --json mergeCommit --jq '.mergeCommit.oid')"
  git fetch --quiet origin "+refs/heads/$base:refs/remotes/origin/$base"
  if [ "$merged_head" != "$(git rev-parse "origin/$base")" ]; then
    echo "✗ release 分支在版本 PR 合入后已有其他更新；已停止，未打 tag"
    exit 1
  fi
  git checkout "$base"
  git merge --ff-only "origin/$base"
  if [ -n "$(git status --porcelain)" ] \
    || [ "$(node -p "require('./package.json').version")" != "$next" ]; then
    echo "✗ 合入后的工作区或版本不符合预期；已停止，未打 tag"
    exit 1
  fi
  echo "✓ 版本 PR 已自动合入，继续推送本次 tag"
fi
git tag -a "$tag" -m "$next"

echo "✓ 已打 tag：$tag @ $base"

if [ "$do_push" = "yes" ]; then
  # 精确推送本次 tag，不直接写受保护 release 分支，也不连带其他本地 tag。
  if ! git push origin "refs/tags/$tag"; then
    echo "✗ tag 推送失败，本地 tag $tag 已保留"
    echo "请先核对远端状态及发布权限；确认可发布后，可直接重试 git push origin refs/tags/$tag"
    echo "如需重跑脚本，先确认远端没有该 tag 且未发布，再 git tag -d $tag；不要改写已发布的 tag"
    exit 1
  fi
  echo "✓ 已 push tag $tag → 生产发布 workflow 将自动触发"
else
  echo "下一步：git push origin refs/tags/$tag   # 仅推送本次 tag，触发生产发布"
fi
echo "归档（发布完成后）：gh pr create --base master --head $base --title \"$msg\""
