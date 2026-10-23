#!/usr/bin/env bash
# 本地构建模式的前置核对自测。不依赖 docker，不访问网络。
# 临时 git 仓库：第一个提交是 release，第二个提交只改已登记的 a.txt。
# json/label 核对用 shell 函数 docker 按参数回放预置值。
# 用法：scripts/agent24/probe/check-local-mode-selftest.sh
set -uo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
BASE=$(mktemp -d "${TMPDIR:-/tmp}/weknora-local-mode-selftest.XXXXXX") || exit 2
trap 'if [ -n "${BASE:-}" ] && [ -d "$BASE" ]; then rm -rf "$BASE"; fi' EXIT
n=0
bad=0
r=$BASE/repo

g() {
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "$r" -c user.name=selftest \
    -c user.email=selftest@invalid -c commit.gpgsign=false "$@"
}

git_at() {
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "$1" -c user.name=selftest \
    -c user.email=selftest@invalid -c commit.gpgsign=false "${@:2}"
}

setup_repo() {
  mkdir -p "$r/scripts/agent24/probe" "$r/docs/agent24" || return 1
  cp "$HERE/lib.sh" "$HERE/patches.sh" "$r/scripts/agent24/probe/" || return 1
  printf 'v1\n' >"$r/a.txt" || return 1
  printf '%s\n' '{"current_baseline":{"release":{"tag":"v0.8.3","commit":"0000000000000000000000000000000000000000"},"product":{"product_patches":[{"id":"T-LOCAL-1","paths":["a.txt"]},{"id":"T-LOCAL-2","paths":["a.txt"]}]}}}' \
    >"$r/docs/agent24/UPSTREAM-BASELINE.json" || return 1
  g init -q || return 1
  g add -A || return 1
  g commit -q -m release || return 1
  c1=$(g rev-parse HEAD) || return 1
  printf '%s\n' "{\"current_baseline\":{\"release\":{\"tag\":\"v0.8.3\",\"commit\":\"${c1}\"},\"product\":{\"product_patches\":[{\"id\":\"T-LOCAL-1\",\"paths\":[\"a.txt\"]},{\"id\":\"T-LOCAL-2\",\"paths\":[\"a.txt\"]}]}}}" \
    >"$r/docs/agent24/UPSTREAM-BASELINE.json" || return 1
  printf 'v2\n' >"$r/a.txt" || return 1
  g add -A || return 1
  g commit -q -m registered || return 1
}

if ! setup_repo >"$BASE/setup.log" 2>&1; then
  echo "FAIL 建临时仓库失败" >&2
  sed 's/^/  /' "$BASE/setup.log" >&2
  exit 1
fi

show_err() { sed 's/^/  /' "$d/err"; }

src_case() {
  local mode=$1 want=$2 name=$3 needle=${4:-} note=${5:-}
  n=$((n + 1))
  d=$BASE/c$n
  mkdir -p "$d/out" "$d/stack" || { bad=1; printf '不符 %s：建目录失败\n' "$name"; return; }
  (
    OUT=$d/out
    STACK_DIR=$d/stack
    PROBE_EMBED=none
    PROBE_APP_IMAGE=$mode
    # shellcheck disable=SC1091
    source "$r/scripts/agent24/probe/lib.sh"
    printf '%s\n' "${TESTED_DIR_NOTE:-}"
  ) >"$d/log" 2>"$d/err"
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad=1
    printf '不符 %s：期望 rc=%s，实际 rc=%s\n' "$name" "$want" "$rc"
    show_err
    return
  fi
  if [ -n "$needle" ] && ! grep -F -q -- "$needle" "$d/err"; then
    bad=1
    printf '不符 %s：stderr 不含 %s\n' "$name" "$needle"
    show_err
    return
  fi
  if [ -n "$note" ] && ! grep -F -q -- "$note" "$d/log"; then
    bad=1
    printf '不符 %s：TESTED_DIR_NOTE 不含 %s\n' "$name" "$note"
    sed 's/^/  /' "$d/log"
  fi
}

src_case bogus 2 "PROBE_APP_IMAGE=bogus"
src_case official 3 "official 只有已登记差异" "PROBE_APP_IMAGE=local"
src_case local 0 "local 只有已登记差异" "" "T-LOCAL-1"

