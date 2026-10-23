# shellcheck shell=bash
# 命名空间之外与登记 release 的差异，减去已登记产品补丁。lib.sh 与 build-local-app.sh 共用。
# 调用前要设好 ROOT、BASELINE、REL_TAG、REL_COMMIT。
# probe_namespace_diff：无差异，或差异路径都已登记时，返回 0，并设置
# NS_HAS_DIFF、UNREG_N、UNREG_LIST、REG_IDS、REG_IDS_CSV。
# REG_IDS / REG_IDS_CSV 只含至少一条 paths 出现在 DIFF_PATHS 里的补丁 id（登记顺序）。
# NS_HAS_DIFF=0 时两者为空。jq 读 product_patches 失败时打印中文原因并返回 3；
# 值不是数组（如 "none"）当空集。有未登记路径，或读不到差异路径时，打印原先的中文提示并返回 3。

probe_namespace_diff() {
  local p patches_json patches_type diff_json filtered_ids filtered_csv jq_rc
  NS_HAS_DIFF=0
  UNREG_N=0
  UNREG_LIST=""
  REG_PATHS=""
  DIFF_PATHS=""
  REG_IDS=""
  REG_IDS_CSV=""
  # 本机 jq 在解析失败时不加 -e 也可能退出 0，但这时没有输出。缺文件则非 0。
  # 成功时这个过滤器至少输出 null；空输出或非 0 都算读失败，不再静默当空。
  # 不是数组（null、"none" 等）才当空集。
  patches_json=$(jq -c '.current_baseline.product.product_patches' "$BASELINE" 2>/dev/null)
  jq_rc=$?
  if [ "$jq_rc" -ne 0 ] || [ -z "$patches_json" ]; then
    echo "前置核对失败：读 product_patches 失败" >&2
    return 3
  fi
  patches_type=$(jq -r 'type' <<<"$patches_json" 2>/dev/null) || patches_type=""
  if [ "$patches_type" != "array" ]; then
    patches_json='[]'
  fi
  # 先取全部 id。没有命名空间外差异时再清空；有差异时再收成实际出现的补丁。
  REG_IDS=$(jq -r '[.[] | .id] | join(", ")' <<<"$patches_json") || {
    echo "前置核对失败：读 product_patches 失败" >&2
    return 3
  }
  REG_IDS_CSV=$(jq -r '[.[] | .id] | join(",")' <<<"$patches_json") || {
    echo "前置核对失败：读 product_patches 失败" >&2
    return 3
  }
  if git -C "$ROOT" diff --quiet "$REL_COMMIT" HEAD -- . ':(exclude)docs/agent24' ':(exclude)scripts/agent24' \
    ':(exclude).github/scripts/agent24-*' ':(exclude).github/workflows/agent24-*'; then
    REG_IDS=""
    REG_IDS_CSV=""
    return 0
  fi
  NS_HAS_DIFF=1
  DIFF_PATHS=$(git -C "$ROOT" diff --name-only --no-renames "$REL_COMMIT" HEAD -- . ':(exclude)docs/agent24' \
    ':(exclude)scripts/agent24' ':(exclude).github/scripts/agent24-*' ':(exclude).github/workflows/agent24-*') \
    || { echo "前置核对失败：读不到 fork 与 release ${REL_TAG} 的差异路径" >&2; return 3; }
  REG_PATHS=$(jq -r '.[] | .paths[]?' <<<"$patches_json") || REG_PATHS=""
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
  diff_json=$(printf '%s\n' "$DIFF_PATHS" | jq -Rsc 'split("\n") | map(select(length > 0))') || {
    echo "前置核对失败：读不到 fork 与 release ${REL_TAG} 的差异路径" >&2
    return 3
  }
  filtered_ids=$(jq -r --argjson diffs "$diff_json" '
    [ .[] | select(any(.paths[]?; . as $p | ($diffs | index($p)) != null)) | .id ] | join(", ")
  ' <<<"$patches_json") || {
    echo "前置核对失败：读 product_patches 失败" >&2
    return 3
  }
  filtered_csv=$(jq -r --argjson diffs "$diff_json" '
    [ .[] | select(any(.paths[]?; . as $p | ($diffs | index($p)) != null)) | .id ] | join(",")
  ' <<<"$patches_json") || {
    echo "前置核对失败：读 product_patches 失败" >&2
    return 3
  }
  REG_IDS=$filtered_ids
  REG_IDS_CSV=$filtered_csv
}

# probe_require_harness_at_head：探测依据必须与 HEAD 一致。
# 路径集合 H = docs/agent24/UPSTREAM-BASELINE.json 与 scripts/agent24/probe/。
# 工作区里 H 有未提交改动（含暂存区）、未跟踪文件（含被忽略的），
# 或 skip-worktree / assume-unchanged 时，打印中文原因并返回 3。
# docs/agent24/probe-evidence/ 不在 H 里。
probe_require_harness_at_head() {
  local others flagged bad rc
  git -C "$ROOT" diff --quiet HEAD -- docs/agent24/UPSTREAM-BASELINE.json scripts/agent24/probe
  rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "前置核对失败：探测依据与 HEAD 不一致：docs/agent24/UPSTREAM-BASELINE.json 或 scripts/agent24/probe 有未提交改动，含暂存区。" >&2
    return 3
  fi
  others=$(git -C "$ROOT" ls-files --others -- docs/agent24/UPSTREAM-BASELINE.json scripts/agent24/probe) \
    || { echo "前置核对失败：列不出探测依据路径下的未跟踪文件" >&2; return 3; }
  if [ -n "$others" ]; then
    echo "前置核对失败：探测依据路径下有未跟踪文件（含被忽略的）。" >&2
    return 3
  fi
  flagged=$(git -C "$ROOT" ls-files -v -- docs/agent24/UPSTREAM-BASELINE.json scripts/agent24/probe) \
    || { echo "前置核对失败：读不到探测依据路径的 index 标记" >&2; return 3; }
  if [ -n "$flagged" ]; then
    bad=$(printf '%s\n' "$flagged" | grep -v '^H ' || true)
    if [ -n "$bad" ]; then
      echo "前置核对失败：探测依据路径有 skip-worktree 或 assume-unchanged 标记。" >&2
      return 3
    fi
  fi
  return 0
}
