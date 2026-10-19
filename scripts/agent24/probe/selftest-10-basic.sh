#!/usr/bin/env bash
# 基础规则。不依赖 jq，也不依赖字段/KV/query。
pos "$R_SK" 'sk- 前接 \n' <<'EOF'
prefix\nsk-FAKEFAKEFAKEFAKE12
EOF
pos "$R_SK" 'sk- 前接 \t' <<'EOF'
prefix\tsk-FAKEFAKEFAKEFAKE12
EOF
pos "$R_SK" 'sk- 前接引号' <<'EOF'
"sk-FAKEFAKEFAKEFAKE12
EOF
pos "$R_SK" 'sk- 前接 _' <<'EOF'
_sk-FAKEFAKEFAKEFAKE12
EOF
pos "$R_SK" 'sk- 前接 \r' <<'EOF'
prefix\rsk-FAKEFAKEFAKEFAKE12
EOF
pos "$R_SK" 'sk- 前接 \u00XX' <<'EOF'
prefix\u0020sk-FAKEFAKEFAKEFAKE12
EOF
pos "$R_SK" 'sk- 行首' <<'EOF'
sk-FAKEFAKEFAKEFAKE12
EOF
pos "$R_ENC" 'enc:v1' <<'EOF'
enc:v1:not-a-real-ciphertext
EOF
pos "$R_JWT" 'JWT eyJ' <<'EOF'
eyJFAKEJWT123456.not.real
EOF
pos "$R_BEARER" 'Bearer' <<'EOF'
Bearer fakebearer123
EOF
pos "$R_AUTH" 'Authorization Basic' <<'EOF'
Authorization: Basic ZmFrZXBhc3Mabc
EOF
pos "$R_HMAC" 'PROBE-HMAC-' <<'EOF'
PROBE-HMAC-fakesentinel0123
EOF
pos "$R_PROBE" 'probe-24hex' <<'EOF'
probe-0123456789abcdef01234567
EOF
pos "$R_COOKIE" 'Set-Cookie' <<'EOF'
Set-Cookie: session=fake
EOF
pos "$R_URL" 'URL 内嵌口令' <<'EOF'
redis://:fakepassword123@127.0.0.1/0
EOF
pos "$R_MAIL" '非 probe.invalid 邮箱' <<'EOF'
nobody@example.com
EOF
pos "$R_HOME" '本机 $HOME' <<EOF
seen at $HOME/notes
EOF
me=${USER:-$(id -un)}
case $HOME in
  /Users*) user_path="/home/${me}/probe-fixture" ;;
  *) user_path="/Users/${me}/probe-fixture" ;;
esac
pos_only "$R_USER" '本机 $USER' <<EOF
$user_path
EOF
pos "$R_HOST" '本机主机名' <<EOF
$(hostname)
$(hostname -s)
EOF

if [ "$(id -u)" -eq 0 ]; then
  n=$((n + 1))
  printf 'SKIP 不可读文件（root 下跳过）\n'
else
  n=$((n + 1))
  d=$BASE/c$n
  mkdir -p "$d"
  printf 'unread-fixture\n' >"$d/hidden.txt"
  chmod 000 "$d/hidden.txt"
  "$CHECK" --skip-env "$d" >"$d/out" 2>&1
  rc=$?
  chmod 644 "$d/hidden.txt" 2>/dev/null || true
  if [ "$rc" -ne 1 ] || ! grep '^FAIL' "$d/out" | grep -F -q '不可读文件'; then
    bad=1
    printf '不符 不可读文件：期望 rc=1 且输出含不可读文件，实际 rc=%s\n' "$rc"
    sed 's/^/  /' "$d/out"
  fi
fi

if [ "$(id -u)" -eq 0 ]; then
  n=$((n + 1))
  printf 'SKIP grep 出错（root 下跳过）\n'
else
  n=$((n + 1))
  d=$BASE/c$n
  mkdir -p "$d/sub"
  chmod 000 "$d/sub"
  "$CHECK" --skip-env "$d" >"$d/out" 2>&1
  rc=$?
  chmod 755 "$d/sub" 2>/dev/null || true
  if [ "$rc" -ne 1 ] || ! grep '^FAIL' "$d/out" | grep -F -q 'grep 出错'; then
    bad=1
    printf '不符 grep 出错：期望 rc=1 且输出含 grep 出错，实际 rc=%s\n' "$rc"
    sed 's/^/  /' "$d/out"
  fi
fi