verify_case() {
  local mode=$1 want=$2 name=$3 needle=${4:-}
  n=$((n + 1))
  d=$BASE/c$n
  mkdir -p "$d/out" "$d/stack" || { bad=1; printf '不符 %s：建目录失败\n' "$name"; return; }
  (
    OUT=$d/out
    STACK_DIR=$d/stack
    PROBE_EMBED=none
    PROBE_APP_IMAGE=local
    # shellcheck disable=SC1091
    source "$r/scripts/agent24/probe/lib.sh"
    want_version="${REL_TAG#v}+agent24.${TESTED_SHA}"
    tag="weknora-app:agent24-${TESTED_SHA}"
    image_id="sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    rev_json=$HEAD_REV
    patch_json=$REG_IDS_CSV
    stub_rev=$HEAD_REV
    stub_base=$REL_TAG
    stub_patch=$REG_IDS_CSV
    stub_any=1
    stub_ver=$want_version
    json_ver=$want_version
    json_base=$REL_TAG
    ba_any=1
    ba_ver=$want_version
    ba_commit=$HEAD_REV
    stub_cfg=$tag
    use_id=$image_id
    bt_arg=2026-01-01T00:00:00Z
    built_at=$bt_arg
    go_ver=""
    pkind=split
    case $mode in
      ok) ;;
      json_rev) rev_json=0000000000000000000000000000000000000000 ;;
      image_id) use_id=sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff ;;
      label_rev) stub_rev=1111111111111111111111111111111111111111 ;;
      patch_label) stub_patch=NOT-THE-ID ;;
      patch_label_spaced) stub_patch=$REG_IDS ;;
      ver_label) stub_ver=0.0.0+agent24.wrong ;;
      base_label) stub_base=v9.9.9 ;;
      json_ver) json_ver=0.0.1+agent24.wrong ;;
      json_base) json_base=v9.9.8 ;;
      ba_any) ba_any=0 ;;
      ba_ver) ba_ver=0.0.2+agent24.wrong ;;
      ba_commit) ba_commit=2222222222222222222222222222222222222222 ;;
      anydoc) stub_any=0 ;;
      tag) stub_cfg=weknora-app:wrong ;;
      json_patch) patch_json=NOT-THE-ID ;;
      patch_comma) pkind=comma ;;
      patch_order) patch_json="T-LOCAL-2,T-LOCAL-1" ;;
      patch_type) pkind=number ;;
      ba_go) go_ver=go1.22.0 ;;
      built_mismatch) built_at=2026-01-02T00:00:00Z ;;
      cfg_param) ;;
      *) echo "未知用例 ${mode}" >&2; exit 1 ;;
    esac
    jq -n \
      --arg revision "$rev_json" \
      --arg image_id "$image_id" \
      --arg tag "$tag" \
      --arg version "$json_ver" \
      --arg base_release "$json_base" \
      --arg patch "$patch_json" \
      --arg built "$built_at" \
      --arg bt "$bt_arg" \
      --arg commit "$ba_commit" \
      --arg any "$ba_any" \
      --arg bver "$ba_ver" \
      --arg go "$go_ver" \
      --arg pkind "$pkind" \
      '{revision:$revision, image_id:$image_id, tag:$tag, version:$version,
        base_release:$base_release,
        patch_ids:(
          if $pkind == "comma" then [$patch]
          elif $pkind == "number" then [1]
          elif $patch == "" then []
          else ($patch | split(","))
          end),
        built_at_utc:$built,
        build_args:({WITH_ANYDOC:$any, VERSION_ARG:$bver, COMMIT_ID_ARG:$commit, BUILD_TIME_ARG:$bt}
          + (if $go == "" then {} else {GO_VERSION_ARG:$go} end))}' \
      >"$d/app.json" || exit 1
    docker() {
      case "$*" in
        *'{{.Config.Image}}'*) printf '%s\n' "$stub_cfg" ;;
        *'{{.Image}}'*) printf '%s\n' "$image_id" ;;
        *org.opencontainers.image.revision*) printf '%s\n' "$stub_rev" ;;
        *io.idoris.agent24.base-release*) printf '%s\n' "$stub_base" ;;
        *io.idoris.agent24.patch-ids*) printf '%s\n' "$stub_patch" ;;
        *io.idoris.agent24.with-anydoc*) printf '%s\n' "$stub_any" ;;
        *io.idoris.agent24.version*) printf '%s\n' "$stub_ver" ;;
        *) printf '未预置的 docker 调用：%s\n' "$*" >&2; return 1 ;;
      esac
    }
    img=$(docker inspect -f '{{.Config.Image}}' app)
    got_id=$(docker inspect -f '{{.Image}}' app)
    if [ "$mode" != image_id ]; then use_id=$got_id; fi
    cfg_arg=$img
    if [ "$mode" = cfg_param ]; then
      cfg_arg=wrong-config-image:param
      img=$tag
    fi
    probe_verify_local_app "$d/app.json" "$use_id" "$cfg_arg" docker
  ) >"$d/log" 2>"$d/err"
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad=1
    printf '不符 %s：期望 rc=%s，实际 rc=%s\n' "$name" "$want" "$rc"
    show_err
    return
  fi
  if [ -n "$needle" ] && ! grep -F -q -- "$needle" "$d/err"; then
    bad=1
    printf '不符 %s：stderr 不含 %s\n' "$name" "$needle"
    show_err
  fi
}

