#!/usr/bin/env bash
# 与 JSON/文本解析无关的规则。解码视图在 redact-20 建好后由 basic_scan_dv 补扫，含 .env 长值。
basic_named() {
  local go=$1 sp='([[:space:]]|\\t|'"$(printf '\302\240')"'|%20|%09|\+)'
  "$go" "API key（sk- 前缀）" -E '(^|[^A-Za-z0-9-]|\\[bfntr]|\\u00[0-9A-Fa-f]{2}|%[0-9A-Fa-f]{2})sk-[A-Za-z0-9_-]{16,}'
  "$go" "API key 密文（enc:v1）" -F 'enc:v1'
  "$go" "JWT（eyJ 开头）" -E 'eyJ[A-Za-z0-9_-]{8,}'
  "$go" "Bearer 凭据（不区分大小写）" -iE "bearer${sp}+[A-Za-z0-9._~+/=-]{8,}"
  "$go" "Authorization 头（Basic / Bearer / Token / ApiKey / Digest）" \
    -iE "authorization\\\\*\"?${sp}*[:=]${sp}*\\[?${sp}*\\\\*\"?${sp}*(basic|bearer|token|api-?key|digest)${sp}+[A-Za-z0-9._~+/=-]{6,}${sp}*\\]?"
  "$go" "租户 HMAC（PROBE-HMAC- 哨兵）" -F 'PROBE-HMAC-'
  "$go" "探测口令（probe-<24 位十六进制>）" -E 'probe-[0-9a-f]{24}'
  "$go" "Cookie / Set-Cookie（含 JSON 写法）" -iE '(set-)?cookie\\*"?[[:space:]]*:'
}
basic_home_user_host() {
  local go=$1 me short esc h full names hargs
  "$go" "本机 \$HOME" -F "$HOME"
  me=${USER:-$(id -un)}
  short=false
  case $me in
    admin|root|user|users|guest|test|tester|ubuntu|debian|probe|runner|build|deploy|docker|service)
      short=true ;;
    *) [ "${#me}" -lt 5 ] && short=true || short=false ;;
  esac
  if [ "$short" = true ]; then
    esc=$(printf '%s\n' "$me" | sed 's/[][\.*^$/]/\\&/g')
    "$go" "本机用户名 \$USER(路径形态)" -E "/(home|Users)/${esc}([/\"' ]|$)"
  else
    "$go" "本机用户名 \$USER" -wF "$me"
  fi
  # 主机名合成一条规则，次数与名字个数无关。取不到 hostname 时失败，不静默少查。
  # 原文遍和解码视图遍都照样计一次；FAIL 只在原文遍打印一次。
  full=$(hostname 2>/dev/null)
  if [ -z "$full" ]; then
    rule_ran
    if [ "$go" = scan ]; then
      bad=1
      echo "FAIL 本机主机名（取不到 hostname）"
    fi
    return 0
  fi
  hargs=()
  names=
  for h in $(printf '%s\n' "$full" "${full%%.*}" "$(hostname -s 2>/dev/null)" | sort -u); do
    hargs[${#hargs[@]}]=-e
    hargs[${#hargs[@]}]=$h
    names="${names:+$names }$h"
  done
  "$go" "本机主机名 $names" -iwF "${hargs[@]}"
}
basic_email() {
  local email_name="非 @probe.invalid 的邮箱"
  if grep_ok "$email_name" grep -rnaoE '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' "$DIR"; then
    grep -v '@probe\.invalid$' "$W/raw" >"$W/kept" || true
    cut -d: -f1,2 <"$W/kept" | sort -u >"$W/locs"
    : >"$W/dv.locs"
    DV_SKIP_RE='@probe\.invalid$'
    DV_VALUE_PRE=
    DV_VALUE_SUF=
    if dv_collect "$email_name" -oE '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'; then
      report "$email_name" "$(cat "$W/locs" "$W/dv.locs" | sed '/^$/d' | sort -u)"
    else
      rule_ran
    fi
    DV_SKIP_RE=
  else
    rule_ran
  fi
}
basic_email_dv() {
  local email_name="非 @probe.invalid 的邮箱" locs
  : >"$W/dv.locs"
  DV_SKIP_RE='@probe\.invalid$'
  DV_VALUE_PRE=
  DV_VALUE_SUF=
  dv_collect "$email_name" -oE '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' || true
  DV_SKIP_RE=
  locs=$(sed '/^$/d' "$W/dv.locs" | sort -u)
  rule_ran
  [ -z "$locs" ] || report_print "$email_name" "$locs"
}
# 解码视图上再用 .env 里的原值做固定串比对。命中报 文件:JSON路径。空值、短值不在这里报。
# 列出名字的 grep：rc≥2 失败。0 条名字是否失败由 basic_env 决定。
read_env_names() {
  local rc
  grep -E '^(export[[:space:]]+)?[A-Z0-9_]*(SECRET|PASSWORD|_KEY|TOKEN|AES|SIGNING)[A-Z0-9_]*=' "$ENVF" >"$W/env.names"
  rc=$?
  if [ "$rc" -ge 2 ]; then
    bad=1
    echo "FAIL .env 密钥值比对（grep 出错 rc=${rc}）"
    return 1
  fi
  return 0
}
basic_env_dv() {
  local line v f rc hit num loc hits
  # 下面有多个提前 return（无解码视图、无 .env、无名字），所以进门就计一次。
  rule_ran
  [ -s "$W/dv.list" ] || return 0
  [ -r "$ENVF" ] || return 0
  read_env_names || return 0
  [ -s "$W/env.names" ] || return 0
  : >"$W/env.dvlocs"
  while IFS= read -r line; do
    line=${line#export }
    v=$(env_value "$line")
    [ -n "$v" ] && [ "${#v}" -ge 6 ] || continue
    while IFS= read -r -d '' f; do
      grep -aHnF -f <(printf '%s\n' "$v") "$f" >"$W/env.dv"
      rc=$?
      if [ "$rc" -ge 2 ]; then
        bad=1
        echo "FAIL .env 密钥值比对（grep 出错 rc=${rc}）"
        continue
      fi
      while IFS= read -r hit; do
        [ -n "$hit" ] || continue
        num=${hit#*:}
        num=${num%%:*}
        loc=$(remap_loc "$f:$num")
        printf '%s (%s)\n' "$loc" "${line%%=*}" >>"$W/env.dvlocs"
      done <"$W/env.dv"
    done <"$W/dv.list"
  done <"$W/env.names"
  hits=$(sort -u "$W/env.dvlocs")
  [ -n "$hits" ] && report_print ".env 密钥值比对（解码视图）" "$hits"
  return 0
}
# 由 redact-20 调用，计入 MOD_RULES_20。解码视图为空时也逐条跑（每条只记一次、不打印），
# 每次恰好 14 次：basic_named 8、URL 1、邮箱 1、$HOME 1、$USER 1、主机名 1、.env 1。
basic_scan_dv() {
  basic_named dv_rule
  dv_value_rule "URL 内嵌口令（scheme://user:pw@）" \
    '[a-z][a-z0-9+.-]*://[^/[:space:]"@:]*:' '[^/[:space:]"@]+' '@'
  basic_email_dv
  basic_home_user_host dv_rule
  basic_env_dv
}
env_value() {
  local v=${1#*=}
  case $v in
    \"*) v=${v#\"}; v=${v%%\"*} ;;
    \'*) v=${v#\'}; v=${v%%\'*} ;;
    *)
      v=${v%%[[:space:]]#*}
      v=${v%"${v##*[![:space:]]}"} ;;
  esac
  printf '%s' "$v"
}
basic_env() {
  local n empty_n short_n short_names env_hits line v rc hits
  # 每条出口都计一次规则，次数与 .env 的状态无关。
  if [ ! -r "$ENVF" ]; then
    rule_ran
    if [ "$SKIP_ENV" = true ]; then
      echo "WARN 按 --skip-env 跳过 .env 比对"
    else
      echo "FAIL 读不到 ${ENVF}（确需跳过请显式传 --skip-env）"
      bad=1
    fi
    return 0
  fi
  read_env_names || { rule_ran; return 0; }
  if [ ! -s "$W/env.names" ]; then
    rule_ran
    bad=1
    echo "FAIL .env 密钥值比对（0 个可比对的赋值）"
    return 0
  fi
  n=0
  empty_n=0
  short_n=0
  short_names=""
  env_hits=""
  while IFS= read -r line; do
    line=${line#export }
    v=$(env_value "$line")
    if [ -z "$v" ]; then
      empty_n=$((empty_n + 1))
      continue
    fi
    if [ ${#v} -lt 6 ]; then
      short_n=$((short_n + 1))
      short_names+="${line%%=*}"$'\n'
      continue
    fi
    n=$((n + 1))
    grep -rnaF -f <(printf '%s\n' "$v") "$DIR" >"$W/env.hits"
    rc=$?
    if [ "$rc" -ge 2 ]; then
      bad=1
      echo "FAIL .env 密钥值比对（grep 出错 rc=${rc}）"
      continue
    fi
    hits=$(cut -d: -f1,2 <"$W/env.hits" | sed "s/\$/ (${line%%=*})/")
    [ -n "$hits" ] && env_hits+="$hits"$'\n'
  done <"$W/env.names"
  if [ "$empty_n" -gt 0 ]; then
    echo "WARN .env 空值不比对，共 ${empty_n} 个"
  fi
  if [ "$short_n" -gt 0 ]; then
    echo "WARN .env 短值（1–5 字符）不比对，共 ${short_n} 个"
    sed 's/^/  /' <<<"${short_names%$'\n'}"
  fi
  if [ "$n" -eq 0 ] && [ "$SKIP_ENV" != true ]; then
    echo "WARN .env 没有比对任何非空值：长值 0 个，空值 ${empty_n} 个，短值 ${short_n} 个"
  fi
  report ".env 密钥值比对（${n} 个非空值）" "${env_hits%$'\n'}"
}

unreadable "$FIND_RC"
basic_named scan
scan_value "URL 内嵌口令（scheme://user:pw@）" \
  '[a-z][a-z0-9+.-]*://[^/[:space:]"@:]*:' '[^/[:space:]"@]+' '@'
basic_email
basic_home_user_host scan
basic_env
# 每次都恰好 report 15 次：不可读 1、basic_named 8、URL 1、邮箱 1、$HOME 1、$USER 1、主机名 1、.env 1。
MOD_RULES_10=15; MOD_DONE_10=1