neg 'disk-' <<'EOF'
disk-xxxxxxxxxxxxxxxxxxxx
EOF
neg 'task-' <<'EOF'
task-yyyyyyyyyyyyyyyyyyyy
EOF
neg '连字符不是 sk- 左边界' <<'EOF'
-sk-FAKEFAKEFAKEFAKE12
EOF
neg '@probe.invalid 邮箱' <<'EOF'
probe-user@probe.invalid
EOF
neg '路径里带邮箱的 URL' <<'EOF'
https://cdn.example.com/path/user:name@probe.invalid/file
EOF
neg 'O3 Authorization 占位符' <<'EOF'
Authorization: Bearer <redacted>
EOF
neg 'O3 Authorization 方括号占位符' <<'EOF'
Authorization:[Bearer <redacted>]
EOF
pos "$R_AUTH" 'O3 Authorization 真值' <<'EOF'
Authorization: Bearer realtoken
EOF
pos "$R_AUTH" 'O3 Authorization 方括号真值' <<'EOF'
Authorization:[Bearer realtoken]
EOF

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d"
printf '%s\n' 'note supersecretvalue1 here' >"$d/f.txt"
printf '%s\n' 'MY_API_KEY=supersecretvalue1' >"$STACK_DIR/.env"
"$CHECK" "$d" >"$d/out" 2>&1
rc=$?
if [ "$rc" -ne 1 ] || ! grep '^FAIL' "$d/out" | grep -F -q -- "$R_ENV" || grep -F -q 'supersecretvalue1' "$d/out"; then
  bad=1
  printf '不符 .env 密钥值比对：期望 rc=1、输出含规则名且不打印值，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d"
printf '%s\n' 'plain note without secrets' >"$d/f.txt"
printf '%s\n' 'SHORT_TOKEN=xY!9' >"$STACK_DIR/.env"
"$CHECK" "$d" >"$d/out" 2>&1
rc=$?
if [ "$rc" -ne 0 ] || grep -q '^FAIL' "$d/out" \
  || ! grep -F -q 'WARN' "$d/out" || ! grep -F -q 'SHORT_TOKEN' "$d/out" \
  || ! grep '^WARN' "$d/out" | grep -F -q '没有比对任何非空值' \
  || grep -F -q 'xY!9' "$d/out"; then
  bad=1
  printf '不符 .env 短值：期望 rc=0、WARN 列出 SHORT_TOKEN 且提示 0 个非空值、不打印值，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi
rm -f "$STACK_DIR/.env"

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d"
printf '%s\n' 'plain note without secrets' >"$d/f.txt"
printf '%s\n' 'EMPTY_TOKEN=' 'SHORT_TOKEN=aB!2' >"$STACK_DIR/.env"
"$CHECK" "$d" >"$d/out" 2>&1
rc=$?
if [ "$rc" -ne 0 ] || grep -q '^FAIL' "$d/out" \
  || ! grep -F -q '空值' "$d/out" || ! grep -F -q '短值' "$d/out" \
  || ! grep -F -q 'SHORT_TOKEN' "$d/out" || grep -F -q 'EMPTY_TOKEN' "$d/out" \
  || grep -F -q 'aB!2' "$d/out"; then
  bad=1
  printf '不符 .env 空值：期望 rc=0、空值和短值分开、只列出 SHORT_TOKEN，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi
rm -f "$STACK_DIR/.env"

pos_s "$R_COOKIE" 'Cookie' 'Cookie: session=fake'
pos_s "$R_AUTH" 'Authorization Token' 'Authorization: Token realtoken1'
pos_s "$R_AUTH" 'Authorization ApiKey' 'Authorization: ApiKey realtoken1'
pos_s "$R_AUTH" 'Authorization Digest' 'Authorization: Digest realtoken1'
pos_s "$R_BEARER" 'Bearer 反斜杠 t' 'Bearer\trealtoken1'
pos_s "$R_BEARER" 'Bearer %20' 'Bearer%20realtoken1'
pos_s "$R_BEARER" 'Bearer NBSP' "$(printf 'Bearer\302\240realtoken1')"
pos_s "$R_AUTH" 'Authorization 反斜杠 t' 'Authorization:\tBasic ZmFrZXBhc3Mabc'
pos_s "$R_AUTH" 'Authorization Basic 反斜杠 t' 'Authorization: Basic\tZmFrZXBhc3Mabc'
pos_s "$R_BEARER" 'Bearer+（表单编码空格）' 'Bearer+realtoken1'
pos_s "$R_BEARER" 'Bearer %09' 'Bearer%09realtoken1'
pos_s "$R_AUTH" 'Authorization 表单编码 +' 'Authorization=Basic+ZmFrZXBhc3Mabc'
pos_s "$R_AUTH" 'Authorization %09' 'Authorization:%09Bearer%09realtoken1'
pos_s "$R_SK" 'sk- 前接 %20' '%20sk-FAKEFAKEFAKEFAKE12'
pos_s "$R_SK" 'sk- 前接 %3D' 'api_key%3Dsk-FAKEFAKEFAKEFAKE12'
pos_s "$R_SK" 'sk- 前接反斜杠 b' 'prefix\bsk-FAKEFAKEFAKEFAKE12'
pos_s "$R_SK" 'sk- 前接反斜杠 f' 'prefix\fsk-FAKEFAKEFAKEFAKE12'
pos_s "$R_MAIL" '邮箱白名单不放宽' 'x@probe.invalid.example.com'

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d"
printf '%s\n' 'plain note' >"$d/f.txt"
rm -rf "$STACK_DIR/.env"
"$CHECK" "$d" >"$d/out" 2>&1
rc=$?
if [ "$rc" -ne 1 ] || ! grep '^FAIL' "$d/out" | grep -F -q '读不到'; then
  bad=1
  printf '不符 无 .env：期望 rc=1 且含读不到，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d"