verify_case ok 0 "json 与 label 全部一致"
# 每例的关键句只出现在对应那条核对的报错里，删掉任一条核对都会有一例不符。
verify_case json_rev 3 "json revision 与 HEAD 不符" "agent24-local-app.json 的 revision="
verify_case image_id 3 "image_id 不符" "app 容器镜像 ID 与 agent24-local-app.json 的 image_id 不符"
verify_case label_rev 3 "label revision 不符" "app 镜像 label revision="
verify_case patch_label 3 "patch-ids label 不符" "app 镜像 label patch-ids=NOT-THE-ID"
verify_case patch_label_spaced 3 "patch-ids label 用逗号加空格分隔" "app 镜像 label patch-ids=T-LOCAL-1, T-LOCAL-2"
verify_case anydoc 3 "with-anydoc 不是 1" "app 镜像 label with-anydoc=0"
verify_case ver_label 3 "label version 不符" "app 镜像 label version=0.0.0"
verify_case tag 3 "tag 与 Config.Image 不符" "容器 Config.Image="
verify_case base_label 3 "label base-release 不符" "app 镜像 label base-release=v9.9.9"
verify_case json_ver 3 "json version 不符" "agent24-local-app.json 的 version=0.0.1"
verify_case json_base 3 "json base_release 不符" "agent24-local-app.json 的 base_release=v9.9.8"
verify_case ba_any 3 "json build_args.WITH_ANYDOC 不是 1" "build_args.WITH_ANYDOC=0"
verify_case ba_ver 3 "json build_args.VERSION_ARG 不符" "build_args.VERSION_ARG=0.0.2"
verify_case ba_commit 3 "json build_args.COMMIT_ID_ARG 不符" "build_args.COMMIT_ID_ARG=2222"
verify_case json_patch 3 "json patch_ids 与 label 不符" "agent24-local-app.json 的 patch_ids="
verify_case patch_type 3 "json patch_ids 不是字符串数组" "不是字符串数组"
verify_case patch_comma 3 "json patch_ids 有元素含有逗号" "有元素含有逗号"
verify_case patch_order 3 "json patch_ids 顺序不同" "顺序与 REG_IDS_CSV 不同"
verify_case ba_go 3 "json build_args 多了 GO_VERSION_ARG" "build_args 的键必须正好是 WITH_ANYDOC、VERSION_ARG、COMMIT_ID_ARG、BUILD_TIME_ARG"
verify_case built_mismatch 3 "json built_at_utc 与 BUILD_TIME_ARG 不同" "built_at_utc=2026-01-02T00:00:00Z"
verify_case cfg_param 3 "ConfigImage 参数错" "容器 Config.Image=wrong-config-image:param"

info_case() {
  local mode=$1 want=$2 name=$3 needle=${4:-}
  n=$((n + 1))
  d=$BASE/c$n
  mkdir -p "$d/out" "$d/stack" || { bad=1; printf '不符 %s：建目录失败\n' "$name"; return; }
  (
    OUT=$d/out
    STACK_DIR=$d/stack
    PROBE_EMBED=none
    PROBE_APP_IMAGE=local
    # shellcheck disable=SC1091
    source "$r/scripts/agent24/probe/lib.sh"
    LOCAL_BUILD_JSON=$(jq -n --arg t "2026-01-01T00:00:00Z" '{build_args:{BUILD_TIME_ARG:$t}}')
    commit=$HEAD_REV
    version="${REL_TAG#v}+agent24.${TESTED_SHA}"
    btime=2026-01-01T00:00:00Z
    case $mode in
      ok) ;;
      prefix) commit=${HEAD_REV:0:7} ;;
      commit) commit=0000000000000000000000000000000000000000 ;;
      version) version=0.0.0+agent24.wrong ;;
      btime) btime=1999-01-01T00:00:00Z ;;
      *) echo "未知用例 ${mode}" >&2; exit 1 ;;
    esac
    probe_check_system_info_local "$commit" "$version" "$btime"
  ) >"$d/log" 2>"$d/err"
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad=1
    printf '不符 %s：期望 rc=%s，实际 rc=%s\n' "$name" "$want" "$rc"
    show_err
    return
  fi
  if [ -n "$needle" ] && ! grep -F -q -- "$needle" "$d/err"; then
    bad=1
    printf '不符 %s：stderr 不含 %s\n' "$name" "$needle"
    show_err
  fi
}

