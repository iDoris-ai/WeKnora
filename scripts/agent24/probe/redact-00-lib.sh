#!/usr/bin/env bash
# 公共函数与敏感字段名。只定义 TEXT_TARGETS（默认是目录下全部文件），不跑规则。
# 占位符只认字面 <redacted>、<redacted:标签>（标签 [a-z0-9_]+）和纯 *。整值才算。
PH='(<redacted>|<redacted:[a-z0-9_]+>|\*+)'
FIELD_CORE='password|passwd|pwd|secret|token|credential|refresh|api[_-]?key|session[_-]?id|hmac(_secret)?|signature|invite_code|authorization|passphrase'
FIELD_NAME="[A-Za-z0-9_-]*(${FIELD_CORE})[A-Za-z0-9_-]*|[A-Za-z0-9_-]*key|auth|sig"
KV_NAME=$FIELD_NAME
# O1：左边界允许引号。键名后仍须是空白加 [:=]，所以 "key": 配不上。
KV_LINE='(^|[^A-Za-z0-9_])('"$KV_NAME"')[[:space:]]*[:=]'
Q_PRE='[?&]('"$KV_NAME"')='
Q_VAL='([^[:space:]"&#\\]|\\.){6,}'
HDR_PRE='(x-api-key|x-external-user-token|x-auth-token)\\*"?[[:space:]]*[:=][[:space:]]*\[?[[:space:]]*\\*"?'
HDR_VAL='[^][:space:],}"]{6,}'
HDR_SUF='\\*"?'
_sq="'"
FIELD_KEY_DQ="\"(${FIELD_NAME})\""
FIELD_KEY_SQ="${_sq}(${FIELD_NAME})${_sq}"
FIELD_RE="${FIELD_KEY_DQ}[[:space:]]*:[[:space:]]*\"[^\"]{6,}|${FIELD_KEY_SQ}[[:space:]]*:[[:space:]]*${_sq}[^${_sq}]{6,}"
ARRAY_RE="${FIELD_KEY_DQ}[[:space:]]*:[[:space:]]*\\[[^]]*\\]|${FIELD_KEY_SQ}[[:space:]]*:[[:space:]]*\\[[^]]*\\]"
FIELD_RULE='敏感字段带值（password/secret/token/credential/refresh/api key/pwd/session_id/hmac/signature/auth/sig/…）'
KV_RULE='非 JSON 敏感赋值（key=value / key: value）'
Q_RULE='URL query 敏感参数'
H_RULE='凭据头带值（X-API-Key / X-External-User-Token / X-Auth-Token）'
DV_SKIP_RE=
DV_VALUE_PRE=
DV_VALUE_SUF=

find "$DIR" -type f -print0 >"$W/files"
FIND_RC=$?
cp "$W/files" "$W/text.targets"
TEXT_TARGETS=$W/text.targets
: >"$W/dv.list"
: >"$W/field.jq.locs"

