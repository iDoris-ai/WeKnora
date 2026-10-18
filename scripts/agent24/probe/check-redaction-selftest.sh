#!/usr/bin/env bash
# 脱敏规则自测。按 REQUIRED_SELFTESTS 的顺序，在子 shell 里 source 同目录 selftest-NN-*.sh。
# 正例必须 rc=1，且规则名出现在 ^FAIL 行。反例必须 rc=0，且没有 FAIL 行。
# 列表里的编号缺文件或不唯一、目录里有列表外的 selftest-NN 文件、总例数为 0，都 FAIL。
# 用例文件里的 exit 只结束它自己的子 shell：例数、bad 和完成哨兵经 fd 3 写进临时文件交回，
# 子 shell 退出码非 0、没有交回结果、缺哨兵、例数不足，都 FAIL。
# STACK_DIR 指向本脚本的临时目录。全部是合成值，没有真实密钥。
# 用法：scripts/agent24/probe/check-redaction-selftest.sh
set -uo pipefail
# 与驱动的 REQUIRED_MODULES 同样做法：叠放的中间片按本片已有的用例文件改这一行。
REQUIRED_SELFTESTS="10 30"
HERE=$(cd "$(dirname "$0")" && pwd)
CHECK=$HERE/check-redaction.sh
BASE=$(mktemp -d "${TMPDIR:-/tmp}/weknora-redaction-selftest.XXXXXX") || exit 2
STACK_DIR=$BASE/stack
mkdir -p "$STACK_DIR"
export STACK_DIR
trap 'chmod -R u+rwx "$BASE" 2>/dev/null; rm -rf "$BASE"' EXIT

n=0
bad=0

# run_case 期望rc 名称 规则名 [文件名] [only] [输出必须含] [输出不得含]
# 夹具正文从标准输入读入。only=only 时，每条 FAIL 行都要含规则名。
run_case() {
  local want=$1 name=$2 rule=$3 file=${4:-f.txt} only=${5:-} must=${6:-} forbid=${7:-}
  local d rc extra
  n=$((n + 1))
  d=$BASE/c$n
  if ! { mkdir -p "$d" && cat >"$d/$file"; }; then
    bad=1
    printf '不符 %s：夹具写入失败\n' "$name"
    return
  fi
  "$CHECK" --skip-env "$d" >"$d/out" 2>&1
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad=1
    printf '不符 %s：期望 rc=%s，实际 rc=%s\n' "$name" "$want" "$rc"
    sed 's/^/  /' "$d/out"
    return
  fi
  if [ "$want" -eq 1 ]; then
    # 只在 FAIL 行里找规则名。ok 行也会带规则名，不能算命中。
    if ! grep '^FAIL' "$d/out" | grep -F -q -- "$rule"; then
      bad=1
      printf '不符 %s：FAIL 行里没有规则 %s\n' "$name" "$rule"
      sed 's/^/  /' "$d/out"
      return
    fi
    if [ "$only" = only ]; then
      extra=$(grep '^FAIL' "$d/out" | grep -v -F -- "$rule" || true)
      if [ -n "$extra" ]; then
        bad=1
        printf '不符 %s：除了 %s 还有别的 FAIL\n' "$name" "$rule"
        sed 's/^/  /' "$d/out"
        return
      fi
    fi
  elif grep -q '^FAIL' "$d/out"; then
    bad=1
    printf '不符 %s：反例出现 FAIL\n' "$name"
    sed 's/^/  /' "$d/out"
    return
  fi
  if [ -n "$must" ] && ! grep -F -q -- "$must" "$d/out"; then
    bad=1
    printf '不符 %s：输出里没有 %s\n' "$name" "$must"
    sed 's/^/  /' "$d/out"
    return
  fi
  if [ -n "$forbid" ] && grep -F -q -- "$forbid" "$d/out"; then
    bad=1
    printf '不符 %s：输出里出现了不该打印的内容\n' "$name"
    sed 's/^/  /' "$d/out"
  fi
}
pos() { run_case 1 "$2" "$1" "${3:-f.txt}" "" "" ""; }
pos_only() { run_case 1 "$2" "$1" "${3:-f.txt}" only "" ""; }
neg() { run_case 0 "$1" x "${2:-f.txt}" "" "" ""; }
pos_s() { pos "$1" "$2" "${4:-f.txt}" <<<"$3"; }
pos_only_s() { pos_only "$1" "$2" "${4:-f.txt}" <<<"$3"; }
neg_s() { neg "$1" "${3:-f.txt}" <<<"$2"; }

R_QUERY='URL query 敏感参数'
R_KV='非 JSON 敏感赋值'
R_HDR='凭据头带值'
R_FIELD='敏感字段带值'
R_SK='API key（sk- 前缀）'
R_ENC='API key 密文（enc:v1）'
R_JWT='JWT（eyJ 开头）'
R_BEARER='Bearer 凭据'
R_AUTH='Authorization 头'
R_HMAC='租户 HMAC'
R_PROBE='探测口令（probe-<24 位十六进制>）'
R_COOKIE='Cookie / Set-Cookie'
R_URL='URL 内嵌口令'
R_MAIL='非 @probe.invalid 的邮箱'
R_HOME='本机 $HOME'
R_USER='本机用户名 $USER'
R_HOST='本机主机名'
R_ENV='.env 密钥值比对'