info_case ok 0 "system/info 本地全对"
info_case prefix 3 "system/info commit 只有 7 位前缀" "与当前 HEAD"
info_case commit 3 "system/info commit 错误" "commit_id=0000000000000000000000000000000000000000"
info_case version 3 "system/info version 错误" "version=0.0.0+agent24.wrong"
info_case btime 3 "system/info build_time 错误" "build_time=1999-01-01T00:00:00Z"

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d/out" "$d/stack" || { bad=1; echo "不符 precheck 接线：建目录失败"; }
if [ "$bad" -eq 0 ] || [ -d "$d/out" ]; then
  (
    OUT=$d/out
    STACK_DIR=$d/stack
    PROBE_EMBED=none
    PROBE_APP_IMAGE=local
    # shellcheck disable=SC1091
    source "$r/scripts/agent24/probe/lib.sh"
    declare -f precheck
    echo '---CHECK_SYSTEM_INFO---'
    declare -f check_system_info
  ) >"$d/log" 2>"$d/err"
  rc=$?
  if [ "$rc" -ne 0 ]; then
    bad=1
    printf '不符 precheck 接线：source rc=%s\n' "$rc"
    show_err
  elif ! grep -F -q 'probe_verify_local_app "$lj" "$id" "$img"' "$d/log"; then
    bad=1
    echo "不符 precheck 接线：调用参数不含容器 Config.Image 变量 img"
  elif ! grep -F -q '{{.Config.Image}}' "$d/log"; then
    bad=1
    echo "不符 precheck 接线：precheck 里没有容器 Config.Image"
  elif ! grep -F -q 'digests+="app=${img}@${LOCAL_APP_IMAGE_ID};"' "$d/log"; then
    bad=1
    echo "不符 precheck 接线：没有用 LOCAL_APP_IMAGE_ID 写 digests"
  elif ! grep -F -q 'probe_check_system_info_local "$c" "$v" "$(bj .data.build_time)"' "$d/log"; then
    bad=1
    echo "不符 precheck 接线：check_system_info 没有按 commit、version、build_time 调用 probe_check_system_info_local"
  fi
fi

cp "$r/a.txt" "$BASE/a.orig" || { echo "FAIL 保存 a.txt 失败" >&2; exit 1; }
printf 'v2-dirty\n' >"$r/a.txt"
src_case local 0 "local 工作区改了 H 以外的已跟踪文件" "" "T-LOCAL-1"
cp "$BASE/a.orig" "$r/a.txt" || { echo "FAIL 恢复 a.txt 失败" >&2; exit 1; }

cp "$r/docs/agent24/UPSTREAM-BASELINE.json" "$BASE/baseline.orig" || { echo "FAIL 保存登记失败" >&2; exit 1; }
jq '. + {probe_selftest_dirty:true}' "$BASE/baseline.orig" >"$r/docs/agent24/UPSTREAM-BASELINE.json" \
  || { echo "FAIL 改登记失败" >&2; exit 1; }
src_case local 3 "local 登记文件未提交" "有未提交改动，含暂存区"
cp "$BASE/baseline.orig" "$r/docs/agent24/UPSTREAM-BASELINE.json" || { echo "FAIL 恢复登记失败" >&2; exit 1; }

printf 'x\n' >"$r/scripts/agent24/probe/untracked-selftest.txt"
src_case local 3 "local 探测目录有未跟踪文件" "有未跟踪文件（含被忽略的）"
rm -f "$r/scripts/agent24/probe/untracked-selftest.txt"

g update-index --skip-worktree -- scripts/agent24/probe/lib.sh
src_case local 3 "local lib.sh 带 skip-worktree" "skip-worktree 或 assume-unchanged"
g update-index --no-skip-worktree -- scripts/agent24/probe/lib.sh

g update-index --assume-unchanged -- scripts/agent24/probe/lib.sh
src_case local 3 "local lib.sh 带 assume-unchanged" "skip-worktree 或 assume-unchanged"
g update-index --no-assume-unchanged -- scripts/agent24/probe/lib.sh

printf 'ignored-selftest.txt\n' >>"$r/.git/info/exclude"
printf 'x\n' >"$r/scripts/agent24/probe/ignored-selftest.txt"
src_case local 3 "local 探测目录有被忽略的文件" "有未跟踪文件（含被忽略的）"
rm -f "$r/scripts/agent24/probe/ignored-selftest.txt"

