#!/usr/bin/env bash
# KB-CT-01 PR② 探测入口。用法：scripts/agent24/probe/run.sh [项号 …]；不带参数跑已提供的全部项。说明见 README.md。
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
. "$HERE/lib.sh"
# shellcheck source=probe-09-health.sh
. "$HERE/probe-09-health.sh"
if [ -f "$HERE/probes.sh" ]; then
  # shellcheck source=probes.sh
  . "$HERE/probes.sh"
fi

# 不管参数怎么传，统一按项号升序执行：第 11 项会撤 key、降权、移除成员，必须排在用到这些数据的项之后。
available=""
for i in $(seq 1 16); do
  if declare -F "p$i" >/dev/null; then available+="$i "; fi
done
# 参数逐个校验：只接受十进制项号（前导 0 按十进制处理），空串、9abc 之类一律以 2 拒绝；再按项号升序去重。
ITEMS=""
if [ $# -gt 0 ]; then
  for a in "$@"; do
    if ! [[ $a =~ ^[0-9]+$ ]] || ! declare -F "p$((10#$a))" >/dev/null; then
      echo "没有第 '$a' 项（本目录提供的探测项：${available% }）" >&2
      exit 2
    fi
    ITEMS+="$((10#$a)) "
  done
else
  ITEMS=$available
fi
ITEMS=$(printf '%s\n' $ITEMS | sort -n -u | tr '\n' ' ')
ITEMS=${ITEMS% }

FULL=no
[ "$ITEMS" = "$(echo $(seq 1 16))" ] && FULL=yes
# 不管本次写到哪里，都先处理正式证据目录（probe-evidence/<受测 SHA>）旁边 SIGKILL 留下的残留（见 lib.sh 的 recover_out）。
recover_out "$FORMAL_OUT"
# 只有"全量 + PROBE_EMBED=fake-ollama + 没有显式指定 OUT"才写正式证据目录；其他情况一律写临时目录。
if [ -z "$OUT_EXPLICIT" ]; then
  if [ "$FULL" != yes ] || [ "$EMBED" != fake-ollama ]; then
    tmpd=${TMPDIR:-/tmp}
    OUT=$(mktemp -d "${tmpd%/}/weknora-probe-partial.XXXXXX") || exit 1
    TMP_OUT=$OUT   # 交给 cleanup：中途失败、目录还是空的就删掉
    echo "本次不是\"全量 + PROBE_EMBED=fake-ollama\"（项：${ITEMS}；模式：${EMBED}）：证据写到 ${OUT}，不碰正式证据目录"
  fi
fi
# OUT 会被整体替换：里面有非证据文件，或显式指定 OUT 却只跑部分项而 OUT 非空，就以 2 拒绝。
check_out "$FULL"

precheck
setup
for i in $ITEMS; do
  echo "第 $i 项"
  "p$i"
done
finalize
# 汇总各项结论；有"与矩阵不一致"时以 4 退出（证据照写）。
summarize || exit 4