printf '%s\n' 'plain note' >"$d/f.txt"
rm -rf "$STACK_DIR/.env"
mkdir "$STACK_DIR/.env"
"$CHECK" "$d" >"$d/out" 2>&1
rc=$?
rm -rf "$STACK_DIR/.env"
if [ "$rc" -ne 1 ] || ! grep '^FAIL' "$d/out" | grep -F -q 'grep 出错'; then
  bad=1
  printf '不符 .env 是目录：期望 rc=1 且 FAIL 含 grep 出错，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi

# basic_env 逐值比对的 grep rc≥2：.env 有长值，证据目录里有不可读子目录，不带 --skip-env。
if [ "$(id -u)" -eq 0 ]; then
  n=$((n + 1))
  printf 'SKIP .env 比对 grep 出错（root 下跳过）\n'
else
  n=$((n + 1))
  d=$BASE/c$n
  mkdir -p "$d/sub"
  printf '%s\n' 'plain note' >"$d/f.txt"
  printf '%s\n' 'MY_API_KEY=longenoughvalue123' >"$STACK_DIR/.env"
  chmod 000 "$d/sub"
  "$CHECK" "$d" >"$d/out" 2>&1
  rc=$?
  chmod 755 "$d/sub" 2>/dev/null || true
  rm -f "$STACK_DIR/.env"
  if [ "$rc" -ne 1 ] || ! grep '^FAIL' "$d/out" | grep -F -q '.env 密钥值比对（grep 出错'; then
    bad=1
    printf '不符 .env 比对 grep 出错：期望 rc=1 且 FAIL 含 .env 密钥值比对（grep 出错，实际 rc=%s\n' "$rc"
    sed 's/^/  /' "$d/out"
  fi
fi

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d"
printf '%s\n' 'plain note' >"$d/f.txt"
printf '%s\n' 'OPENAI_APIKEY=OpenAIKeyValueShouldNotLeak99' >"$STACK_DIR/.env"
"$CHECK" "$d" >"$d/out" 2>&1
rc=$?
if [ "$rc" -ne 1 ] || ! grep '^FAIL' "$d/out" | grep -F -q '0 个可比对的赋值' \
  || grep -F -q 'OpenAIKeyValueShouldNotLeak99' "$d/out"; then
  bad=1
  printf '不符 .env 无名可对：期望 rc=1 且含 0 个可比对的赋值，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi
rm -f "$STACK_DIR/.env"

# lib.sh：空输入无法判定；xh 把长度够的值放进 SECRETS，finalize 再换成占位符。OUT 只用临时目录。
# 前置核对不是这一例要测的：在临时 git 仓库里 source lib.sh 的副本，登记的 release 就是该仓库的首个提交，
# 命名空间外没有差异，核对必然通过；与真实仓库 HEAD 是否带产品补丁无关。
n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d/out" "$d/stack" "$d/repo/scripts/agent24/probe" "$d/repo/docs/agent24"
printf '%s\n' 'MY_API_KEY=not-in-the-evidence-xyz' >"$d/stack/.env"
sec=Zq8vN3pLx7Wm
librc=0
(
  set -e
  r=$d/repo
  g() { GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "$r" -c user.name=selftest \
    -c user.email=selftest@invalid -c commit.gpgsign=false "$@"; }
  cp -R "$HERE"/. "$r/scripts/agent24/probe/"
  cp "$HERE/../../../docs/agent24/UPSTREAM-BASELINE.json" "$r/docs/agent24/UPSTREAM-BASELINE.json"
  g init -q
  g add -A
  g commit -q -m base
  c=$(g rev-parse HEAD)
  printf '{"current_baseline":{"release":{"tag":"v0.0.0","commit":"%s"},"product":{"product_patches":"none"}}}\n' \
    "$c" >"$r/docs/agent24/UPSTREAM-BASELINE.json"
  g add -A
  g commit -q -m baseline
) >"$d/repo.log" 2>&1 || librc=$?
if [ "$librc" -ne 0 ]; then
  printf '不符 lib 空输入与凭据替换：建临时仓库失败 rc=%s\n' "$librc"
  sed 's/^/  /' "$d/repo.log"
