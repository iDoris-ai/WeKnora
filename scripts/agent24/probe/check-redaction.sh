#!/usr/bin/env bash
# 证据脱敏自检。按 REQUIRED_MODULES 检查并 source 同目录 redact-NN-*.sh。
# 每个编号必须恰好有一个文件。缺了就失败，不再静默少跑一组规则。
# 每个模块在末尾声明 MOD_DONE_NN=1 和 MOD_RULES_NN（本模块 report 的确切次数），逐模块核对。
# 模块里执行 exit（任何退出码）都判 FAIL、以 1 退出。
# 用法：check-redaction.sh [--skip-env] [证据目录]（参数顺序不限）
#   默认目录 docs/agent24/probe-evidence/<HEAD 的 8 位 SHA>。
#   读不到 $STACK_DIR/.env 时失败；只有显式传 --skip-env 才跳过 .env 比对。
set -uo pipefail
# C locale：BSD grep 在 UTF-8 locale 下遇到二进制字节会漏匹配。规则都是 ASCII。
export LC_ALL=C
# 叠放的中间片会按本片已有模块改这一行（必须含 00 和 10；目录里有而列表里没有的模块号也会失败）。
REQUIRED_MODULES="00 10 30"
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
HERE=$(cd "$(dirname "$0")" && pwd)
SKIP_ENV=false
DIR_ARG=""
for arg in "$@"; do
  if [ "$arg" = --skip-env ]; then
    SKIP_ENV=true
  elif [ -z "$DIR_ARG" ]; then
    DIR_ARG=$arg
  else
    echo "多余的参数：$arg" >&2
    exit 2
  fi
done
DIR=${DIR_ARG:-$ROOT/docs/agent24/probe-evidence/$(git -C "$ROOT" rev-parse --short=8 HEAD)}
ENVF=${STACK_DIR:-$HOME/weknora-probe-stack}/.env
bad=0
if [ ! -d "$DIR" ]; then
  if [ -z "$DIR_ARG" ]; then
    echo "SKIP 默认证据目录不存在：$DIR"
    echo "     要自检，请把证据目录作为参数传入，"
    echo "     例如 run.sh 只跑部分项时打印的临时目录：check-redaction.sh /tmp/weknora-probe-partial.XXXXXX"
    echo "     本次没有检查任何文件。"
    exit 0
  fi
  echo "证据目录不存在：$DIR" >&2
  exit 2
fi

W=$(mktemp -d "${TMPDIR:-/tmp}/weknora-redaction.XXXXXX") || exit 2
# 驱动自己的每个结论都先置 _drv_end=1。没置位就退出，说明是模块里执行了 exit（不论退出码）：
# 一律 FAIL 并以 1 退出，不让 exit 0 越过哨兵、次数核对和最终结论。
_drv_end=0
drv_exit() { _drv_end=1; exit "$1"; }
trap '_drv_st=$?; rm -rf "$W"; if [ "$_drv_end" != 1 ]; then echo "FAIL 驱动未正常结束（模块提前退出）"; exit 1; fi; exit "$_drv_st"' EXIT

# bash 3.2 在 set -u 下展开空数组会报未绑定；先看个数，非空才展开。bash 5 同样安全。
mods=()
missing=0
# 00（公共函数）和 10（基础规则）每一片都有，列表里少了它们就失败。
for nn in 00 10; do
  case " $REQUIRED_MODULES " in
    *" $nn "*) ;;
    *) echo "FAIL 缺少脱敏模块 ${nn}（REQUIRED_MODULES 必须包含 00 和 10）"; missing=1 ;;
  esac
done
# 目录里有 redact-NN-*.sh 但 NN 不在列表里：多半是叠放时漏改了 REQUIRED_MODULES。
shopt -s nullglob
present=("$HERE"/redact-[0-9][0-9]-*.sh)
shopt -u nullglob
if [ "${#present[@]}" -gt 0 ]; then
  for m in "${present[@]}"; do
    bn=$(basename "$m")
    nn=${bn#redact-}
    nn=${nn%%-*}
    case " $REQUIRED_MODULES " in
      *" $nn "*) ;;
      *) echo "FAIL 脱敏模块 $nn 不在 REQUIRED_MODULES 里：$bn"; missing=1 ;;
    esac
  done