# 只在暂存区不同：工作区已回到 HEAD。
cp "$r/scripts/agent24/probe/lib.sh" "$BASE/lib.orig" || { echo "FAIL 保存 lib.sh 失败" >&2; exit 1; }
printf '# staged-only\n' >>"$r/scripts/agent24/probe/lib.sh"
g add -- scripts/agent24/probe/lib.sh
cp "$BASE/lib.orig" "$r/scripts/agent24/probe/lib.sh" || { echo "FAIL 恢复 lib.sh 失败" >&2; exit 1; }
src_case local 3 "local lib.sh 只有暂存区改动" "有未提交改动，含暂存区"
g reset -q -- scripts/agent24/probe/lib.sh

g update-index --chmod=+x -- scripts/agent24/probe/patches.sh
src_case local 3 "local patches.sh 只在暂存区改了权限" "有未提交改动，含暂存区"
g reset -q -- scripts/agent24/probe/patches.sh

printf 'x\n' >"$r/scripts/agent24/probe/staged-new.txt"
g add -- scripts/agent24/probe/staged-new.txt
rm -f "$r/scripts/agent24/probe/staged-new.txt"
src_case local 3 "local 新文件已暂存但工作区已删" "有未提交改动，含暂存区"
g reset -q -- scripts/agent24/probe/staged-new.txt

g config core.fileMode false
chmod +x "$r/scripts/agent24/probe/patches.sh"
src_case local 3 "local core.fileMode=false 时改了权限" "有未提交改动，含暂存区"
chmod -x "$r/scripts/agent24/probe/patches.sh"
g config --unset core.fileMode

if ! { printf 'x\n' >"$r/b.txt" && g add -A && g commit -q -m unregistered; } >"$BASE/third.log" 2>&1; then
  echo "FAIL 第三个提交失败" >&2
  sed 's/^/  /' "$BASE/third.log" >&2
  exit 1
fi
src_case official 3 "official 有未登记 b.txt" "b.txt"
src_case local 3 "local 有未登记 b.txt" "b.txt"

# 官方模式放行：HEAD 与 release 无命名空间外差异。登记了两个补丁，但路径没有改动。
crepo=$BASE/crepo
mkdir -p "$crepo/scripts/agent24/probe" "$crepo/docs/agent24" || { echo "FAIL 建 crepo 失败" >&2; exit 1; }
cp "$HERE/lib.sh" "$HERE/patches.sh" "$crepo/scripts/agent24/probe/" || { echo "FAIL 拷 crepo 脚本失败" >&2; exit 1; }
printf 'a\n' >"$crepo/clean-a.txt"
printf 'b\n' >"$crepo/clean-b.txt"
printf '%s\n' '{"current_baseline":{"release":{"tag":"v0.8.3","commit":"0000000000000000000000000000000000000000"},"product":{"product_patches":[{"id":"T-CLEAN-1","paths":["clean-a.txt"]},{"id":"T-CLEAN-2","paths":["clean-b.txt"]}]}}}' \
  >"$crepo/docs/agent24/UPSTREAM-BASELINE.json"
git_at "$crepo" init -q || { echo "FAIL crepo init 失败" >&2; exit 1; }
git_at "$crepo" add -A || { echo "FAIL crepo add 失败" >&2; exit 1; }
git_at "$crepo" commit -q -m release || { echo "FAIL crepo commit 失败" >&2; exit 1; }
c1=$(git_at "$crepo" rev-parse HEAD) || { echo "FAIL crepo rev 失败" >&2; exit 1; }
jq --arg c "$c1" '.current_baseline.release.commit = $c' "$crepo/docs/agent24/UPSTREAM-BASELINE.json" >"$crepo/docs/agent24/UPSTREAM-BASELINE.json.tmp" \
  || { echo "FAIL crepo 改登记失败" >&2; exit 1; }
mv "$crepo/docs/agent24/UPSTREAM-BASELINE.json.tmp" "$crepo/docs/agent24/UPSTREAM-BASELINE.json"
git_at "$crepo" add -A || { echo "FAIL crepo add2 失败" >&2; exit 1; }
git_at "$crepo" commit -q -m baseline-only || { echo "FAIL crepo commit2 失败" >&2; exit 1; }

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d/out" "$d/stack" || { bad=1; echo "不符 official 无命名空间外差异：建目录失败"; }
(
  OUT=$d/out
  STACK_DIR=$d/stack
  PROBE_EMBED=none
  PROBE_APP_IMAGE=official
  # shellcheck disable=SC1091
  source "$crepo/scripts/agent24/probe/lib.sh"
  printf 'REG_IDS_CSV=%s\n' "$REG_IDS_CSV"
) >"$d/log" 2>"$d/err"
rc=$?
if [ "$rc" -ne 0 ]; then
  bad=1
  printf '不符 official 无命名空间外差异：期望 rc=0，实际 rc=%s\n' "$rc"
  show_err