# 驱动按模块核对 REPORT_N。report 计一次；出错分支不 report 时用 rule_ran 补记，使次数与
# 输入无关。解码视图补扫只在命中时打印（report_print），但每次调用都 rule_ran 记一次。
rule_ran() { REPORT_N=$(( ${REPORT_N:-0} + 1 )); }
report() { rule_ran; report_print "$@"; }
report_print() {
  if [ -n "$2" ]; then
    bad=1
    echo "FAIL $1"
    sed 's/^/  /' <<<"$2"
  else
    echo "ok   $1"
  fi
}
unreadable() {
  local rc=$1
  if [ "$rc" -ne 0 ]; then
    rule_ran
    bad=1
    echo "FAIL 不可读文件（find 出错 rc=${rc}）"
    return
  fi
  : >"$W/locs"
  while IFS= read -r -d '' f; do
    [ -r "$f" ] || printf '%s\n' "$f" >>"$W/locs"
  done <"$W/files"
  report "不可读文件" "$(sort -u "$W/locs")"
}
grep_ok() {
  local name=$1 rc
  shift
  : >"$W/raw" || { bad=1; echo "FAIL ${name}（临时文件不可写）"; return 1; }
  "$@" >"$W/raw"
  rc=$?
  if [ "$rc" -ge 2 ]; then
    bad=1
    echo "FAIL ${name}（grep 出错 rc=${rc}）"
    return 1
  fi
  return 0
}
# 解码视图上用 grep -o 取片段，再按片段做占位符锚定（与原文那遍一致）。
dv_collect() {
  local name=$1 f rc line num path src match
  shift
  [ -s "$W/dv.list" ] || return 0
  while IFS= read -r -d '' f; do
    grep -aHno "$@" "$f" >"$W/dv.one"
    rc=$?
    if [ "$rc" -ge 2 ]; then
      bad=1
      echo "FAIL ${name}（grep 出错 rc=${rc}）"
      return 1
    fi
    [ -s "$W/dv.one" ] || continue
    src=$(cat "$f.src")
    while IFS= read -r line; do
      [ -n "$line" ] || continue
      num=${line#*:}
      num=${num%%:*}
      match=${line#*:*:}
      if [ -n "$DV_SKIP_RE" ] && printf '%s\n' "$match" | grep -qE "$DV_SKIP_RE"; then
        continue
      fi
      if [ -n "$DV_VALUE_PRE" ] && printf '%s\n' "$match" | grep -qE "^${DV_VALUE_PRE}${PH}${DV_VALUE_SUF}\$"; then
        continue
      fi
      path=$(sed -n "${num}p" "$f.map")
      printf '%s:%s\n' "$src" "$path" >>"$W/dv.locs"
    done <"$W/dv.one"
  done <"$W/dv.list"
}
scan() {
  local name=$1 locs
  shift
  grep_ok "$name" grep -rna "$@" "$DIR" || { rule_ran; return 0; }
  cut -d: -f1,2 <"$W/raw" | sort -u >"$W/locs"
  : >"$W/dv.locs"
  DV_SKIP_RE=
  DV_VALUE_PRE=
  DV_VALUE_SUF=
  dv_collect "$name" "$@" || { rule_ran; return 0; }
  locs=$(cat "$W/locs" "$W/dv.locs" | sed '/^$/d' | sort -u)
  report "$name" "$locs"
}
dv_rule() {
  local name=$1 locs
  shift
  : >"$W/dv.locs"
  DV_SKIP_RE=
  DV_VALUE_PRE=
  DV_VALUE_SUF=
  dv_collect "$name" "$@" || true
  locs=$(sed '/^$/d' "$W/dv.locs" | sort -u)
  rule_ran
  [ -z "$locs" ] || report_print "$name" "$locs"
}
is_placeholder() {
  printf '%s\n' "$1" | grep -qxE '<redacted>|<redacted:[a-z0-9_]+>|\*+'
}
unwrap_brackets() {
  local s=$1
  if [ "${s#[}" != "$s" ] && [ "${s%]}" != "$s" ]; then
    s=${s#[}
    s=${s%]}
  fi
  printf '%s' "$s"
}
trim_lead() {
  local s=$1
  while [ -n "$s" ]; do
    case $s in
      [[:space:]]*) s=${s#?} ;;
      *) break ;;
    esac
  done
  printf '%s' "$s"
}
# 0 = 要报。方式词后的空白全部去掉（允许多个空格）。
auth_value_bad() {
  local v low
  v=$(unwrap_brackets "$1")
  low=$(printf '%s' "$v" | tr '[:upper:]' '[:lower:]')
  case $low in
    'api-key '*) v=${v:7} ;;
    'apikey '*) v=${v:6} ;;
    'bearer '*) v=${v:6} ;;
    'basic '*) v=${v:5} ;;
    'digest '*) v=${v:6} ;;
    'token '*) v=${v:5} ;;
  esac
  v=$(trim_lead "$v")
  v=$(unwrap_brackets "$v")
  [ "${#v}" -ge 6 ] || return 1
  is_placeholder "$v" && return 1
  return 0
}
collect_value() {
  local name=$1 pre=$2 suf=$3 out=$4 line n rc
  [ -s "$W/raw" ] || return 0
  sed 's/^[^:]*:[0-9]*://' "$W/raw" >"$W/matches"
  grep -vEn "^${pre}${PH}${suf}\$" "$W/matches" >"$W/kept" || rc=$?
  if [ "${rc:-0}" -ge 2 ]; then bad=1; echo "FAIL ${name}（grep 出错 rc=${rc}）"; return 1; fi
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    n=${line%%:*}
    sed -n "${n}p" "$W/raw" | cut -d: -f1,2 >>"$out"
  done <"$W/kept"
}
scan_value() {
  local name=$1 pre=$2 val=$3 suf=${4:-} locs
  grep_ok "$name" grep -rnaoiE "$pre$val$suf" "$DIR" || { rule_ran; return 0; }
  : >"$W/locs"
  collect_value "$name" "$pre" "$suf" "$W/locs"
  : >"$W/dv.locs"
  DV_SKIP_RE=
  DV_VALUE_PRE=$pre
  DV_VALUE_SUF=$suf
  dv_collect "$name" -iE "$pre$val$suf" || { rule_ran; return 0; }
  DV_VALUE_PRE=
  DV_VALUE_SUF=
  locs=$(cat "$W/locs" "$W/dv.locs" | sed '/^$/d' | sort -u)
  report "$name" "$locs"
}
dv_value_rule() {
  local name=$1 pre=$2 val=$3 suf=${4:-} locs
  : >"$W/dv.locs"
  DV_SKIP_RE=
  DV_VALUE_PRE=$pre
  DV_VALUE_SUF=$suf
  dv_collect "$name" -iE "$pre$val$suf" || true
  DV_VALUE_PRE=
  DV_VALUE_SUF=
  locs=$(sed '/^$/d' "$W/dv.locs" | sort -u)
  rule_ran
  [ -z "$locs" ] || report_print "$name" "$locs"
}
grep_list() {
  local name=$1 list=$2 f rc
  shift 2
  : >"$W/raw"
  [ -s "$list" ] || return 0
  while IFS= read -r -d '' f; do
    grep -aHan "$@" "$f" >>"$W/raw"
    rc=$?
    if [ "$rc" -ge 2 ]; then
      bad=1
      echo "FAIL ${name}（grep 出错 rc=${rc}）"
      return 1
    fi
  done <"$list"
  return 0
}
remap_loc() {
  local loc=$1 file num src path
  file=${loc%:*}
  num=${loc##*:}
  if [ -f "$file.src" ]; then
    src=$(cat "$file.src")
    if [ -f "$file.map" ]; then
      path=$(sed -n "${num}p" "$file.map")
      printf '%s:%s\n' "$src" "$path"
    else
      printf '%s:%s\n' "$src" "$num"
    fi
  else
    printf '%s\n' "$loc"
  fi
}
normalize_to() {
  # 反斜杠串 + 引号收成一个引号。值内部的转义引号会变成截断点，见 REDACTION.md。
  sed -E 's/\\+"/"/g' "$1" >"$2"
}
# 下面只定义，供 redact-30 使用。
parse_quoted() {
  local s=$1 q i=1 n ch
  q=${s:0:1}
  n=${#s}
  KV_VAL=
  while [ "$i" -lt "$n" ]; do
    ch=${s:$i:1}
    if [ "$ch" = '\' ] && [ $((i + 1)) -lt "$n" ]; then
      KV_VAL=${KV_VAL}${s:$((i + 1)):1}; i=$((i + 2))
    elif [ "$ch" = '\' ]; then
      KV_VAL=${KV_VAL}'\'; i=$((i + 1))
    elif [ "$ch" = "$q" ]; then
      KV_CONSUMED=${s:0:$((i + 1))}; PARSE_CLOSED=1; return 0
    else
      KV_VAL=${KV_VAL}$ch; i=$((i + 1))
    fi
  done
  KV_VAL=${s:1}; KV_CONSUMED=$s; PARSE_CLOSED=0
}
truncate_at_quote() {
  local s=$1 i=0 n ch bs=0
  n=${#s}
  KV_VAL=
  while [ "$i" -lt "$n" ]; do
    ch=${s:$i:1}
    if [ "$ch" = '\' ]; then
      bs=$((bs + 1)); KV_VAL=${KV_VAL}$ch; i=$((i + 1)); continue
    fi
    if [ "$ch" = '"' ] && [ $((bs % 2)) -eq 0 ]; then
      KV_CONSUMED=${s:0:$((i + 1))}; return 0
    fi
    bs=0; KV_VAL=${KV_VAL}$ch; i=$((i + 1))
  done
  KV_VAL=$s; KV_CONSUMED=$s
}
label_in_actor() {
  local line=$1 off=$2 frag=$3 rest whole inner before prefix start end mend base skip
  rest=$line
  mend=$((off + ${#frag}))
  while [ -n "$rest" ]; do
    [[ $rest =~ \"actor\"[[:space:]]*:[[:space:]]*\"([^\"]*)\" ]] || return 1
    whole=${BASH_REMATCH[0]}; inner=${BASH_REMATCH[1]}
    before=${rest%%"$whole"*}; prefix=${whole%%"$inner"*}
    base=$((${#line} - ${#rest}))
    start=$((base + ${#before} + ${#prefix})); end=$((start + ${#inner}))
    [ "$off" -ge "$start" ] && [ "$mend" -le "$end" ] && return 0
    skip=$((${#before} + ${#whole}))
    [ "$skip" -gt 0 ] || return 1
    rest=${rest:$skip}
  done
  return 1
}
MOD_RULES_00=0; MOD_DONE_00=1
