# shellcheck shell=bash
# KB-CT-01 探测框架：前置核对、测试数据、请求记录、证据输出、伪 Ollama、清理。由 run.sh source。
# 约定：只访问 127.0.0.1；不改 .env、不加容器、不重启；数据库只读；证据先写临时目录，自检通过后才移入 OUT。
# 凭据不经命令行参数传递（本机其他用户能看到进程参数）：请求头、请求体、HMAC 密钥都经文件或标准输入交给子进程。

# 只访问 127.0.0.1：清掉代理变量，所有 curl 另带 --noproxy '*'；-q 不读用户的 ~/.curlrc（其中可能开了 location 等）。
unset http_proxy https_proxy all_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY ALLOW_000

BASE=${BASE:-http://127.0.0.1:8080}
STACK_DIR=${STACK_DIR:-$HOME/weknora-probe-stack}
EMBED=${PROBE_EMBED:-none}
case $EMBED in
  none | fake-ollama) ;;
  *) echo "PROBE_EMBED 只接受 none 或 fake-ollama（收到 ${EMBED}）" >&2; exit 2 ;;
esac
APP_IMAGE=${PROBE_APP_IMAGE:-official}
case $APP_IMAGE in
  official | local) ;;
  *) echo "PROBE_APP_IMAGE 只接受 official 或 local（收到 ${APP_IMAGE}）" >&2; exit 2 ;;
esac
PROBE_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$PROBE_DIR/../../.." && pwd)
OUT_EXPLICIT=${OUT:+yes}   # 没有显式指定 OUT 且只跑部分项时，run.sh 会改写到临时目录
BASELINE=$ROOT/docs/agent24/UPSTREAM-BASELINE.json
# 证据身份取自登记的 release 和受测 HEAD，不写死版本。
command -v jq >/dev/null || { echo "前置核对失败：缺少 jq" >&2; exit 3; }
REL_TAG=$(jq -er '.current_baseline.release.tag' "$BASELINE" 2>/dev/null) \
  || { echo "前置核对失败：读不到 ${BASELINE} 的 .current_baseline.release.tag" >&2; exit 3; }