elif ! grep -F -x -q 'REG_IDS_CSV=' "$d/log"; then
  bad=1
  echo "不符 official 无命名空间外差异：NS_HAS_DIFF=0 时 REG_IDS_CSV 应为空"
  sed 's/^/  /' "$d/log"
fi

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d/out" "$d/stack" || { bad=1; echo "不符 local 无差异 note：建目录失败"; }
(
  OUT=$d/out
  STACK_DIR=$d/stack
  PROBE_EMBED=none
  PROBE_APP_IMAGE=local
  # shellcheck disable=SC1091
  source "$crepo/scripts/agent24/probe/lib.sh"
  printf '%s\n' "${TESTED_DIR_NOTE:-}"
) >"$d/log" 2>"$d/err"
rc=$?
if [ "$rc" -ne 0 ]; then
  bad=1
  printf '不符 local 无差异 REG_IDS 为空：期望 rc=0，实际 rc=%s\n' "$rc"
  show_err
elif ! grep -F -q '（无）' "$d/log"; then
  bad=1
  echo "不符 local 无差异 REG_IDS 为空：TESTED_DIR_NOTE 不含（无）"
  sed 's/^/  /' "$d/log"
fi

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d/out" "$d/stack" || { bad=1; echo "不符 空 patch_ids：建目录失败"; }
(
  OUT=$d/out
  STACK_DIR=$d/stack
  PROBE_EMBED=none
  PROBE_APP_IMAGE=local
  # shellcheck disable=SC1091
  source "$crepo/scripts/agent24/probe/lib.sh"
  stub_ver="${REL_TAG#v}+agent24.${TESTED_SHA}"
  tag="weknora-app:agent24-${TESTED_SHA}"
  image_id="sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  jq -n \
    --arg revision "$HEAD_REV" \
    --arg image_id "$image_id" \
    --arg tag "$tag" \
    --arg version "$stub_ver" \
    --arg base "$REL_TAG" \
    --arg built "2026-01-01T00:00:00Z" \
    '{revision:$revision, image_id:$image_id, tag:$tag, version:$version, base_release:$base,
      patch_ids:[""], built_at_utc:$built,
      build_args:{WITH_ANYDOC:"1", VERSION_ARG:$version, COMMIT_ID_ARG:$revision, BUILD_TIME_ARG:$built}}' \
    >"$d/app.json" || exit 1
  docker() {
    case "$*" in
      *'{{.Config.Image}}'*) printf '%s\n' "$tag" ;;
      *'{{.Image}}'*) printf '%s\n' "$image_id" ;;
      *org.opencontainers.image.revision*) printf '%s\n' "$HEAD_REV" ;;
      *io.idoris.agent24.base-release*) printf '%s\n' "$REL_TAG" ;;
      *io.idoris.agent24.patch-ids*) printf '%s\n' "" ;;
      *io.idoris.agent24.with-anydoc*) printf '%s\n' 1 ;;
      *io.idoris.agent24.version*) printf '%s\n' "$stub_ver" ;;
      *) printf '未预置的 docker 调用：%s\n' "$*" >&2; return 1 ;;
    esac
  }
  probe_verify_local_app "$d/app.json" "$image_id" "$tag" docker
) >"$d/log" 2>"$d/err"
rc=$?
if [ "$rc" -ne 3 ]; then
  bad=1
  printf '不符 空 REG_IDS_CSV 不接受 [""]：期望 rc=3，实际 rc=%s\n' "$rc"
  show_err
elif ! grep -F -q '逐项不一致' "$d/err"; then
  bad=1
  echo "不符 空 REG_IDS_CSV 不接受 [""]：stderr 不含逐项不一致"
  show_err
fi

# 登记两个补丁，HEAD 只改了其中一个的路径。
idrepo=$BASE/idrepo
mkdir -p "$idrepo/scripts/agent24/probe" "$idrepo/docs/agent24" || { echo "FAIL 建 idrepo 失败" >&2; exit 1; }
cp "$HERE/lib.sh" "$HERE/patches.sh" "$idrepo/scripts/agent24/probe/" || { echo "FAIL 拷 idrepo 脚本失败" >&2; exit 1; }
printf 'a\n' >"$idrepo/only-a.txt"
printf 'b\n' >"$idrepo/only-b.txt"
printf '%s\n' '{"current_baseline":{"release":{"tag":"v0.8.3","commit":"0000000000000000000000000000000000000000"},"product":{"product_patches":[{"id":"T-ONLY-1","paths":["only-a.txt"]},{"id":"T-ONLY-2","paths":["only-b.txt"]}]}}}' \
  >"$idrepo/docs/agent24/UPSTREAM-BASELINE.json"