fi
for nn in $REQUIRED_MODULES; do
  shopt -s nullglob
  found=("$HERE"/redact-"$nn"-*.sh)
  shopt -u nullglob
  if [ "${#found[@]}" -eq 0 ]; then
    echo "FAIL 缺少脱敏模块 $nn"
    missing=1
    continue
  fi
  if [ "${#found[@]}" -ne 1 ]; then
    echo "FAIL 脱敏模块 $nn 不唯一"
    missing=1
    continue
  fi
  mods[${#mods[@]}]="${found[0]}"
done
if [ "$missing" -ne 0 ] || [ "${#mods[@]}" -eq 0 ]; then
  [ "$missing" -eq 0 ] && echo "FAIL 缺少脱敏模块"
  drv_exit 1
fi
names=
for m in "${mods[@]}"; do
  names="${names:+$names }$(basename "$m")"
done
echo "加载脱敏模块：$names"
mod_nn() { printf '%s\n' "$(basename "$1")" | sed -n 's/^redact-\([0-9][0-9]\)-.*/\1/p'; }
# 加载前先清掉所有模块的哨兵、声明次数和驱动记下的实际次数（环境里可能预设）。
for m in "${mods[@]}"; do
  nn=$(mod_nn "$m")
  unset "MOD_DONE_$nn" "MOD_RULES_$nn" "_drv_rn_$nn"
done
# 加载循环只负责 source 和检查返回码，再记下本模块的实际 report 次数。
# 模块顶层的 break/continue 会跳出或跳过这个循环，提前 return 会少跑规则；
# 所以哨兵和次数不在这里核对，而是循环结束后按模块列表逐个再核一遍。
for m in "${mods[@]}"; do
  bn=$(basename "$m")
  nn=$(mod_nn "$m")
  REPORT_N=0
  # shellcheck disable=SC1090
  source "$m"
  rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL 模块加载出错：$bn"
    drv_exit 1
  fi
  printf -v "_drv_rn_$nn" '%s' "$REPORT_N"
done
# 逐模块核对：本模块必须有完成哨兵、驱动记下了实际次数，且实际次数恰好等于声明的 MOD_RULES_NN。
for m in "${mods[@]}"; do
  bn=$(basename "$m")
  nn=$(mod_nn "$m")
  dv="MOD_DONE_$nn"
  rnv="_drv_rn_$nn"
  if [ "${!dv:-}" != 1 ] || [ -z "${!rnv:-}" ]; then
    echo "FAIL 模块未完成：${bn}（缺完成哨兵或没有走完加载循环，模块顶层有 break/continue/return？）"
    drv_exit 1
  fi
  REPORT_N=${!rnv}
  rvn="MOD_RULES_$nn"
  want=${!rvn:-}
  # 只认 1–4 位、无前导 0 的十进制数，比较用字符串相等，不经过 [ -lt ] 的整数范围。
  case $want in
    [0-9]|[1-9][0-9]|[1-9][0-9][0-9]|[1-9][0-9][0-9][0-9]) ;;
    *)
      echo "FAIL 模块未声明规则次数（MOD_RULES_${nn} 须是 0–9999 的整数）：$bn"
      drv_exit 1
      ;;
  esac
  if [ "$REPORT_N" != "$want" ]; then
    if [ "$REPORT_N" -lt "$want" ] 2>/dev/null; then
      echo "FAIL 规则未跑够：$bn 实际 ${REPORT_N}，声明 ${want}"
    else
      echo "FAIL 规则次数多于声明：$bn 实际 ${REPORT_N}，声明 ${want}"
    fi
    drv_exit 1
  fi
done

_drv_end=1
if [ "$bad" -eq 0 ]; then
  echo "脱敏自检通过：$DIR"
else
  echo "脱敏自检失败：$DIR" >&2
  exit 1
fi