[[ $REL_TAG =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
  || { echo "前置核对失败：登记的 release.tag 无效（${REL_TAG}），应形如 v0.8.3" >&2; exit 3; }
REL_COMMIT=$(jq -er '.current_baseline.release.commit' "$BASELINE" 2>/dev/null) \
  || { echo "前置核对失败：读不到 ${BASELINE} 的 .current_baseline.release.commit" >&2; exit 3; }
TESTED_SHA=$(git -C "$ROOT" rev-parse --short=8 HEAD) || { echo "前置核对失败：读不到 HEAD" >&2; exit 3; }
if ! git -C "$ROOT" cat-file -e "${REL_COMMIT}^{commit}" 2>/dev/null; then
  echo "前置核对失败：本地缺 release 提交 ${REL_COMMIT}（浅克隆时先 git fetch --deepen=500）" >&2; exit 3
fi
# shellcheck source=patches.sh
. "$PROBE_DIR/patches.sh" || { echo "前置核对失败：加载 ${PROBE_DIR}/patches.sh 失败" >&2; exit 3; }
# 未登记路径两种模式都拒绝。只有已登记补丁时，官方模式拒绝（官方镜像不含 fork 补丁）；本地模式放行。
probe_namespace_diff || exit 3
if [ "$NS_HAS_DIFF" = 1 ] && [ "$UNREG_N" -eq 0 ] && [ "$APP_IMAGE" != local ]; then
  echo "前置核对失败：fork 有已登记的产品补丁（${REG_IDS}，见 docs/agent24/PATCH-LEDGER.md），官方 ${REL_TAG} 镜像不含这些补丁，不能用官方镜像出正式证据。需要本地构建镜像的探测模式（用 PROBE_APP_IMAGE=local，先跑 build-local-app.sh，见 README §11）。不要为了通过核对去改登记。" >&2
  exit 3
fi
HEAD_REV=""
if [ "$APP_IMAGE" = local ]; then
  HEAD_REV=$(git -C "$ROOT" rev-parse HEAD) || { echo "前置核对失败：读不到 HEAD" >&2; exit 3; }
  probe_require_harness_at_head || exit 3
fi
FORMAL_OUT=$ROOT/docs/agent24/probe-evidence/$TESTED_SHA
OUT=${OUT:-$FORMAL_OUT}
DEFAULT_SERVICES="app docreader frontend postgres redis"
ALLOW_HOST_OLLAMA=${PROBE_ALLOW_HOST_OLLAMA:-0}   # =1 时 none 模式允许宿主 11434 上有服务（只打 WARN）
if [ "$APP_IMAGE" = local ]; then
  if [ -z "$REG_IDS" ]; then
    _patch_ids="（无）"
  else
    _patch_ids=$REG_IDS
  fi
  TESTED_DIR_NOTE="受测目录名 ${TESTED_SHA} = 受测 HEAD ${TESTED_SHA}；app 镜像由受测 HEAD 本地构建；命名空间之外与 ${REL_TAG} 的差异全部是已登记补丁（${_patch_ids}）；其余 4 个服务用登记的官方镜像"
else
  TESTED_DIR_NOTE="受测目录名 ${TESTED_SHA} = 受测 HEAD ${TESTED_SHA}；fork 命名空间之外与 ${REL_TAG}（${REL_COMMIT:0:7}）逐字节相同，所以直接用 ${REL_TAG} 官方镜像"
fi
FAKE_MODEL=probe-fake-embed:latest
SECRETS=()

EN=""
FD=""
if [ "$EMBED" = fake-ollama ]; then
  EN="向量为伪造的固定值，只证明分块和读取路径，不代表检索质量"
  FD="（依赖伪 embedding）"
fi

fail3() { echo "前置核对失败：$*" >&2; exit 3; }
die() { echo "失败：$*" >&2; exit 1; }

W=$(mktemp -d)
EV=$W/evidence
mkdir -p "$EV"
REC=$W/rec
CHK=$W/chk
NOTE=$W/note
: >"$REC"; : >"$CHK"; : >"$NOTE"

cleanup() {
  if [ -n "${FAKE_PID:-}" ]; then
    kill "$FAKE_PID" 2>/dev/null
    sleep 1
    kill -9 "$FAKE_PID" 2>/dev/null
  fi
  # replace_out 中途被打断：删掉复制了一半的 .new.*；旧 OUT 已挪开、新的还没就位时，把 .old.* 恢复成 OUT。
  if [ -n "${REPL_NEW:-}" ] && [ -d "$REPL_NEW" ]; then rm -rf -- "$REPL_NEW"; fi
  if [ -n "${REPL_OLD:-}" ] && [ -d "$REPL_OLD" ] && [ ! -e "$OUT" ]; then mv "$REPL_OLD" "$OUT"; fi
  # 自检失败且挪不走证据时保留工作目录（0700）供排查，见 finalize。
  if [ -z "${KEEP_W:-}" ]; then rm -rf "$W"; fi
  # run.sh 为部分运行建的临时 OUT：中途失败、里面还是空的，就删掉（rmdir 只删空目录）。
  if [ -n "${TMP_OUT:-}" ]; then rmdir "$TMP_OUT" 2>/dev/null; fi
}
trap cleanup EXIT
# 中断也经 EXIT 清理；退出码按惯例：INT 130、TERM 143、HUP 129。
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

dc() { docker compose -f "$STACK_DIR/docker-compose.yml" --project-directory "$STACK_DIR" "$@"; }
cid() { dc ps -q "$1"; }
# 数据库只读：会话级 default_transaction_read_only=on，只用于读取校验。用户和库名取自 app 容器的 DB_USER、DB_NAME。
sql() {
  dc exec -T -e PGOPTIONS='-c default_transaction_read_only=on' postgres \
    psql -U "$DBU" -d "$DBN" -Atc "$1"
}
applog() { dc logs --no-color --since "$1" app 2>&1; }
uuid() { openssl rand -hex 16 | sed -E 's/(.{8})(.{4})(.{4})(.{4})(.{12})/\1-\2-\3-\4-\5/'; }
b64u() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
# 自签外部主体 JWT（HS256）：jwt_sign 密钥 载荷JSON。密钥经标准输入交给 python（第一行），不上命令行。
jwt_sign() {
  local h p s
  h=$(printf '{"alg":"HS256","typ":"JWT"}' | b64u)
  p=$(printf '%s' "$2" | b64u)
  s=$(printf '%s\n%s.%s' "$1" "$h" "$p" | python3 -I -c '
import base64, hashlib, hmac, sys
k = sys.stdin.readline().rstrip("\n").encode()
m = sys.stdin.read().encode()
print(base64.urlsafe_b64encode(hmac.new(k, m, hashlib.sha256).digest()).decode().rstrip("="), end="")') \
    || die "jwt_sign 计算签名失败"
  [ -n "$s" ] || die "jwt_sign 签名为空"
  printf '%s.%s.%s' "$h" "$p" "$s"
}
# 按固定字符串查找，查找串经 -f 从文件描述符读入、不上命令行：sgrep 串 [grep 选项…] [文件…]
sgrep() {
  local s=$1
  shift
  grep -F -f <(printf '%s\n' "$s") "$@"
}

# ---- 请求与记录 ----
# call 主体 方法 路径 [curl 参数…]。主体：none | key:名（K_名）| jwt:名（J_名，TN_名 非空时附 X-Tenant-ID）。
# 凭据头写进 $W/auth（$W 是 0700 的 mktemp 目录），用 -H @文件 交给 curl。
call() {
  local a=$1 m=$2 p=$3 v tv
  shift 3
  : >"$W/body"
  : >"$W/hdr"
  : >"$W/auth"
  case $a in
    key:*) v="K_${a#key:}"; printf 'X-API-Key: %s\n' "${!v}" >"$W/auth" ;;
    jwt:*)
      v="J_${a#jwt:}"; tv="TN_${a#jwt:}"
      printf 'Authorization: Bearer %s\n' "${!v}" >"$W/auth"
      if [ -n "${!tv:-}" ]; then printf 'X-Tenant-ID: %s\n' "${!tv}" >>"$W/auth"; fi ;;
  esac
  if [ -s "$W/xauth" ]; then cat "$W/xauth" >>"$W/auth"; fi
  : >"$W/xauth"
  ST=$(curl -q --noproxy '*' -s -m "${TMO:-30}" -o "$W/body" -D "$W/hdr" -w '%{http_code}' -X "$m" \
    -H @"$W/auth" "$@" "$BASE$p")
  # 传输失败默认中止。bash 3.2 非 posix 下，ALLOW_000=1 call/J 只对这一次调用（含 J 再调的 call）可见，返回后恢复。
  if [ "$ST" = "000" ] && [ "${ALLOW_000:-}" != 1 ]; then
    die "传输失败，HTTP 000：$m $p"
  fi
  # 记录里的 file_path 只留协议头（与 obs 一致），不落具体资源 ID。
  p=$(sed -E 's#file_path=([A-Za-z]+)(%3A%2F%2F|://)[^&]*#file_path=\1://…#' <<<"$p")
  jq -nc --arg a "$a" --arg m "$m" --arg p "$p" --arg s "$ST" \
    '{actor:$a,method:$m,path:$p,status:($s|tonumber)}' >>"$REC"
}
# 额外的凭据头（如 X-External-User-Token）：xh 头名 值。只对紧接着的下一次 call 生效，同样经 $W/auth 交给 curl，
# 不要写成 call 的 -H 参数（那样会出现在 curl 的进程参数里）。值同时记入 SECRETS，收尾时替换。
xh() {
  printf '%s: %s\n' "$1" "$2" >>"$W/xauth"
  SECRETS+=("$2")
}
# J 主体 方法 路径 JSON [curl 参数…]：请求体（可能含口令）写进 $W/req，经 --data-binary @文件 发送。
J() {
  local a=$1 m=$2 p=$3
  printf '%s' "$4" >"$W/req"
  shift 4
  call "$a" "$m" "$p" -H 'Content-Type: application/json' --data-binary @"$W/req" "$@"
}
bj() { jq -r "$1" "$W/body" 2>/dev/null; }
hd() { grep -i "^$1:" "$W/hdr" | head -1 | cut -d' ' -f2- | tr -d '\r'; }
has() { if sgrep "$1" -q "$W/body"; then echo true; else echo false; fi; }
# 把一个字段附到上一条请求记录：f 名 jq表达式（取自响应）；fv 名 字符串。只取指定字段，不落凭据。
# 值经文件交给 jq（不上命令行）；新记录先算好、校验通过才替换，任何一步失败都直接退出，不会丢掉已有记录。
_amend() {
  local last new
  [ -s "$REC" ] || die "_amend $1：还没有请求记录"
  last=$(tail -n1 "$REC")
  printf '%s' "$2" >"$W/amend"
  new=$(jq -c --arg k "$1" --slurpfile v "$W/amend" \
    'if ($v | length) == 1 then .fields[$k] = $v[0] else error("值不是单个 JSON") end' <<<"$last") \
    && [ -n "$new" ] || die "_amend $1：值不是单个 JSON，没有改动请求记录"
  { sed '$d' "$REC"; printf '%s\n' "$new"; } >"$REC.t" || die "_amend $1：写不了 $REC.t"
  mv "$REC.t" "$REC" || die "_amend $1：替换 $REC 失败"
}
# jq 表达式输出多个值时合成数组，输出单个值时原样，没有输出或出错时记 null。
f() {
  local v
  v=$(jq -c "[$2] | if length == 1 then .[0] elif length == 0 then null else . end" "$W/body" 2>/dev/null) \
    || v=null
  _amend "$1" "${v:-null}"
}
fv() {   # true/false 写成 JSON 布尔，其余写成字符串
  case $2 in
    true | false) _amend "$1" "$2" ;;
    *) _amend "$1" "$(jq -n --arg v "$2" '$v')" ;;
  esac
}
# ck 描述 期望(正则) 实际：期望取自矩阵结论。obs 描述 值：只记事实，不参与判定。
ck() {
  local ok=false
  [[ $3 =~ ^($2)$ ]] && ok=true
  jq -nc --arg d "$1" --arg e "$2" --arg a "$3" --argjson ok $ok \
    '{check:$d,expect:$e,actual:$a,ok:$ok}' >>"$CHK"
}
# ck_absent 描述 输入 实际值 期望：反向检查（“不出现”）在输入为空时无法判定。
ck_absent() {
  if [ -z "$2" ]; then
    jq -nc --arg d "$1" --arg e "$4" --arg a "输入为空，无法判定" \
      '{check:$d,expect:$e,actual:$a,ok:false}' >>"$CHK"
  else
    ck "$1" "$4" "$3"
  fi
}
obs() { jq -nc --arg d "$1" --arg a "$2" '{observation:$d,value:$a}' >>"$NOTE"; }
reset_rec() { : >"$REC"; : >"$CHK"; : >"$NOTE"; }