fi
(
  [ "$librc" -eq 0 ] || exit 1
  OUT=$d/out STACK_DIR=$d/stack
  export OUT STACK_DIR
  # shellcheck disable=SC1091
  source "$d/repo/scripts/agent24/probe/lib.sh"
  ck_absent "空输入" "" x 0
  grep -F -q '输入为空，无法判定' "$CHK" || exit 1
  grep -F -q '"ok":false' "$CHK" || exit 1
  xh X-Test "$sec"
  printf 'note %s here\n' "$sec" >"$EV/note.txt"
  finalize
) >"$d/log" 2>&1
rc=$?
if [ "$rc" -ne 0 ] || ! grep -F -q '<redacted>' "$d/out/note.txt" \
  || grep -F -q "$sec" "$d/out/note.txt"; then
  bad=1
  printf '不符 lib 空输入与凭据替换：期望 rc=0、OUT 里是占位符，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/log"
fi

# lib.sh 前置核对：命名空间外与登记 release 不同时 exit 3，区分已登记补丁与未登记差异。
# 临时仓库首个提交含 probe 副本、登记文件和命名空间外文件（内容 a）；登记 release 为该提交，
# 再把该文件改成 b 提交。参数：用例目录、命名空间外文件、product_patches 的 JSON 值。
mk_patch_repo() {
  (
    set -e
    r=$1/repo
    g() { GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git -C "$r" -c user.name=selftest \
      -c user.email=selftest@invalid -c commit.gpgsign=false "$@"; }
    mkdir -p "$r/scripts/agent24/probe" "$r/docs/agent24" "$(dirname "$r/$2")"
    cp -R "$HERE"/. "$r/scripts/agent24/probe/"
    cp "$HERE/../../../docs/agent24/UPSTREAM-BASELINE.json" "$r/docs/agent24/UPSTREAM-BASELINE.json"
    printf 'a\n' >"$r/$2"
    g init -q
    g add -A
    g commit -q -m base
    c=$(g rev-parse HEAD)
    printf '{"current_baseline":{"release":{"tag":"v0.0.0","commit":"%s"},"product":{"product_patches":%s}}}\n' \
      "$c" "$3" >"$r/docs/agent24/UPSTREAM-BASELINE.json"
    printf 'b\n' >"$r/$2"
    g add -A
    g commit -q -m patch
  )
}
# 在子 shell 里 source 临时仓库的 lib.sh；前置核对失败会先 exit 3，走不到需要 docker 的地方。
src_patch_repo() {
  (
    OUT=$1/out STACK_DIR=$1/stack
    export OUT STACK_DIR
    # shellcheck disable=SC1091
    source "$1/repo/scripts/agent24/probe/lib.sh"
  )
}

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d/out" "$d/stack"
rc=0
mk_patch_repo "$d" third_party/x.txt '[{"id":"T-PATCH-1","paths":["third_party/x.txt"]}]' >"$d/repo.log" 2>&1 || rc=$?
if [ "$rc" -eq 0 ]; then
  rc=0
  src_patch_repo "$d" >"$d/log" 2>"$d/err" || rc=$?
else
  cp "$d/repo.log" "$d/err"
  rc=-1
fi
if [ "$rc" -ne 3 ] || ! grep -F -q 'fork 有已登记的产品补丁' "$d/err" \
  || ! grep -F -q 'T-PATCH-1' "$d/err" || grep -F -q '未登记' "$d/err"; then
  bad=1
  printf '不符 前置核对：已登记补丁仍拒绝：期望 rc=3 且提示已登记补丁 T-PATCH-1、不含未登记，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/err"
fi

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d/out" "$d/stack"
rc=0
mk_patch_repo "$d" third_party/y.txt '"none"' >"$d/repo.log" 2>&1 || rc=$?
if [ "$rc" -eq 0 ]; then
  rc=0
  src_patch_repo "$d" >"$d/log" 2>"$d/err" || rc=$?
