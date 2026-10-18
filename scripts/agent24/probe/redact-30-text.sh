#!/usr/bin/env bash
# 对 TEXT_TARGETS 跑字段、数组、KV、query、凭据头。原文一遍，规范化副本再一遍，位置去重。
# 没有 redact-20 时 TEXT_TARGETS 仍是目录下全部文件，非 JSON 夹具照样能跑。
# path 模式只在末对象键是 actor，或行内 label_in_actor 时豁免主体标签。
# 比较区分大小写，不受 nocasematch 影响。KV 到 50 处还没判完则整行算命中。
label_exempt() {
  local line=$1 off=$2 val=$3 mode=$4 path=$5 mapf=$6
  local was=0 result=1 frag p seg
  shopt -q nocasematch && was=1; shopt -u nocasematch
  frag="key:${val}"
  case $val in
    empty|kb1|noretr|chat|full|mvs|rev) ;;
    *) val="" ;;
  esac
  if [ -n "$val" ] && [ "${line:$off:${#frag}}" = "$frag" ]; then
    if [ "$mode" = path ]; then
      p=$path
      while [ -n "$p" ]; do
        seg=${p##*.}
        case $p in *.*) p=${p%.*} ;; *) p= ;; esac
        case $seg in
          '(json)') continue ;;
          *[!0-9]*) [ "$seg" = actor ] && result=0; break ;;
        esac
      done
      [ "$result" -ne 0 ] && label_in_actor "$line" "$off" "$frag" && result=0
    elif label_in_actor "$line" "$off" "$frag"; then
      result=0
    elif printf '%s\n' "$line" | grep -qxE "[[:space:]]*\"${frag}\"[[:space:]]*"; then
      result=0
    fi
  fi
  [ "$was" -eq 1 ] && shopt -s nocasematch
  return "$result"
}
kv_line_bad() {
  local orig=$1 mode=$2 path=$3 mapf=$4
  local cursor=$1 guard=0 full key pre sep rest trimmed lead val consumed step skip auth_mode
  local base=0 prefix offset key_off rc
  shopt -s nocasematch
  while [ "$guard" -lt 50 ]; do
    guard=$((guard + 1))
    [[ $cursor =~ $KV_LINE ]] || break
    full=${BASH_REMATCH[0]}
    pre=${BASH_REMATCH[1]}
    key=${BASH_REMATCH[2]}
    sep=${full#"${full%?}"}
    prefix=${cursor%%"$full"*}
    offset=$((${#prefix} + ${#full}))
    key_off=$((base + ${#prefix} + ${#pre}))
    rest=${cursor:$offset}
    trimmed=$(trim_lead "$rest")
    lead=$((${#rest} - ${#trimmed}))
    val=
    consumed=
    if [ "$pre" = '"' ] && [ "$sep" = ":" ] \
      && [ "${trimmed:0:1}" != '"' ] && [ "${trimmed:0:1}" != "'" ]; then
      truncate_at_quote "$trimmed"
      val=$KV_VAL
      consumed=$KV_CONSUMED
    else
      case $trimmed in
        \"*|\'*)
          parse_quoted "$trimmed"
          val=$KV_VAL
          consumed=$KV_CONSUMED
          ;;
        *)
          if [ "$sep" = ":" ]; then
            val=${trimmed%"${trimmed##*[![:space:]]}"}
            consumed=$trimmed
          else
            val=$trimmed
            val=${val%%[[:space:]]*}
            val=${val%%;*}
            val=${val%%&*}
            val=${val%%,*}
            val=${val%%\"*}
            consumed=$val
          fi
          ;;
      esac
    fi
    skip=
    auth_mode=0
    case $key in
      x-api-key|x-external-user-token|x-auth-token) skip=1; consumed= ;;
      auth|authorization) auth_mode=1 ;;
    esac
    if [ "$pre" = "?" ] || [ "$pre" = "&" ]; then
      if [ "$sep" = "=" ] && [ "$full" = "${pre}${key}=" ]; then
        if printf '%s\n' "${pre}${key}=${trimmed}" | grep -qiE "^${Q_PRE}${Q_VAL}"; then
          skip=1
        fi
      fi
    fi
    if [ -z "$skip" ] && [ "$key" = "key" ] && [ "$sep" = ":" ]; then
      if label_exempt "$orig" "$key_off" "$val" "$mode" "$path" "$mapf"; then
        skip=1
      fi
    fi
    if [ -z "$skip" ]; then
      if [ "$auth_mode" -eq 1 ]; then
        if auth_value_bad "$val"; then
          shopt -u nocasematch
          return 0
        fi
      elif [ "${#val}" -ge 6 ] && ! is_placeholder "$val"; then
        shopt -u nocasematch
        return 0
      fi
    fi
    if [ -n "$consumed" ]; then
      step=$((offset + lead + ${#consumed}))
    else
      step=$((offset + 1))
    fi
    if [ "$step" -le "$offset" ]; then
      step=$((offset + 1))
    fi
    base=$((base + step))
    cursor=${cursor:$step}
  done
  [[ $cursor =~ $KV_LINE ]]; rc=$?
  [ "$rc" -eq 0 ] && KV_OVER=1
  shopt -u nocasematch
  return "$rc"
}
array_has_secret() {
  local body=$1 auth=0 got=0 q rest val
  case $(printf '%s' "$2" | tr '[:upper:]' '[:lower:]') in
    auth|authorization) auth=1 ;;
  esac
  body=$(trim_lead "$body"); body=${body%"${body##*[![:space:]]}"}
  [ -n "$body" ] || return 1
  while [ -n "$body" ]; do
    body=$(trim_lead "$body"); [ -n "$body" ] || break
    case $body in
      ,*) body=${body#,}; continue ;;
      \"*|\'*)
        q=${body:0:1}; parse_quoted "$body"; val=$KV_VAL
        if [ "${PARSE_CLOSED:-0}" != 1 ]; then return 0; fi
        rest=$(trim_lead "${body:${#KV_CONSUMED}}")
        case $rest in ""|,*) body=${rest#,} ;; *) return 0 ;; esac ;;
      *)
        val=${body%%,*}; val=${val%"${val##*[![:space:]]}"}
        body=${body#"${body%%,*}"}; body=${body#,} ;;
    esac
    got=1; [ -n "$val" ] || continue
    if [ "$auth" -eq 1 ]; then auth_value_bad "$val" && return 0
    elif [ "${#val}" -ge 6 ] && ! is_placeholder "$val"; then return 0; fi
  done
  [ "$got" -eq 0 ]
}
field_match_bad() {
  local match=$1 q name val rest lname
  q=${match:0:1}
  [ "$q" = '"' ] || [ "$q" = "'" ] || return 1
  rest=${match#?}
  name=${rest%%"${q}"*}
  rest=${rest#*"${q}"}
  rest=${rest#*:}
  rest=$(trim_lead "$rest")
  [ "${rest:0:1}" = "$q" ] || return 1
  val=${rest:1}
  lname=$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')
  case $lname in
    auth|authorization)
      auth_value_bad "$val"
      return $?
      ;;
  esac
  [ "${#val}" -ge 6 ] || return 1
  is_placeholder "$val" && return 1
  return 0
}
array_match_bad() {
  local match=$1 q name rest body
  q=${match:0:1}
  [ "$q" = '"' ] || [ "$q" = "'" ] || return 1
  rest=${match#?}
  name=${rest%%"${q}"*}
  rest=${rest#*"${q}"}
  rest=${rest#*[}
  body=${rest%]}
  array_has_secret "$body" "$name"
}
consume_matches() {
  local kind=$1 line loc match
  [ -s "$W/raw" ] || return 0
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    loc=$(printf '%s\n' "$line" | cut -d: -f1,2)
    match=$(printf '%s\n' "$line" | sed 's/^[^:]*:[0-9]*://')
    if [ "$kind" = scalar ]; then
      field_match_bad "$match" || continue
    else
      array_match_bad "$match" || continue
    fi
    remap_loc "$loc" >>"$W/field.locs"
  done <"$W/raw"
}
build_passes() {
  local f i=0 base
  mkdir -p "$W/norm"
  : >"$W/pass.a"
  : >"$W/pass.b"
  [ -s "$TEXT_TARGETS" ] || return 0
  while IFS= read -r -d '' f; do
    printf '%s\0' "$f" >>"$W/pass.a"
    base=$W/norm/$i
    normalize_to "$f" "$base"
    if [ -f "$f.src" ]; then
      cp "$f.src" "$base.src"
    else
      printf '%s\n' "$f" >"$base.src"
    fi
    [ -f "$f.map" ] && cp "$f.map" "$base.map"
    printf '%s\0' "$base" >>"$W/pass.b"
    i=$((i + 1))
  done <"$TEXT_TARGETS"
}
kv_scan_list() {
  local list=$1 f rc line num text mode mapf path
  [ -s "$list" ] || return 0
  while IFS= read -r -d '' f; do
    grep -aHaniE "$KV_LINE" "$f" >"$W/kv.lines"
    rc=$?
    if [ "$rc" -ge 2 ]; then
      bad=1
      echo "FAIL ${KV_RULE}（grep 出错 rc=${rc}）"
      continue
    fi
    mode=line
    mapf=
    path=
    if [ -f "$f.map" ]; then
      mode=path
      mapf=$f.map
    fi
    while IFS= read -r line; do
      [ -n "$line" ] || continue
      num=${line#*:}
      num=${num%%:*}
      text=${line#*:*:}
      if [ "$mode" = path ]; then
        path=$(sed -n "${num}p" "$mapf")
      fi
      if kv_line_bad "$text" "$mode" "$path" "$mapf"; then
        remap_loc "$f:$num" >>"$W/kv.locs"
      fi
    done <"$W/kv.lines"
  done <"$list"
}
append_value_hits() {
  local name=$1 pre=$2 val=$3 suf=$4 list=$5 out=$6 f rc line loc match hitpre rest
  [ -s "$list" ] || return 0
  while IFS= read -r -d '' f; do
    grep -aHanoiE "$pre$val$suf" "$f" >"$W/raw"
    rc=$?
    if [ "$rc" -ge 2 ]; then
      bad=1
      echo "FAIL ${name}（grep 出错 rc=${rc}）"
      continue
    fi
    while IFS= read -r line; do
      [ -n "$line" ] || continue
      loc=$(printf '%s\n' "$line" | cut -d: -f1,2)
      match=$(printf '%s\n' "$line" | sed 's/^[^:]*:[0-9]*://')
      hitpre=$(printf '%s\n' "$match" | grep -oiE "^${pre}")
      rest=${match#"$hitpre"}
      printf '%s\n' "$rest" | grep -qE "^${PH}${suf}\$" && continue
      remap_loc "$loc" >>"$out"
    done <"$W/raw"
  done <"$list"
}

build_passes
: >"$W/field.locs"
for _p in "$W/pass.a" "$W/pass.b"; do
  grep_list "$FIELD_RULE" "$_p" -oiE "$FIELD_RE" && consume_matches scalar
  grep_list "$FIELD_RULE" "$_p" -oiE "$ARRAY_RE" && consume_matches array
done
report "$FIELD_RULE" "$(sort -u "$W/field.locs")"

: >"$W/kv.locs"
kv_scan_list "$W/pass.a"
kv_scan_list "$W/pass.b"
report "${KV_RULE}${KV_OVER:+（超过单行检查上限）}" "$(sort -u "$W/kv.locs")"

scan_both() {
  local name=$1 pre=$2 val=$3 suf=$4 out=$5
  : >"$out"
  append_value_hits "$name" "$pre" "$val" "$suf" "$W/pass.a" "$out"
  append_value_hits "$name" "$pre" "$val" "$suf" "$W/pass.b" "$out"
  report "$name" "$(sort -u "$out")"
}
scan_both "$Q_RULE" "$Q_PRE" "$Q_VAL" "" "$W/q.locs"
scan_both "$H_RULE" "$HDR_PRE" "$HDR_VAL" "$HDR_SUF" "$W/h.locs"
MOD_RULES_30=4; MOD_DONE_30=1