# emit 项号 文件名 标题 未测说明 [伪 embedding 说明] [待回写矩阵的差异，多条用"；"分隔]
emit() {
  jq -n --argjson env "$ENVJ" --arg i "$1" --arg t "$3" --arg u "$4" --arg en "${5:-}" --arg wb "${6:-}" \
    --slurpfile r "$REC" --slurpfile c "$CHK" --slurpfile n "$NOTE" '
    ([$c[] | select(.ok | not) | .check]) as $bad
    | {item: ($i | tonumber), title: $t, environment: $env,
       requests: $r, checks: $c, observations: $n,
       conclusion: ({
         verdict: (if ($c | length) == 0 then "未测"
                   elif ($bad | length) > 0 then "与矩阵不一致（见 mismatches）"
                   elif $u != "" then "已测部分与矩阵一致；其余未测"
                   else "与矩阵一致" end),
         mismatches: $bad, untested: $u}
         + (if $en != "" then {embedding_note: $en} else {} end)
         + (if $wb != "" then {pending_writeback: ($wb | split("；"))} else {} end))}' >"$EV/$2"
  jq -e . "$EV/$2" >/dev/null || die "$2 不是合法 JSON"
  reset_rec
  echo "  -> $2"
}

# setup 里的取值：take 变量名 jq表达式 说明。HTTP 非 2xx、值为空或 null 都失败退出。
take() {
  local v
  [[ $ST =~ ^2 ]] || die "$3（HTTP ${ST}）"
  v=$(jq -r "($2) // empty" "$W/body" 2>/dev/null)
  [ -n "$v" ] || die "$3：响应里没有 $2"
  printf -v "$1" '%s' "$v"
}
want() { [[ $ST =~ ^($1)$ ]] || die "$2（HTTP ${ST}）"; }

