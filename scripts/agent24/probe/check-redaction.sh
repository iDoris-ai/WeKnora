#!/usr/bin/env bash
# 证据脱敏自检：用 grep 哨兵扫描证据目录（含二进制文件，-a），命中任何凭据或本机身份信息就失败。
# 只打印"文件:行号 规则名"，不打印命中的值。
# 用法：check-redaction.sh [--skip-env] [证据目录]（参数顺序不限；默认目录 docs/agent24/probe-evidence/4b363d15）
#   读不到 $STACK_DIR/.env 时失败；只有显式传 --skip-env 才跳过 .env 比对。
set -uo pipefail
# C locale：BSD grep 在 UTF-8 locale 下遇到二进制字节会漏匹配；规则都是 ASCII，用 C locale 扫描。
export LC_ALL=C
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
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
DIR=${DIR_ARG:-$ROOT/docs/agent24/probe-evidence/4b363d15}
ENVF=${STACK_DIR:-$HOME/weknora-probe-stack}/.env
bad=0
if [ ! -d "$DIR" ]; then
  if [ -z "$DIR_ARG" ]; then
    # 默认的正式证据目录尚不存在（例如目前只有示例项）：说清楚，不当作失败。
    echo "SKIP 默认证据目录不存在：$DIR"
    echo "     要自检，请把证据目录作为参数传入，"
    echo "     例如 run.sh 只跑部分项时打印的临时目录：check-redaction.sh /tmp/weknora-probe-partial.XXXXXX"
    echo "     本次没有检查任何文件。"
    exit 0
  fi
  echo "证据目录不存在：$DIR" >&2
  exit 2
fi

report() {   # report 规则名 命中列表（文件:行号）
  if [ -n "$2" ]; then
    bad=1
    echo "FAIL $1"
    sed 's/^/  /' <<<"$2"
  else
    echo "ok   $1"
  fi
}
scan() {   # scan 规则名 grep 参数…
  local name=$1
  shift
  report "$name" "$(grep -rna "$@" "$DIR" | cut -d: -f1,2)"
}
# scan_value 规则名 前缀正则 值正则 [后缀正则]（不区分大小写）：取出"前缀 + 值 + 后缀"，
# 只有值整体就是占位符（<redacted…> 或纯 *）时才放过——按"前缀 + 占位符 + 后缀"整段锚定比对，
# 所以 "真值=<redacted>" 这类只在结尾带占位符的值照样报出。
PH='(<redacted[^"\\<>]*>|\*+)'
scan_value() {
  local name=$1 pre=$2 val=$3 suf=${4:-}
  report "$name" "$(grep -rnaoiE "$pre$val$suf" "$DIR" | grep -viE "^[^:]*:[0-9]+:$pre$PH$suf\$" | cut -d: -f1,2)"
}

# sk- 要求左边界（行首或非字母数字、非 _ -），disk-…、task-… 这类词不算。
scan "API key（sk- 前缀）" -E '(^|[^A-Za-z0-9_-])sk-[A-Za-z0-9_-]{16,}'
scan "API key 密文（enc:v1）" -F 'enc:v1'
scan "JWT（eyJ 开头）" -E 'eyJ[A-Za-z0-9_-]{8,}'
scan "Bearer 凭据（不区分大小写）" -iE 'bearer +[A-Za-z0-9._~+/=-]{8,}'
scan "Authorization 头（Basic / Bearer / Token / ApiKey / Digest）" \
  -iE 'authorization\\*"? *[:=] *\[? *\\*"? *(basic|bearer|token|api-?key|digest) +[A-Za-z0-9._~+/=-]{6,}'