git_at "$idrepo" init -q || { echo "FAIL idrepo init 失败" >&2; exit 1; }
git_at "$idrepo" add -A || { echo "FAIL idrepo add 失败" >&2; exit 1; }
git_at "$idrepo" commit -q -m release || { echo "FAIL idrepo commit 失败" >&2; exit 1; }
c1=$(git_at "$idrepo" rev-parse HEAD) || { echo "FAIL idrepo rev 失败" >&2; exit 1; }
jq --arg c "$c1" '.current_baseline.release.commit = $c' "$idrepo/docs/agent24/UPSTREAM-BASELINE.json" >"$idrepo/docs/agent24/UPSTREAM-BASELINE.json.tmp" \
  || { echo "FAIL idrepo 改登记失败" >&2; exit 1; }
mv "$idrepo/docs/agent24/UPSTREAM-BASELINE.json.tmp" "$idrepo/docs/agent24/UPSTREAM-BASELINE.json"
printf 'a2\n' >"$idrepo/only-a.txt"
git_at "$idrepo" add -A || { echo "FAIL idrepo add2 失败" >&2; exit 1; }
git_at "$idrepo" commit -q -m only-a || { echo "FAIL idrepo commit2 失败" >&2; exit 1; }

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d/out" "$d/stack" || { bad=1; echo "不符 登记 id 只取差异：建目录失败"; }
(
  OUT=$d/out
  STACK_DIR=$d/stack
  PROBE_EMBED=none
  PROBE_APP_IMAGE=local
  # shellcheck disable=SC1091
  source "$idrepo/scripts/agent24/probe/lib.sh"
  printf 'REG_IDS_CSV=%s\n' "$REG_IDS_CSV"
) >"$d/log" 2>"$d/err"
rc=$?
if [ "$rc" -ne 0 ]; then
  bad=1
  printf '不符 登记 id 只取差异里出现的补丁：期望 rc=0，实际 rc=%s\n' "$rc"
  show_err
elif ! grep -F -x -q 'REG_IDS_CSV=T-ONLY-1' "$d/log"; then
  bad=1
  echo "不符 登记 id 只取差异里出现的补丁：REG_IDS_CSV 不是 T-ONLY-1"
  sed 's/^/  /' "$d/log"
fi

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d" || { bad=1; echo "不符 jq 读 product_patches：建目录失败"; }
(
  ROOT=$r
  BASELINE=$d/bad.json
  REL_TAG=v0.8.3
  REL_COMMIT=$(git -C "$r" rev-parse HEAD)
  printf '{\n' >"$BASELINE"
  # shellcheck disable=SC1091
  source "$r/scripts/agent24/probe/patches.sh"
  probe_namespace_diff
) >"$d/log" 2>"$d/err"
rc=$?
if [ "$rc" -ne 3 ]; then
  bad=1
  printf '不符 jq 读 product_patches 失败：期望 rc=3，实际 rc=%s\n' "$rc"
  show_err
elif ! grep -F -q '读 product_patches 失败' "$d/err"; then
  bad=1
  echo "不符 jq 读 product_patches 失败：stderr 不含该句"
  show_err
fi

# product_patches 为 "none"（不是数组）当空集。无命名空间外差异时 source 为 0。
nonerepo=$BASE/nonerepo
mkdir -p "$nonerepo/scripts/agent24/probe" "$nonerepo/docs/agent24" || { echo "FAIL 建 nonerepo 失败" >&2; exit 1; }
cp "$HERE/lib.sh" "$HERE/patches.sh" "$nonerepo/scripts/agent24/probe/" || { echo "FAIL 拷 nonerepo 脚本失败" >&2; exit 1; }
printf '%s\n' '{"current_baseline":{"release":{"tag":"v0.8.3","commit":"0000000000000000000000000000000000000000"},"product":{"product_patches":"none"}}}' \
  >"$nonerepo/docs/agent24/UPSTREAM-BASELINE.json"
