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
      *) echo "未知用例 ${mode}" >&2; exit 1 ;;
    esac
    jq -n \
      --arg revision "$rev_json" \
      --arg image_id "$image_id" \
      --arg tag "$tag" \
      --arg version "$json_ver" \
      --arg base_release "$json_base" \
      --arg patch "$patch_json" \
      --arg built "2026-01-01T00:00:00Z" \
      --arg commit "$ba_commit" \
      --arg any "$ba_any" \
      --arg bver "$ba_ver" \
      '{revision:$revision, image_id:$image_id, tag:$tag, version:$version,
        base_release:$base_release,
        patch_ids:(if $patch == "" then [] else ($patch | split(",")) end),
        built_at_utc:$built,
        build_args:{WITH_ANYDOC:$any, VERSION_ARG:$bver, COMMIT_ID_ARG:$commit, BUILD_TIME_ARG:$built}}' \
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
    probe_verify_local_app "$d/app.json" "$use_id" docker
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

if ! { printf 'x\n' >"$r/b.txt" && g add -A && g commit -q -m unregistered; } >"$BASE/third.log" 2>&1; then
  echo "FAIL 第三个提交失败" >&2
  sed 's/^/  /' "$BASE/third.log" >&2
  exit 1
fi
src_case official 3 "official 有未登记 b.txt" "b.txt"
src_case local 3 "local 有未登记 b.txt" "b.txt"

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