scan "租户 HMAC（PROBE-HMAC- 哨兵）" -F 'PROBE-HMAC-'
scan "探测口令（probe-<24 位十六进制>）" -E 'probe-[0-9a-f]{24}'
scan "Cookie / Set-Cookie（含 JSON 写法）" -iE '(set-)?cookie\\*"?[[:space:]]*:'
# 敏感字段带真实字符串值：字段名含 password、secret、token、credential、refresh、api key（api_key、api-key、apiKey），
# 或以 key 结尾（key、access_key、private_key、secretKey 等）；允许前后缀、驼峰、大小写不同。
# 同时匹配普通和任意层转义的引号，值可以是字符串或数组的首个字符串元素；值长度 ≥6，整体是占位符才放过。
# 布尔、数字不带引号，不会命中。
FIELD_NAME='[A-Za-z0-9_-]*(password|passwd|secret|token|credential|refresh|api[_-]?key)[A-Za-z0-9_-]*|[A-Za-z0-9_-]*key'
FIELD_PRE='\\*"('"$FIELD_NAME"')\\*"[[:space:]]*:[[:space:]]*(\[[[:space:]]*)?\\*"'
scan_value "敏感字段带值（password/secret/token/credential/refresh/api key/…key 类字段名）" "$FIELD_PRE" '[^"\\]{6,}'
scan_value "X-API-Key 头带值" 'x-api-key\\*"?[[:space:]]*[:=][[:space:]]*\\*"?' '[^"\\[:space:],}]{6,}'
# URL 里的口令（scheme://user:pw@host，用户名可以为空，如 redis://:pw@host），口令整体是占位符才放过。
# 口令按 RFC 3986 不含未编码的 /，所以遇到 / 就不算 userinfo（否则路径里带邮箱的 URL 会误报）。
scan_value "URL 内嵌口令（scheme://user:pw@）" '[a-z][a-z0-9+.-]*://[^/[:space:]"@:]*:' '[^/[:space:]"@]+' '@'
report "非 @probe.invalid 的邮箱" "$(grep -rnaoE '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' "$DIR" \
  | grep -v '@probe\.invalid$' | cut -d: -f1,2)"
scan "本机 \$HOME" -F "$HOME"
# 用户名短于 5 个字符或是常见词时，整词匹配会误报，只按路径形态 /home/<u>、/Users/<u> 匹配。
me=${USER:-$(id -un)}
case $me in
  admin | root | user | users | guest | test | tester | ubuntu | debian | probe | runner | build | deploy | docker | service)
    short=true ;;
  *) [ ${#me} -lt 5 ] && short=true || short=false ;;
esac
if [ "$short" = true ]; then
  scan "本机用户名 \$USER(路径形态)" -E "/(home|Users)/$(sed 's/[][\.*^$/]/\\&/g' <<<"$me")([/\"' ]|$)"
else
  scan "本机用户名 \$USER" -wF "$me"
fi
for h in $(printf '%s\n' "$(hostname)" "$(hostname -s)" | sort -u); do
  scan "本机主机名 $h" -iwF "$h"
done

# .env 里密钥类变量的值（只比对，不输出）。处理 export 前缀、单双引号和行尾注释。
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
if [ -r "$ENVF" ]; then
  n=0
  env_hits=""
  while IFS= read -r line; do
    line=${line#export }
    v=$(env_value "$line")
    [ ${#v} -ge 6 ] || continue
    n=$((n + 1))
    # 值经 -f 从文件描述符读入，不上命令行。
    hits=$(grep -rnaF -f <(printf '%s\n' "$v") "$DIR" | cut -d: -f1,2 | sed "s/\$/ (${line%%=*})/")
    [ -n "$hits" ] && env_hits+="$hits"$'\n'
  done < <(grep -E '^(export[[:space:]]+)?[A-Z0-9_]*(SECRET|PASSWORD|_KEY|TOKEN|AES|SIGNING)[A-Z0-9_]*=' "$ENVF")
  report ".env 密钥值比对（${n} 个非空值）" "${env_hits%$'\n'}"
elif [ "$SKIP_ENV" = true ]; then
  echo "WARN 按 --skip-env 跳过 .env 比对"
else
  echo "FAIL 读不到 ${ENVF}（确需跳过请显式传 --skip-env）"
  bad=1
fi

if [ $bad = 0 ]; then
  echo "脱敏自检通过：$DIR"
else
  echo "脱敏自检失败：$DIR" >&2
  exit 1
fi