else
  cp "$d/repo.log" "$d/err"
  rc=-1
fi
if [ "$rc" -ne 3 ] || ! grep -F -q '未登记的改动' "$d/err" \
  || ! grep -F -q 'third_party/y.txt' "$d/err"; then
  bad=1
  printf '不符 前置核对：未登记差异拒绝并列出路径：期望 rc=3 且列出 third_party/y.txt，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/err"
fi

# 已登记路径按整行比对：根目录 Cargo.toml 只是已登记 third_party/anydoc-go/Cargo.toml 的子串，
# 仍算未登记的改动（lib.sh 改成 grep -Fq 时这一例会失败）。
n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d/out" "$d/stack"
rc=0
mk_patch_repo "$d" Cargo.toml '[{"id":"T-PATCH-2","paths":["third_party/anydoc-go/Cargo.toml"]}]' \
  >"$d/repo.log" 2>&1 || rc=$?
if [ "$rc" -eq 0 ]; then
  rc=0
  src_patch_repo "$d" >"$d/log" 2>"$d/err" || rc=$?
else
  cp "$d/repo.log" "$d/err"
  rc=-1
fi
if [ "$rc" -ne 3 ] || ! grep -F -q '未登记的改动' "$d/err" || ! grep -F -q 'Cargo.toml' "$d/err" \
  || grep -F -q '已登记的产品补丁' "$d/err"; then
  bad=1
  printf '不符 前置核对：子串不算已登记：期望 rc=3 且报未登记的改动 Cargo.toml，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/err"
fi

# check-redaction.sh 不带目录：逐个扫描 probe-evidence 下的全部子目录，任何一个失败即失败；隐藏目录跳过。
# 驱动按自身位置找仓库根，所以复制到临时目录的同样结构里运行。参数：用例目录。
mk_ev_root() {
  local r=$1/root
  mkdir -p "$r/scripts/agent24/probe" "$r/docs/agent24/probe-evidence/aaaa1111" \
    "$r/docs/agent24/probe-evidence/bbbb2222" "$r/docs/agent24/probe-evidence/.bbbb2222.old.x" || return 1
  cp "$HERE/check-redaction.sh" "$HERE"/redact-[0-9][0-9]-*.sh "$r/scripts/agent24/probe/" || return 1
  printf '%s\n' 'plain note' >"$r/docs/agent24/probe-evidence/aaaa1111/f.txt"
  printf '%s\n' 'plain note' >"$r/docs/agent24/probe-evidence/bbbb2222/f.txt"
  printf '%s\n' 'key sk-SelftestHiddenDirAbcdef123456' >"$r/docs/agent24/probe-evidence/.bbbb2222.old.x/f.txt"
}
n=$((n + 1))
d=$BASE/c$n
rc=0
mk_ev_root "$d" || rc=-1
[ "$rc" -eq 0 ] && { "$d/root/scripts/agent24/probe/check-redaction.sh" --skip-env >"$d/out" 2>&1; rc=$?; }
if [ "$rc" -ne 0 ] || grep -q '^FAIL' "$d/out" || ! grep -F -q '脱敏自检通过：' "$d/out" \
  || [ "$(grep -c '^脱敏自检通过：.*/probe-evidence/[ab]\{4\}[0-9]\{4\}$' "$d/out")" != 2 ] \
  || ! grep -F -q '跳过隐藏目录' "$d/out"; then
  bad=1
  printf '不符 无参扫描全部子目录（全部通过）：期望 rc=0、两个子目录都通过、隐藏目录跳过，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi
n=$((n + 1))
d=$BASE/c$n
rc=0
mk_ev_root "$d" || rc=-1
printf '%s\n' 'key sk-SelftestSecondDirAbcdef123456' >"$d/root/docs/agent24/probe-evidence/bbbb2222/f.txt"
[ "$rc" -eq 0 ] && { "$d/root/scripts/agent24/probe/check-redaction.sh" --skip-env >"$d/out" 2>&1; rc=$?; }
if [ "$rc" -ne 1 ] || ! grep '^FAIL' "$d/out" | grep -F -q "$R_SK" \
  || ! grep -F -q '证据目录未通过' "$d/out" || ! grep -F -q 'bbbb2222' "$d/out" \
  || ! grep -F -q '脱敏自检通过：' "$d/out" || grep -F -q 'SelftestSecondDir' "$d/out"; then
  bad=1
  printf '不符 无参扫描全部子目录（第二个失败）：期望 rc=1、报出 bbbb2222、第一个仍通过，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi

SELFTEST_MIN_10=61
SELFTEST_DONE_10=1