# probe_verify_local_app JSON 镜像ID 容器ConfigImage [读label命令]
# 不读调用方的 img。核对结果通过全局变量 LOCAL_BUILD_JSON、LOCAL_APP_IMAGE_ID 交回：
# LOCAL_BUILD_JSON 是 json 里的本地构建字段，LOCAL_APP_IMAGE_ID 是其中的 image_id。
# 第四个参数是读 label 的命令，调用形式与 docker image inspect 相同，缺省 docker。
# 任一不符以 3 退出。
probe_verify_local_app() {
  local lj=$1 id=$2 cfg_image=$3 reader=${4:-docker}
  local json_rev json_version json_base json_patch_ids app_tag
  local ba_any ba_ver ba_commit ba_time built_at want_version keys
  local want_patch got_patch same_set
  local rev_label rel_label patch_label any_label ver_label
  LOCAL_BUILD_JSON=$(jq -ce '{revision, image_id, tag, version, base_release, patch_ids, built_at_utc, build_args}' "$lj") \
    || fail3 "读不到 $lj 的本地构建字段"
  json_rev=$(jq -er '.revision' <<<"$LOCAL_BUILD_JSON") || fail3 "读不到 $lj 的 revision"
  LOCAL_APP_IMAGE_ID=$(jq -er '.image_id' <<<"$LOCAL_BUILD_JSON") || fail3 "读不到 $lj 的 image_id"
  app_tag=$(jq -er '.tag' <<<"$LOCAL_BUILD_JSON") || fail3 "读不到 $lj 的 tag"
  [ "$json_rev" = "$HEAD_REV" ] || fail3 "agent24-local-app.json 的 revision=${json_rev} 与当前 HEAD ${HEAD_REV} 不符"
  [ "$id" = "$LOCAL_APP_IMAGE_ID" ] || fail3 "app 容器镜像 ID 与 agent24-local-app.json 的 image_id 不符"
  rev_label=$("$reader" image inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$id") \
    || fail3 "读不到 app 镜像 label revision"
  rel_label=$("$reader" image inspect -f '{{index .Config.Labels "io.idoris.agent24.base-release"}}' "$id") \
    || fail3 "读不到 app 镜像 label base-release"
  patch_label=$("$reader" image inspect -f '{{index .Config.Labels "io.idoris.agent24.patch-ids"}}' "$id") \
    || fail3 "读不到 app 镜像 label patch-ids"
  any_label=$("$reader" image inspect -f '{{index .Config.Labels "io.idoris.agent24.with-anydoc"}}' "$id") \
    || fail3 "读不到 app 镜像 label with-anydoc"
  ver_label=$("$reader" image inspect -f '{{index .Config.Labels "io.idoris.agent24.version"}}' "$id") \
    || fail3 "读不到 app 镜像 label version"
  [ "$rev_label" = "$HEAD_REV" ] || fail3 "app 镜像 label revision=${rev_label:-空} 与当前 HEAD ${HEAD_REV} 不符"
  [ "$rel_label" = "$REL_TAG" ] || fail3 "app 镜像 label base-release=${rel_label:-空} 与登记 ${REL_TAG} 不符"
  [ "$patch_label" = "$REG_IDS_CSV" ] || fail3 "app 镜像 label patch-ids=${patch_label:-空} 与当前登记 ${REG_IDS_CSV:-空} 不符"
  [ "$any_label" = 1 ] || fail3 "app 镜像 label with-anydoc=${any_label:-空}，应为 1"
  want_version="${REL_TAG#v}+agent24.${TESTED_SHA}"
  [ "$ver_label" = "$want_version" ] || fail3 "app 镜像 label version=${ver_label:-空} 与 ${want_version} 不符"
  [ "$cfg_image" = "$app_tag" ] || fail3 "容器 Config.Image=${cfg_image:-空} 与 agent24-local-app.json 的 tag=${app_tag} 不符"
  # json 的字段各自对计算值（HEAD、登记 tag、版本串）核对一次；label 已对同一组值核对过，不再两两互比。
  json_version=$(jq -er '.version' <<<"$LOCAL_BUILD_JSON") || fail3 "读不到 $lj 的 version"
  json_base=$(jq -er '.base_release' <<<"$LOCAL_BUILD_JSON") || fail3 "读不到 $lj 的 base_release"
  [ "$json_version" = "$want_version" ] || fail3 "agent24-local-app.json 的 version=${json_version} 与计算值 ${want_version} 不符"
  [ "$json_base" = "$REL_TAG" ] || fail3 "agent24-local-app.json 的 base_release=${json_base} 与登记 ${REL_TAG} 不符"
  if ! jq -e '(.patch_ids | type) == "array" and all(.patch_ids[]; type == "string")' <<<"$LOCAL_BUILD_JSON" >/dev/null; then
    fail3 "agent24-local-app.json 的 patch_ids 不是字符串数组"
  fi
  if jq -e 'any(.patch_ids[]; contains(","))' <<<"$LOCAL_BUILD_JSON" >/dev/null; then
    fail3 "agent24-local-app.json 的 patch_ids 有元素含有逗号"
  fi
  # 空串对应 []。顺序不同单独报这一句；内容不同仍交给下面和 label 的核对。
  if [ -z "$REG_IDS_CSV" ]; then
    want_patch='[]'
  else
    want_patch=$(jq -cn --arg csv "$REG_IDS_CSV" '$csv | split(",")') || fail3 "拆不开当前登记差异中的补丁 id"
  fi
  got_patch=$(jq -c '.patch_ids' <<<"$LOCAL_BUILD_JSON") || fail3 "读不到 $lj 的 patch_ids"
  if [ "$got_patch" != "$want_patch" ]; then
    same_set=$(jq -n --argjson g "$got_patch" --argjson w "$want_patch" '($g|type)=="array" and ($w|type)=="array" and (($g|sort)==($w|sort))') || same_set=false
    if [ "$same_set" = "true" ]; then
      fail3 "agent24-local-app.json 的 patch_ids 顺序与 REG_IDS_CSV 不同"
    fi
  fi
  json_patch_ids=$(jq -er '.patch_ids | if type == "array" then join(",") else error("patch_ids") end' <<<"$LOCAL_BUILD_JSON") \
    || fail3 "读不到 $lj 的 patch_ids"
  [ "$json_patch_ids" = "$patch_label" ] || fail3 "agent24-local-app.json 的 patch_ids=${json_patch_ids} 与 label patch-ids 不符"
  if [ "$got_patch" != "$want_patch" ]; then
    fail3 "agent24-local-app.json 的 patch_ids 与 REG_IDS_CSV 逐项不一致"
  fi
  keys=$(jq -r '.build_args | if type == "object" then (keys | join(",")) else "__not_object__" end' <<<"$LOCAL_BUILD_JSON") \
    || fail3 "读不到 $lj 的 build_args"
  [ "$keys" = "BUILD_TIME_ARG,COMMIT_ID_ARG,VERSION_ARG,WITH_ANYDOC" ] \
    || fail3 "agent24-local-app.json 的 build_args 的键必须正好是 WITH_ANYDOC、VERSION_ARG、COMMIT_ID_ARG、BUILD_TIME_ARG"
  built_at=$(jq -er '.built_at_utc' <<<"$LOCAL_BUILD_JSON") || fail3 "读不到 $lj 的 built_at_utc"
  ba_time=$(jq -er '.build_args.BUILD_TIME_ARG' <<<"$LOCAL_BUILD_JSON") || fail3 "读不到 $lj 的 build_args.BUILD_TIME_ARG"
  [ "$built_at" = "$ba_time" ] || fail3 "agent24-local-app.json 的 built_at_utc=${built_at} 与 build_args.BUILD_TIME_ARG=${ba_time} 不符"
  ba_any=$(jq -er '.build_args.WITH_ANYDOC' <<<"$LOCAL_BUILD_JSON") || fail3 "读不到 $lj 的 build_args.WITH_ANYDOC"
  ba_ver=$(jq -er '.build_args.VERSION_ARG' <<<"$LOCAL_BUILD_JSON") || fail3 "读不到 $lj 的 build_args.VERSION_ARG"
  ba_commit=$(jq -er '.build_args.COMMIT_ID_ARG' <<<"$LOCAL_BUILD_JSON") || fail3 "读不到 $lj 的 build_args.COMMIT_ID_ARG"
  [ "$ba_any" = 1 ] || fail3 "agent24-local-app.json 的 build_args.WITH_ANYDOC=${ba_any}，应为 1"
  [ "$ba_ver" = "$want_version" ] || fail3 "agent24-local-app.json 的 build_args.VERSION_ARG=${ba_ver} 与计算值 ${want_version} 不符"
  [ "$ba_commit" = "$HEAD_REV" ] || fail3 "agent24-local-app.json 的 build_args.COMMIT_ID_ARG=${ba_commit} 与当前 HEAD ${HEAD_REV} 不符"
}

# ---- 前置核对：任一不符以退出码 3 停下。除 /system/info 外都在写入任何数据之前完成。 ----
precheck() {
  local t s line pair img id exp dg port_lines="" digests="" warns="" base_port app_port st running schema
  local digest_note="RepoDigests 与登记值一致" lj=""
  LOCAL_BUILD_JSON=null
  LOCAL_APP_IMAGE_ID=""
  for t in curl jq docker openssl python3; do
    command -v "$t" >/dev/null || fail3 "缺少 $t"
  done
  [[ $BASE =~ ^http://127\.0\.0\.1:([0-9]+)$ ]] || fail3 "BASE 只能是 http://127.0.0.1:端口"
  base_port=${BASH_REMATCH[1]}
  [ -r "$BASELINE" ] || fail3 "读不到 $BASELINE"
  [ -r "$STACK_DIR/.env" ] || fail3 "读不到 $STACK_DIR/.env（脱敏自检要用它比对密钥值）"

  running=$(dc ps --services --status running | sort | tr '\n' ' ' | sed 's/ $//')
  [ "$running" = "$DEFAULT_SERVICES" ] || fail3 "运行中的服务是 [$running]，应为 [$DEFAULT_SERVICES]"
  for s in $DEFAULT_SERVICES; do
    while IFS= read -r line; do
      [ -n "$line" ] || continue
      port_lines+="$s: $line;"
      [[ $line =~ \-\>\ 127\.0\.0\.1: ]] || fail3 "$s 的发布端口没有只绑 127.0.0.1：$line"
    done < <(docker port "$(cid "$s")")
  done
  app_port=$(docker port "$(cid app)" 8080/tcp | sed -n 's/^127\.0\.0\.1://p' | head -1)
  [ "$app_port" = "$base_port" ] || fail3 "BASE 端口 $base_port 不是 app 发布的宿主端口（${app_port:-无}）"
  DBU=$(docker exec "$(cid app)" printenv DB_USER)
  DBN=$(docker exec "$(cid app)" printenv DB_NAME)
  [ -n "$DBU" ] && [ -n "$DBN" ] || fail3 "app 容器里读不到 DB_USER 或 DB_NAME"
  st=$(docker exec "$(cid app)" printenv STORAGE_TYPE)
  [ "$st" = local ] || fail3 "app 的 STORAGE_TYPE=${st}，应为 local"

  # 5 个默认服务都按登记的 digest 比对（登记值是 {digest, as_of} 对象；兼容旧的纯字符串写法）。
  # 本地构建模式：app 不比 RepoDigests，改为核对 agent24-local-app.json 与镜像 label。
  if [ "$APP_IMAGE" = local ]; then
    lj="$STACK_DIR/agent24-local-app.json"
    [ -f "$lj" ] && [ -r "$lj" ] || fail3 "读不到 $lj"
    digest_note="app 为本地构建：镜像 ID 与 agent24-local-app.json 一致；label 的 revision、base-release、patch-ids、with-anydoc、version 与当前 HEAD、登记一致；json 各字段与同一组值一致；Config.Image 等于 json 的 tag。其余服务 RepoDigests 与登记值一致"
  fi
  for pair in app:app frontend:ui docreader:docreader postgres:postgres redis:redis; do
    s=${pair%%:*}
    img=$(docker inspect -f '{{.Config.Image}}' "$(cid "$s")")
    id=$(docker inspect -f '{{.Image}}' "$(cid "$s")")   # 按容器实际使用的镜像 ID 查，避免标签已被改指
    if [ "$APP_IMAGE" = local ] && [ "$s" = app ]; then
      probe_verify_local_app "$lj" "$id" "$img" docker
      digests+="app=${img}@${LOCAL_APP_IMAGE_ID};"
      continue
    fi
    exp=$(jq -r --arg k "${pair#*:}" '.current_baseline.image_digests[$k]
      | if type == "object" then .digest else . end // empty' "$BASELINE")
    [[ $exp == sha256:* ]] || fail3 "UPSTREAM-BASELINE.json 的 current_baseline 里没有 ${pair#*:} 的 digest"
    dg=$(docker image inspect -f '{{json .RepoDigests}}' "$id")
    jq -e --arg e "@$exp" 'map(endswith($e)) | any' <<<"$dg" >/dev/null || fail3 "$s 镜像 RepoDigests 与登记值不符"
    digests+="$s=$img@$exp;"
  done

  # 只认"<当前登记 tag> 为 N"，旧版本残留的句子不会被读出。tag 里的点号换成 [.]：替换串里的反斜杠
  # 不一定留得住（随 jq 版本），留不住时点号仍是通配，v0x8x3 也会被认成 v0.8.3。
  WANT_DB=$(jq -r --arg t "$REL_TAG" '.current_baseline.test_stack_constraints[]
    | capture(($t | gsub("\\."; "[.]")) + " 为 (?<n>[0-9]+)").n' "$BASELINE" | head -1)
  WANT_COMMIT=$(jq -r '.current_baseline.release.commit // empty' "$BASELINE")
  [ -n "$WANT_DB" ] && [ -n "$WANT_COMMIT" ] || fail3 "登记里缺 schema_migrations 版本或 release.commit"
  schema=$(sql "select version||' dirty='||dirty from schema_migrations") || fail3 "无法连接数据库（psql 失败）"
  [ "$schema" = "$WANT_DB dirty=false" ] || fail3 "schema_migrations=${schema}，应为 $WANT_DB"
  if curl -q --noproxy '*' -s -m 2 http://127.0.0.1:11434/ >/dev/null; then
    [ "$EMBED" = fake-ollama ] && fail3 "127.0.0.1:11434 已被占用，不能起伪 Ollama"
    [ "$ALLOW_HOST_OLLAMA" = 1 ] || fail3 "none 模式下宿主 127.0.0.1:11434 有服务；确认无妨请设 PROBE_ALLOW_HOST_OLLAMA=1"
    # none 模式：.env 的 OLLAMA_BASE_URL 指向宿主 11434，产品可能去那里拉取不存在的模型（KBV 的 probe-absent-embed）。
    local w11434="none 模式下宿主 127.0.0.1:11434 有应答：产品可能调用宿主的 Ollama"
    w11434="${w11434} 去拉取不存在的模型，第 3 项的 500 原文可能不同"
    warns+="$w11434;"
    echo "WARN $w11434" >&2
  fi

  ENVJ=$(jq -nc --arg d "$digests" --arg m "$schema" --arg t "$(date -u +%FT%TZ)" --arg e "$EMBED" \
    --arg en "$EN" --arg p "$port_lines" --arg b "$BASE" --arg r "$running" --arg st "$st" --arg w "$warns" \
    --arg tn "$TESTED_DIR_NOTE" --arg src "$APP_IMAGE" --arg dc "$digest_note" --argjson lb "$LOCAL_BUILD_JSON" '
    {tested_dir_note: $tn, images: ($d | split(";") | map(select(. != ""))), digest_check: $dc,
     schema_migrations: $m, run_at_utc: $t, base: $b, running_services: $r, storage_type: $st,
     docker_port: ($p | split(";") | map(select(. != ""))), embed_mode: $e}
    + {app_source: $src}
    + (if $src == "local" then {local_build: $lb} else {} end)
    + (if $en != "" then {embedding_note: $en} else {} end)
    + (if $w != "" then {warnings: ($w | split(";") | map(select(. != "")))} else {} end)')
}

# probe_check_system_info_local COMMIT VERSION BUILD_TIME
# 本地构建的 /system/info：commit 全长等于 HEAD_REV；version 等于去掉 v 的登记 tag
# 再接 +agent24.<TESTED_SHA>；build_time 等于 LOCAL_BUILD_JSON 里 build_args.BUILD_TIME_ARG。
probe_check_system_info_local() {
  local commit=$1 version=$2 build_time=$3 want_version want_bt
  [ "$commit" = "$HEAD_REV" ] || fail3 "/system/info commit_id=$commit 与当前 HEAD ${HEAD_REV} 不符"
  want_version="${REL_TAG#v}+agent24.${TESTED_SHA}"
  [ "$version" = "$want_version" ] || fail3 "/system/info version=$version 与本地构建版本 ${want_version} 不符"
  want_bt=$(jq -er '.build_args.BUILD_TIME_ARG' <<<"$LOCAL_BUILD_JSON") || fail3 "读不到本地构建的 build_args.BUILD_TIME_ARG"
  [ "$build_time" = "$want_bt" ] || fail3 "/system/info build_time=$build_time 与 build_args.BUILD_TIME_ARG=${want_bt} 不符"
}

# /system/info 要鉴权，只能在注册并登录第一个测试用户之后核对；不符同样以 3 停下，不再写其他数据。
check_system_info() {
  call jwt:A GET /api/v1/system/info
  [ "$ST" = 200 ] || fail3 "/system/info 返回 $ST"
  local c d v
  c=$(bj .data.commit_id)
  d=$(bj .data.db_version)
  v=$(bj .data.version)
  if [ "$APP_IMAGE" = local ]; then
    probe_check_system_info_local "$c" "$v" "$(bj .data.build_time)"
  else
    [ ${#c} -ge 7 ] && [[ $WANT_COMMIT == "$c"* ]] || fail3 "/system/info commit_id=$c 与登记 $WANT_COMMIT 不符"
    # /system/info 的 version 不带 v 前缀（如 0.8.3），登记的 tag 带（如 v0.8.3）。
    [ "v$v" = "$REL_TAG" ] || fail3 "/system/info version=$v 与登记 $REL_TAG 不符"
  fi
  [ "$d" = "$WANT_DB" ] || fail3 "/system/info db_version=${d}，应为 $WANT_DB"
  ENVJ=$(jq -c --argjson s "$(jq -c '.data | {version, edition, commit_id, db_version, build_time}' "$W/body")" \
    '.system_info = $s' <<<"$ENVJ")
}

# ---- 伪 Ollama（PROBE_EMBED=fake-ollama）：宿主 127.0.0.1:11434，按文本哈希给 8 维固定向量。----
# query 含 PROBE-SLOW 时延迟 8 秒，供"取消"探测使用。
fake_ollama() {
  local i
  python3 -I - 2>/dev/null <<'PY' &
import hashlib, json, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
N = "probe-fake-embed:latest"
class H(BaseHTTPRequestHandler):
    def out(self, o, c=200):
        b = json.dumps(o).encode()
        self.send_response(c)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)
    def do_HEAD(self):
        self.send_response(200)
        self.end_headers()
    def do_GET(self):
        tags = {"models": [{"name": N, "model": N, "size": 1, "digest": "0" * 64}]}
        self.out(tags if self.path.startswith("/api/tags") else {"status": "ok"})
    def do_POST(self):
        r = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"{}")
        if not self.path.startswith("/api/embed") or r.get("model") != N:
            return self.out({"error": "model not found"}, 404)
        texts = r.get("input") or ""
        texts = texts if isinstance(texts, list) else [texts]
        if any("PROBE-SLOW" in t for t in texts):
            time.sleep(8)
        vecs = [[b / 255 - 0.5 for b in hashlib.sha256(t.encode()).digest()[:8]] for t in texts]
        self.out({"embeddings": vecs})
    def log_message(self, *a):
        pass
ThreadingHTTPServer(("127.0.0.1", 11434), H).serve_forever()
PY
  FAKE_PID=$!
  disown
  for i in $(seq 1 20); do
    kill -0 "$FAKE_PID" 2>/dev/null || die "伪 Ollama 进程已退出（端口可能被占用）"
    if curl -q --noproxy '*' -s -m 1 http://127.0.0.1:11434/api/tags \
      | jq -e --arg n "$FAKE_MODEL" '.models[0].name == $n' >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  die "伪 Ollama 没有就绪"
}

# ---- 测试数据：全部经 API 建立；每一步都检查返回值。数据清单见 README「框架接口」。 ----
register_login() {
  local u=$1
  local email="probe-$1-$RID@probe.invalid"
  J none POST /api/v1/auth/register \
    "{\"username\":\"probe-$u-$RID\",\"email\":\"$email\",\"password\":\"$PW\"}"
  take "U_$u" .user.id "注册用户 $u"
  take "T_$u" .user.tenant_id "用户 $u 的个人租户"
  J none POST /api/v1/auth/login "{\"email\":\"$email\",\"password\":\"$PW\"}"
  take "J_$u" .token "登录用户 $u"
  SECRETS+=("$(bj .token)" "$(bj .refresh_token)")
}

mk_kb() {   # mk_kb 变量名 主体 请求体
  J "$2" POST /api/v1/knowledge-bases "$3"
  take "$1" .data.id "建 KB $1"
}

upload() {   # upload 变量名 主体 KB_ID 文件
  call "$2" POST "/api/v1/knowledge-bases/$3/knowledge/file" -F "file=@$W/$4"
  take "$1" .data.id "上传 $4"
}

mk_key() {   # mk_key 名 请求体 → K_名、KID_名
  J jwt:A POST "/api/v1/tenants/$TA/api-keys" "$2"
  take "K_$1" .data.token "建 API key $1"
  take "KID_$1" .data.id "建 API key $1"
  SECRETS+=("$(bj .data.token)")
}

setup() {
  local u i emb="" strat='"indexing_strategy":{"vector_enabled":false,"keyword_enabled":true}'
  RID=$(date -u +%m%d%H%M%S)$RANDOM
  PW="probe-$(openssl rand -hex 12)"
  SECRETS+=("$PW")
  echo "run id $RID"

  register_login A
  check_system_info
  for u in V C X; do register_login "$u"; done
  TA=$T_A
  TN_V=$TA
  TN_C=$TA
  J jwt:A POST "/api/v1/tenants/$TA/members" "{\"email\":\"probe-V-$RID@probe.invalid\",\"role\":\"viewer\"}"
  want '200|201' "把 V 加为 viewer"
  J jwt:A POST "/api/v1/tenants/$TA/members" "{\"email\":\"probe-C-$RID@probe.invalid\",\"role\":\"contributor\"}"
  want '200|201' "把 C 加为 contributor"

  if [ "$EMBED" = fake-ollama ]; then
    fake_ollama
    for u in A X; do
      J "jwt:$u" POST /api/v1/models \
        "{\"name\":\"$FAKE_MODEL\",\"type\":\"Embedding\",\"source\":\"local\",\"parameters\":{\"embedding_parameters\":{\"dimension\":8}}}"
      take "M_$u" .data.id "用户 $u 注册伪 embedding 模型"
    done
  fi
  [ "$EMBED" = fake-ollama ] && emb="\"embedding_model_id\":\"$M_A\","
  mk_kb KB1 jwt:A "{\"name\":\"probe-KB1-$RID\",$emb$strat}"
  mk_kb KB2 jwt:A "{\"name\":\"probe-KB2-$RID\",$emb$strat}"
  [ "$EMBED" = fake-ollama ] && emb="\"embedding_model_id\":\"$M_X\","
  mk_kb KBX jwt:X "{\"name\":\"probe-KBX-$RID\",$emb$strat}"
  # KBV：默认策略（开向量），模型指向不存在的 Ollama 模型，用来触发检索的内部错误路径。
  J jwt:A POST /api/v1/models \
    '{"name":"probe-absent-embed:latest","type":"Embedding","source":"local","parameters":{"embedding_parameters":{"dimension":8}}}'
  take M_ABSENT .data.id "注册不存在的 embedding 模型"
  mk_kb KBV jwt:A "{\"name\":\"probe-KBV-$RID\",\"embedding_model_id\":\"$M_ABSENT\"}"
  # KBF：开向量、用伪模型，供"取消"探测（伪 Ollama 对 PROBE-SLOW 延迟响应）。
  if [ "$EMBED" = fake-ollama ]; then
    mk_kb KBF jwt:A "{\"name\":\"probe-KBF-$RID\",\"embedding_model_id\":\"$M_A\"}"
  fi

  printf '测试文档 alpha 的哨兵句 PROBE-SENTINEL-BODY-ALPHA-%s。纯属合成的假内容。\n' "$RID" >"$W/alpha.txt"
  printf '测试文档 beta 的哨兵句 PROBE-SENTINEL-BODY-BETA-%s。\n' "$RID" >"$W/beta.txt"
  printf '测试文档 gamma（另一租户）PROBE-SENTINEL-BODY-GAMMA-%s。\n' "$RID" >"$W/gamma.txt"
  printf '<html><body>PROBE-SENTINEL-HTML-%s</body></html>\n' "$RID" >"$W/probe.html"
  upload D_ALPHA jwt:A "$KB1" alpha.txt
  upload D_HTML jwt:A "$KB1" probe.html
  upload D_BETA jwt:A "$KB2" beta.txt
  upload D_GAMMA jwt:X "$KBX" gamma.txt
  for D in D_ALPHA D_BETA; do
    for i in $(seq 1 24); do
      call jwt:A GET "/api/v1/knowledge/${!D}"
      PS=$(bj .data.parse_status)
      [[ $PS =~ ^(completed|failed)$ ]] && break
      sleep 5
    done
    if [ "$EMBED" = fake-ollama ] && [ "$PS" != completed ]; then die "$D 解析未完成：$PS"; fi
  done
  call jwt:A GET "/api/v1/knowledge/$D_ALPHA"
  take FP_ALPHA .data.file_path "取 alpha 的 file_path"
  PS=$(bj .data.parse_status)
  PERR=$(bj .data.error_message)

  mk_key empty '{"name":"p-empty","capabilities":["retrieve"],"knowledge_base_ids":[]}'
  mk_key kb1 "{\"name\":\"p-kb1\",\"capabilities\":[\"retrieve\"],\"knowledge_base_ids\":[\"$KB1\"]}"
  mk_key noretr '{"name":"p-noretr","capabilities":["run_evaluations"]}'
  mk_key chat '{"name":"p-chat","capabilities":["chat","retrieve"]}'
  mk_key full '{"name":"p-full","full_access":true}'
  mk_key mvs '{"name":"p-mvs","capabilities":["manage_vector_stores"]}'
  mk_key rev '{"name":"p-rev","capabilities":["retrieve"]}'
  reset_rec
  echo "setup 完成：alpha parse_status=$PS"
}

# ---- 收尾：替换本次凭据 → 脱敏自检 → 通过后才移入 OUT。中途失败不会动 OUT。 ----
# 凭据以 NUL 分隔经标准输入交给 python，按长度从长到短替换为 <redacted>（不上命令行）。
finalize() {
  local s failed
  for s in ${SECRETS[@]+"${SECRETS[@]}"}; do
    if [ ${#s} -ge 8 ]; then printf '%s\0' "$s"; fi
  done | python3 -I -c '
import os, sys
d = sys.argv[1]
secs = sorted({x for x in sys.stdin.buffer.read().split(b"\0") if x}, key=len, reverse=True)
for n in sorted(os.listdir(d)):
    p = os.path.join(d, n)
    if not os.path.isfile(p):
        continue
    with open(p, "rb") as fh:
        b = o = fh.read()
    for s in secs:
        b = b.replace(s, b"<redacted>")
    if b != o:
        with open(p, "wb") as fh:
            fh.write(b)
' "$EV" || die "替换本次凭据失败，没有改动 $OUT"
  if ! STACK_DIR=$STACK_DIR "$PROBE_DIR/check-redaction.sh" "$EV"; then
    # 留下证据供排查：挪到新的 0700 目录（mktemp -d），不删；里面可能还有凭据，看完自行删除。
    failed=${TMPDIR:-/tmp}
    failed=$(mktemp -d "${failed%/}/weknora-probe-failed.XXXXXX") || die "脱敏自检未通过，没有改动 $OUT"
    if ! mv "$EV"/* "$failed"/; then
      KEEP_W=1   # 交给 cleanup：不删工作目录，没挪走的证据还在里面
      die "脱敏自检未通过，没有改动 ${OUT}。证据没能全部挪到 ${failed}，没挪走的留在 ${EV}（工作目录 ${W} 已保留，权限 0700，可能含凭据，排查后请删除）"
    fi
    die "脱敏自检未通过，没有改动 ${OUT}。未通过的证据留在 ${failed}（权限 0700，可能含凭据，排查后请删除）"
  fi
  replace_out
}

# 原子替换 OUT：先复制到 OUT 同级的临时目录并逐个比对，再整体 rename。任何一步失败都不动原来的 OUT。
replace_out() {
  local parent base new old fn
  parent=$(dirname "$OUT")
  base=$(basename "$OUT")
  mkdir -p "$parent" || die "建不了 $parent"
  new=$(mktemp -d "$parent/.$base.new.XXXXXX") || die "在 $parent 下建不了临时目录"
  REPL_NEW=$new   # 交给 cleanup：中断时删掉
  # mktemp -d 建的是 0700；按 umask 放开，替换后 OUT 的权限与普通目录一致。
  chmod "$(printf '%o' $((0777 & ~$(umask))))" "$new" || { rm -rf -- "$new"; die "设置 $new 权限失败"; }
  cp -p "$EV"/* "$new"/ || { rm -rf -- "$new"; die "复制证据失败，没有改动 $OUT"; }
  for fn in "$EV"/*; do
    cmp -s "$fn" "$new/$(basename "$fn")" || { rm -rf -- "$new"; die "复制后校验不一致，没有改动 $OUT"; }
  done
  if [ -e "$OUT" ]; then
    old=$(mktemp -d "$parent/.$base.old.XXXXXX") && rmdir "$old" || { rm -rf -- "$new"; die "建不了备份目录名"; }
    REPL_OLD=$old   # 交给 cleanup：中断时若 OUT 不在就把它恢复回去
    mv "$OUT" "$old" || { rm -rf -- "$new"; die "无法挪开旧的 ${OUT}，没有改动它"; }
  fi
  if ! mv "$new" "$OUT"; then
    [ -n "${old:-}" ] && mv "$old" "$OUT"
    rm -rf -- "$new"
    die "rename 失败，已恢复原来的 $OUT"
  fi
  REPL_NEW=""
  REPL_OLD=""
  [ -n "${old:-}" ] && rm -rf -- "$old"
  echo "证据已整体替换到 ${OUT}（目录内只有本次运行的文件）"
}

# 上次运行被 SIGKILL 打断（trap 来不及执行）时，OUT 同级可能残留 .<名>.new.* 或 .<名>.old.*：
# .new.* 是复制了一半的新证据，删掉；.old.* 是挪开的旧 OUT——OUT 还在就删掉，OUT 不在且只有一个就恢复成 OUT，
# 不在且有多个就以 2 停下，交给人处理。recover_out 目录：run.sh 对正式证据目录和本次 OUT 各调一次。
recover_out() {
  local out=$1 parent base d olds=() news=()
  parent=$(dirname "$out")
  base=$(basename "$out")
  [ -d "$parent" ] || return 0
  for d in "$parent/.$base.new."??????; do [ -d "$d" ] && news+=("$d"); done
  for d in "$parent/.$base.old."??????; do [ -d "$d" ] && olds+=("$d"); done
  for d in ${news[@]+"${news[@]}"}; do
    echo "清理上次中断残留的 $d" >&2
    rm -rf -- "$d"
  done
  [ ${#olds[@]} -gt 0 ] || return 0
  if [ -e "$out" ]; then
    for d in "${olds[@]}"; do
      echo "清理上次中断残留的 ${d}（OUT 已就位）" >&2
      rm -rf -- "$d"
    done
  elif [ ${#olds[@]} -eq 1 ]; then
    echo "上次中断时 OUT 已挪开、新的没有就位：把 ${olds[0]} 恢复成 $out" >&2
    mv "${olds[0]}" "$out" || { echo "恢复失败：${olds[0]}" >&2; exit 2; }
  else
    echo "拒绝：$out 不存在，同级有多个上次中断残留的 .$base.old.*，请人工确认后恢复或删除" >&2
    exit 2
  fi
}

# OUT 会被整体替换，所以先确认里面没有别的东西：check_out 是否全量（yes/no）。
# 显式指定的 OUT 和默认的正式证据目录都查；run.sh 为部分运行新建的临时目录是空的，直接通过。
check_out() {
  local name
  recover_out "$OUT"
  [ -e "$OUT" ] || return 0
  [ -d "$OUT" ] || { echo "拒绝：OUT 已存在且不是目录：$OUT" >&2; exit 2; }
  [ -n "$(ls -A "$OUT")" ] || return 0
  if [ -n "$OUT_EXPLICIT" ] && [ "$1" != yes ]; then
    echo "拒绝：只跑部分项，而 OUT（${OUT}）非空；OUT 会被整体替换，其余项的证据会丢。换一个空目录或不指定 OUT。" >&2
    exit 2
  fi
  while IFS= read -r name; do
    if [ -d "$OUT/$name" ] || ! [[ $name =~ ^([0-9]{2}-[A-Za-z0-9-]+\.json|13-log-body\.txt)$ ]]; then
      echo "拒绝：OUT（${OUT}）里有不是证据文件的 ${name}；OUT 会被整体替换，为免误删请换目录。" >&2
      exit 2
    fi
  done < <(ls -A "$OUT")
}

# 各项结论汇总（含待回写矩阵的差异）；有"与矩阵不一致"时返回 4（证据照写）。
summarize() {
  local fn bad=0 line
  echo "各项结论："
  for fn in "$OUT"/*.json; do
    [ -f "$fn" ] || continue
    line=$(jq -r '"  第 \(.item) 项：\(.conclusion.verdict)",
      (.conclusion.pending_writeback // [] | .[] | "    待回写矩阵：\(.)")' "$fn")
    echo "$line"
    jq -e '.conclusion.mismatches | length > 0' "$fn" >/dev/null && bad=1
  done
  if [ -f "$OUT/13-log-body.txt" ]; then
    line=$(sed -n 's/^## 结论：//p' "$OUT/13-log-body.txt")
    echo "  第 13 项：$line"
    [[ $line == 与矩阵不一致* ]] && bad=1
  fi
  return $((bad * 4))
}