# 框架自己的变量都以 _st_ 开头。用例文件跑在子 shell 里，改不到这些变量。
# bash 3.2 在 set -u 下展开空数组会报未绑定；先看个数，非空才展开。
_st_list=()
for _st_nn in $REQUIRED_SELFTESTS; do
  shopt -s nullglob
  _st_found=("$HERE"/selftest-"$_st_nn"-*.sh)
  shopt -u nullglob
  if [ "${#_st_found[@]}" -ne 1 ]; then
    bad=1
    echo "FAIL 自测用例文件 $_st_nn 缺失或不唯一（找到 ${#_st_found[@]} 个）" >&2
    continue
  fi
  _st_list[${#_st_list[@]}]=${_st_found[0]}
done
shopt -s nullglob
_st_all=("$HERE"/selftest-[0-9][0-9]*.sh)
shopt -u nullglob
if [ "${#_st_all[@]}" -gt 0 ]; then
  for _st_t in "${_st_all[@]}"; do
    _st_bn=$(basename "$_st_t")
    _st_nn=${_st_bn#selftest-}
    _st_nn=${_st_nn:0:2}
    case " $REQUIRED_SELFTESTS " in
      *" $_st_nn "*) ;;
      *) bad=1; echo "FAIL 自测用例文件不在 REQUIRED_SELFTESTS 里：$_st_bn" >&2 ;;
    esac
  done
fi
if [ "${#_st_list[@]}" -eq 0 ]; then
  echo "FAIL 没有自测用例文件" >&2
  exit 1
fi
# 取结果文件里 名字=值 的最后一行。
_st_get() { sed -n "s/^$1=//p" "$2" | tail -n 1; }
for _st_t in "${_st_list[@]}"; do
  _st_bn=$(basename "$_st_t")
  _st_nn=${_st_bn#selftest-}
  _st_nn=${_st_nn%%-*}
  # 加载前先定下本文件的哨兵名、下限名和结果文件。
  _st_done="SELFTEST_DONE_$_st_nn"
  _st_min="SELFTEST_MIN_$_st_nn"
  _st_res=$BASE/result.$_st_nn
  if ! /bin/bash -n "$_st_t"; then
    bad=1
    echo "FAIL 自测语法错误：$_st_bn" >&2
    continue
  fi
  _st_before=$n
  (
    bad=0
    # 每个文件用自己的夹具目录：前一个文件提前退出、没交回例数时，c$n 也不会撞上它留下的夹具。
    BASE=$BASE/st$_st_nn
    mkdir -p "$BASE" || exit 1
    # shellcheck disable=SC1090
    source "$_st_t"
    src_rc=$?
    { printf 'src_rc=%s\nn=%s\nbad=%s\n' "$src_rc" "$n" "$bad"
      set | grep '^SELFTEST_'
      echo 'end=1'; } >&3
  ) 3>"$_st_res"
  _st_rc=$?
  if [ "$_st_rc" -ne 0 ]; then
    bad=1
    echo "FAIL 自测子 shell 退出码非 0：$_st_bn rc=${_st_rc}" >&2
  fi
  if [ "$(_st_get end "$_st_res")" != 1 ]; then
    bad=1
    echo "FAIL 自测提前退出（没有交回结果）：$_st_bn" >&2
    continue
  fi
  if [ "$(_st_get src_rc "$_st_res")" != 0 ]; then
    bad=1
    echo "FAIL 自测加载出错：$_st_bn" >&2
  fi
  [ "$(_st_get bad "$_st_res")" = 0 ] || bad=1
  _st_after=$(_st_get n "$_st_res")
  case $_st_after in
    ''|*[!0-9]*) bad=1; echo "FAIL 自测例数不可读：$_st_bn" >&2; continue ;;
  esac
  n=$_st_after
  if [ "$(_st_get "$_st_done" "$_st_res")" != 1 ]; then
    bad=1
    echo "FAIL 自测未完成：$_st_bn" >&2
  fi
  _st_got=$((n - _st_before))
  _st_need=$(_st_get "$_st_min" "$_st_res")
  # 下限须是 ≥1 的整数：0 等于不设下限。超出整数范围时 [ -ge ] 出错，同样判 FAIL。
  case $_st_need in
    ''|*[!0-9]*)
      bad=1
      echo "FAIL 自测例数下限不是正整数：$_st_bn ${_st_min}=${_st_need:-未声明}" >&2
      continue
      ;;
  esac
  if ! [ "$_st_need" -ge 1 ] 2>/dev/null; then
    bad=1
    echo "FAIL 自测例数下限须 ≥1 且在整数范围内：$_st_bn ${_st_min}=${_st_need}" >&2
    continue
  fi
  if ! [ "$_st_got" -ge "$_st_need" ]; then
    bad=1
    echo "FAIL 自测例数不足：$_st_bn 实际 ${_st_got} 下限 ${_st_need}" >&2
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
