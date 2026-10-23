# shellcheck shell=bash
# 命名空间之外与登记 release 的差异，减去已登记产品补丁。lib.sh 与 build-local-app.sh 共用。
# 调用前要设好 ROOT、BASELINE、REL_TAG、REL_COMMIT。
# probe_namespace_diff：无差异，或差异路径都已登记时，返回 0，并设置
# NS_HAS_DIFF、UNREG_N、UNREG_LIST、REG_IDS。有未登记路径，或读不到差异路径时，
# 打印原先的中文提示并返回 3。

probe_require_clean_worktree() {
  local st
  st=$(git -C "$ROOT" status --porcelain) || {
    echo "前置核对失败：读不到工作区状态" >&2
    return 3
  }
  if [ -n "$st" ]; then
    echo "前置核对失败：工作区不干净（git status --porcelain 有输出，含未跟踪文件）。本地构建要求干净工作区。" >&2
    return 3
  fi
}

probe_namespace_diff() {
  local p
  NS_HAS_DIFF=0
  UNREG_N=0
  UNREG_LIST=""
  REG_IDS=""
  REG_PATHS=""
  DIFF_PATHS=""
  if git -C "$ROOT" diff --quiet "$REL_COMMIT" HEAD -- . ':(exclude)docs/agent24' ':(exclude)scripts/agent24' \
    ':(exclude).github/scripts/agent24-*' ':(exclude).github/workflows/agent24-*'; then
    return 0
  fi
  NS_HAS_DIFF=1
  DIFF_PATHS=$(git -C "$ROOT" diff --name-only --no-renames "$REL_COMMIT" HEAD -- . ':(exclude)docs/agent24' \
    ':(exclude)scripts/agent24' ':(exclude).github/scripts/agent24-*' ':(exclude).github/workflows/agent24-*') \
    || { echo "前置核对失败：读不到 fork 与 release ${REL_TAG} 的差异路径" >&2; return 3; }
  # product_patches 是数组时取全部 .paths[]；是 "none" 或读不到时当作空集。
  REG_PATHS=$(jq -r '.current_baseline.product.product_patches
    | if type == "array" then .[] | .paths[]? else empty end' "$BASELINE" 2>/dev/null) || REG_PATHS=""
  REG_IDS=$(jq -r '.current_baseline.product.product_patches
    | if type == "array" then [.[] | .id] | join(", ") else "" end' "$BASELINE" 2>/dev/null) || REG_IDS=""
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    if [ -n "$REG_PATHS" ] && printf '%s\n' "$REG_PATHS" | grep -Fxq -- "$p"; then continue; fi
    UNREG_N=$((UNREG_N + 1))
    if [ "$UNREG_N" -le 10 ]; then UNREG_LIST=${UNREG_LIST:+$UNREG_LIST, }$p; fi
  done <<<"$DIFF_PATHS"
  if [ "$UNREG_N" -gt 0 ]; then
    if [ "$UNREG_N" -gt 10 ]; then UNREG_LIST="$UNREG_LIST 等 ${UNREG_N} 个"; fi
    echo "前置核对失败：fork 命名空间之外有未登记的改动，与登记的 release ${REL_TAG} 不同：${UNREG_LIST}。产品补丁要先登记到 docs/agent24/PATCH-LEDGER.md 和 UPSTREAM-BASELINE.json 的 product_patches。" >&2
    return 3
  fi
}