git_at "$nonerepo" init -q || { echo "FAIL nonerepo init 失败" >&2; exit 1; }
git_at "$nonerepo" add -A || { echo "FAIL nonerepo add 失败" >&2; exit 1; }
git_at "$nonerepo" commit -q -m release || { echo "FAIL nonerepo commit 失败" >&2; exit 1; }
c1=$(git_at "$nonerepo" rev-parse HEAD) || { echo "FAIL nonerepo rev 失败" >&2; exit 1; }
jq --arg c "$c1" '.current_baseline.release.commit = $c' "$nonerepo/docs/agent24/UPSTREAM-BASELINE.json" >"$nonerepo/docs/agent24/UPSTREAM-BASELINE.json.tmp" \
  || { echo "FAIL nonerepo 改登记失败" >&2; exit 1; }
mv "$nonerepo/docs/agent24/UPSTREAM-BASELINE.json.tmp" "$nonerepo/docs/agent24/UPSTREAM-BASELINE.json"
git_at "$nonerepo" add -A || { echo "FAIL nonerepo add2 失败" >&2; exit 1; }
git_at "$nonerepo" commit -q -m baseline-only || { echo "FAIL nonerepo commit2 失败" >&2; exit 1; }

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d/out" "$d/stack" || { bad=1; echo "不符 product_patches 为 none：建目录失败"; }
(
  OUT=$d/out
  STACK_DIR=$d/stack
  PROBE_EMBED=none
  PROBE_APP_IMAGE=official
  # shellcheck disable=SC1091
  source "$nonerepo/scripts/agent24/probe/lib.sh"
  printf 'REG_IDS_CSV=%s\n' "$REG_IDS_CSV"
) >"$d/log" 2>"$d/err"
rc=$?
if [ "$rc" -ne 0 ]; then
  bad=1
  printf '不符 product_patches 为 none 当空集：期望 rc=0，实际 rc=%s\n' "$rc"
  show_err
elif ! grep -F -x -q 'REG_IDS_CSV=' "$d/log"; then
  bad=1
  echo "不符 product_patches 为 none 当空集：REG_IDS_CSV 应为空"
  sed 's/^/  /' "$d/log"
fi

n=$((n + 1))
if ! grep -F -q 'probe_require_harness_at_head || exit 3' "$HERE/build-local-app.sh"; then
  bad=1
  echo "不符 build-local-app.sh 调用 probe_require_harness_at_head"
fi

n=$((n + 1))
if ! grep -F -q -- '--label "io.idoris.agent24.patch-ids=${REG_IDS_CSV}"' "$HERE/build-local-app.sh"; then
  bad=1
  echo "不符 build-local-app.sh 的 label patch-ids 不是 REG_IDS_CSV"
fi

n=$((n + 1))
if ! grep -F -x -q "  patch_ids='[]'" "$HERE/build-local-app.sh" \
  || ! grep -F -q "patch_ids=\$(jq -cn --arg csv \"\$REG_IDS_CSV\" '\$csv | split(\",\")')" "$HERE/build-local-app.sh"; then
  bad=1
  echo "不符 build-local-app.sh 的 patch_ids：空集应为 []，否则由 REG_IDS_CSV 按逗号拆"
fi

# product_patches 的 id 必须是互不相同、非空、不含逗号的字符串。
for bad_ids in '[{"paths":["a.txt"]}]' '[{"id":"","paths":["a.txt"]}]' '[{"id":"A,B","paths":["a.txt"]}]' \
  '[{"id":"A","paths":["a.txt"]},{"id":"A","paths":["a.txt"]}]' '[{"id":5,"paths":["a.txt"]}]'; do
  n=$((n + 1))
  d=$BASE/c$n
  mkdir -p "$d" || { bad=1; echo "不符 id 校验：建目录失败"; continue; }
  (
    ROOT=$r
    BASELINE=$d/ids.json
    REL_TAG=v0.8.3
    REL_COMMIT=$(git -C "$r" rev-parse HEAD)
    jq -n --argjson p "$bad_ids" '{current_baseline: {product: {product_patches: $p}}}' >"$BASELINE"
    # shellcheck disable=SC1091
    source "$r/scripts/agent24/probe/patches.sh"
    probe_namespace_diff
  ) >"$d/log" 2>"$d/err"
  rc=$?
  if [ "$rc" -ne 3 ] || ! grep -F -q 'id 必须是互不相同、非空、不含逗号的字符串' "$d/err"; then
    bad=1
    printf '不符 product_patches id 非法 %s：期望 rc=3 且报 id 规则，实际 rc=%s\n' "$bad_ids" "$rc"
    show_err
  fi
done

if [ "$n" -eq 0 ]; then
  echo "FAIL 自测总例数为 0" >&2
  exit 1
fi
if [ "$bad" -eq 0 ]; then
  printf '%s 例全部符合\n' "$n"
else
  printf '%s 例里有不符\n' "$n" >&2
  exit 1
fi
