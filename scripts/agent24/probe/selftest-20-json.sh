#!/usr/bin/env bash
# jq 路径。需要 redact-20；用到文本规则时，完整自测里 redact-30 也在。
# 值以转义引号开头：正则路径在引号处截断，只剩 jq 会打出 f.json:键。
n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d"
keys='session_id sessionid hmac signature pwd auth invite_code sig authorization passwd credential refresh client_secret private_key passphrase session-id'
body='{'
comma=
for k in $keys; do
  body="${body}${comma}\"${k}\":\"\\\"sess-123456\""
  comma=,
done
printf '%s\n' "${body}}" >"$d/f.json"
"$CHECK" --skip-env "$d" >"$d/out" 2>&1
rc=$?
miss=
for k in $keys; do
  grep -E -q "f.json:${k}$" "$d/out" || miss="$miss $k"
done
if [ "$rc" -ne 1 ] || [ -n "$miss" ]; then
  bad=1
  printf '不符 jq 逐键路径：期望各键的 f.json:键，缺:%s rc=%s\n' "$miss" "$rc"
  sed 's/^/  /' "$d/out"
fi
pos "$R_FIELD" '数组第 2 个元素' f.json <<'EOF'
{"password":["ab","second-element-1"]}
EOF
pos "$R_FIELD" '值以 \" 开头' f.json <<'EOF'
{"token":"\"abcdef"}
EOF
pos "$R_FIELD" '值以 \\ 开头' f.json <<'EOF'
{"token":"\\abcdef"}
EOF
pos "$R_FIELD" '<redacted 真值>' f.json <<'EOF'
{"pwd":"<redacted 真值>"}
EOF
pos "$R_HDR" 'JSON X-External-User-Token' f.json <<'EOF'
{"X-External-User-Token":"jsonheadervalue"}
EOF
pos "$R_KV" 'O1 字符串日志行' f.json <<'EOF'
{"line":"ts=1 password=realsecret123"}
EOF
run_case 1 '数组真值在前（JSON）' "$R_FIELD" f.json "" 'password.0' 'real-secret-1' <<'EOF'
{"password":["real-secret-1","<redacted>"]}
EOF
run_case 1 '多行数组' "$R_FIELD" f.json "" 'password.2' 'real-secret-value' <<'EOF'
{
  "password": [
    "ab",
    "<redacted>",
    "real-secret-value"
  ]
}
EOF
run_case 1 'O1 内嵌 JSON token' "$R_FIELD" f.json "" 'body.(json).token' '' <<'EOF'
{"body":"{\"token\":\"realsecret123\"}"}
EOF
run_case 1 'G5 重复键第一个值' "$R_FIELD" f.json "" '' 'firstsecret1' <<'EOF'
{"password":"firstsecret1","password":"<redacted>"}
EOF

neg '<redacted>' f.json <<'EOF'
{"password":"<redacted>"}
EOF
neg '<redacted:hmac_secret>' f.json <<'EOF'
{"hmac_secret":"<redacted:hmac_secret>"}
EOF
neg '纯星号 ***' f.json <<'EOF'
{"password":"***"}
EOF
neg '六位星号占位符' f.json <<'EOF'
{"password":"******"}
EOF
neg 'author 整名不是 auth' f.json <<'EOF'
{"author":"someone-long"}
EOF
neg 'design 整名不是 sig' f.json <<'EOF'
{"design":"not-a-signature-value"}
EOF
neg '布尔和数字值' f.json <<'EOF'
{"password":true,"token":false,"secret":123456,"api_key":0}
EOF
neg '数组元素是占位符' f.json <<'EOF'
{"password":["ab","<redacted>"]}
{"token":["ab","******"]}
EOF
neg 'O1 内嵌 JSON 占位符' f.json <<'EOF'
{"body":"{\"token\":\"<redacted>\"}"}
EOF
neg 'O1 字符串日志占位符' f.json <<'EOF'
{"line":"ts=1 password=<redacted>"}
EOF
neg 'JSON 主体标签 key:noretr' f.json <<'EOF'
{
  "actor": "key:noretr"
}
EOF
pos "$R_KV" '整个 JSON 字符串 key:noretr' f.json <<'EOF'
"key:noretr"
EOF
neg 'O5 内嵌 actor 标签不报' f.json <<'EOF'
{"msg":"{\"actor\":\"key:noretr\"}"}
EOF
pos "$R_KV" 'O5 note 里的 key:noretr' f.json <<'EOF'
{"note":"key:noretr"}
EOF
neg 'O7 JSON authorization 占位符' f.json <<'EOF'
{"authorization":"Bearer <redacted>"}
EOF
neg 'O7 JSON authorization 方括号占位符' f.json <<'EOF'
{"authorization":"[Bearer <redacted>]"}
EOF
neg 'G6 authorization 数组占位符' f.json <<'EOF'
{"authorization":["Bearer <redacted>"]}
EOF
neg 'G6 authorization 两个空格' f.json <<'EOF'
{"authorization":"Bearer  <redacted>"}
EOF
neg 'O2 JSON query 占位符' f.json <<'EOF'
{"url":"https://probe.example/cb?token=<redacted>&x=1"}
EOF
neg 'O2 JSON 凭据头占位符' f.json <<'EOF'
{"line":"X-API-Key: <redacted>"}
EOF
neg 'O2 JSON URL 口令占位符' f.json <<'EOF'
{"line":"redis://:<redacted>@127.0.0.1/0"}
EOF
neg 'O2 JSON map 占位符' f.json <<'EOF'
{"line":"map[X-Api-Key:[<redacted>]]"}
EOF

o8key=abcdefghijklmnopqrstuvwxyz0123456789EXTRA
run_case 1 'O8 长键不打印原文' "$R_FIELD" f.json "" '<key len=41>' "$o8key" <<EOF
{"sessions":{"$o8key":{"token":"realsecret12"}}}
EOF
probe_key=probe-0123456789abcdef01234567
run_case 1 'O3 probe 键不打印原文' "$R_FIELD" f.json "" '<key len=30>' "$probe_key" <<EOF
{"$probe_key":{"token":"realsecret12"}}
EOF

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d"
cat >"$d/f.json" <<'EOF'
{"bs":"pw\\secret1","lt":"pw<secret1","gt":"pw>secret1","amp":"pw&secret1"}
EOF
cat >"$STACK_DIR/.env" <<'EOF'
BS_TOKEN=pw\secret1
LT_TOKEN=pw<secret1
GT_TOKEN=pw>secret1
AMP_TOKEN=pw&secret1
EOF
"$CHECK" "$d" >"$d/out" 2>&1
rc=$?
ok_env=1
for need in 'f.json:bs (BS_TOKEN)' 'f.json:lt (LT_TOKEN)' 'f.json:gt (GT_TOKEN)' 'f.json:amp (AMP_TOKEN)'; do
  grep -F -q -- "$need" "$d/out" || ok_env=0
done
if [ "$rc" -ne 1 ] || [ "$ok_env" -ne 1 ] \
  || ! grep '^FAIL' "$d/out" | grep -F -q -- "$R_ENV" \
  || grep -F -q -- 'pw\secret1' "$d/out" \
  || grep -F -q -- 'pw<secret1' "$d/out" \
  || grep -F -q -- 'pw>secret1' "$d/out" \
  || grep -F -q -- 'pw&secret1' "$d/out"; then
  bad=1
  printf '不符 .env 解码视图：期望 rc=1、四处 JSON 路径且不打印值，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi
rm -f "$STACK_DIR/.env"

real_jq=$(command -v jq)
mkdir -p "$BASE/bin"
cat >"$BASE/bin/jq" <<EOF
#!/bin/sh
"$real_jq" "\$@"
rc=\$?
if [ "\$rc" -ge 2 ]; then
  exit 5
fi
exit \$rc
EOF
chmod +x "$BASE/bin/jq"
o2_path=$PATH
PATH="$BASE/bin:$PATH"
export PATH
neg 'O2 txt 占位符' <<'EOF'
password=<redacted>
EOF
neg 'O2 disk-' <<'EOF'
disk-xxxxxxxxxxxxxxxxxxxx
EOF
neg 'O2 probe.invalid' <<'EOF'
probe-user@probe.invalid
EOF
pos "$R_KV" 'O2 txt 正例' <<'EOF'
password=supersecret1
EOF
PATH=$o2_path
export PATH

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d" "$BASE/jqfail"
printf '%s\n' 'plain note without secrets' >"$d/f.txt"
cat >"$BASE/jqfail/jq" <<'EOF'
#!/bin/sh
exit 2
EOF
chmod +x "$BASE/jqfail/jq"
o5_path=$PATH
PATH="$BASE/jqfail:$PATH"
export PATH
"$CHECK" --skip-env "$d" >"$d/out" 2>&1
rc=$?
PATH=$o5_path
export PATH
if [ "$rc" -ne 1 ] || ! grep -F -q 'jq 不可用' "$d/out"; then
  bad=1
  printf '不符 jq 不可用：期望 rc=1 且输出含 jq 不可用，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi

run_case 1 'db.password' "$R_FIELD" f.json "" 'f.json:<key len=11>' '' <<<'{"db.password":"zq9realsecret"}'
run_case 1 'db password' "$R_FIELD" f.json "" 'f.json:<key len=11>' '' <<<'{"db password":"zq9realsecret"}'
run_case 1 'db:password' "$R_FIELD" f.json "" 'f.json:<key len=11>' '' <<<'{"db:password":"zq9realsecret"}'
run_case 1 'Api Key' "$R_FIELD" f.json "" 'f.json:<key len=7>' '' <<<'{"Api Key":"zq9realsecret"}'
run_case 1 'settings.storage.secret_key' "$R_FIELD" f.json "" 'f.json:<key len=27>' '' <<<'{"settings.storage.secret_key":"zq9realsecret"}'
run_case 1 '点号祖先键' "$R_FIELD" f.json "" 'f.json:<key len=20>.v' '' <<<'{"settings.llm.api_key":{"v":"zq9realsecret"}}'
run_case 1 'credential.value' "$R_FIELD" f.json "" 'f.json:credential.value' '' <<<'{"credential":{"value":"abcdefgh12"}}'
run_case 1 '祖先键 6 字符' "$R_FIELD" f.json "" 'f.json:password.v' '' <<<'{"password":{"v":"abcdef"}}'
run_case 1 '占位符子串' "$R_FIELD" f.json "" 'f.json:token' '' <<<'{"token":"\"real*secret"}'
run_case 1 '6 字符边界' "$R_FIELD" f.json "" 'f.json:token' '' <<<'{"token":"\"abcde"}'
neg_s '5 字符边界' '{"token":"\"abcd"}' f.json
pos_s "$R_SK" 'dv_collect unicode sk-' '{"m":"sk-\u0046AKEFAKEFAKEFAKE12"}' f.json
run_case 1 'eyJ 短键隐藏' "$R_FIELD" f.json "" '<key len=11>' 'eyJabcdefgh' <<<'{"eyJabcdefgh":{"token":"realsecret12"}}'
neg_s 'auth Bearer 占位符' '{"auth":"Bearer <redacted>"}' f.json
neg_s 'authorization Bearer 方括号占位符' '{"authorization":"Bearer [<redacted>]"}' f.json
neg_s 'oauth 不是 auth' '{"oauth":"not-a-secret-value"}' f.json
neg_s 'settings.sandbox.docker' '{"settings.sandbox.docker":"docker_backend_disabled"}' f.json

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d" "$BASE/jqerr"
printf '%s\n' '{"password":{"v":"zq9realsecret"}}' >"$d/f.json"
real_jq=$(command -v jq)
cat >"$BASE/jqerr/jq" <<EOF
#!/bin/sh
for a in "\$@"; do
  case \$a in
    *emit*) echo jq-shim >&2; exit 0 ;;
  esac
done
exec "$real_jq" "\$@"
EOF
chmod +x "$BASE/jqerr/jq"
oerr=$PATH
PATH="$BASE/jqerr:$PATH"
export PATH
"$CHECK" --skip-env "$d" >"$d/out" 2>&1
rc=$?
PATH=$oerr
export PATH
if [ "$rc" -ne 1 ] || ! grep '^FAIL' "$d/out" | grep -F -q 'jq 出错'; then
  bad=1
  printf '不符 jq 遍历出错：期望 rc=1 且 FAIL 含 jq 出错，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d"
printf '%s\n' '{"log":"first line\nsecond pw\\secret1"}' >"$d/f.json"
printf '%s\n' 'BS_TOKEN=pw\secret1' >"$STACK_DIR/.env"
"$CHECK" "$d" >"$d/out" 2>&1
rc=$?
if [ "$rc" -ne 1 ] || ! grep -F -q 'f.json:log' "$d/out" \
  || ! grep '^FAIL' "$d/out" | grep -F -q -- "$R_ENV" \
  || grep -F -q 'pw\secret1' "$d/out"; then
  bad=1
  printf '不符 多行 .env：期望 rc=1、f.json:log 且不打印值，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi
rm -f "$STACK_DIR/.env"

# 副本里 basic_scan_dv 改了名：redact-20 直接调用，不存在就 FAIL（证据里没有 JSON 也一样）。
n=$((n + 1)); drv=$BASE/d-nodv; ev=$BASE/d-nodv-ev; mkdir -p "$drv" "$ev"
cp "$CHECK" "$HERE"/redact-*.sh "$drv/"
sed 's/^basic_scan_dv()/basic_scan_dv_gone()/' "$HERE/redact-10-basic.sh" >"$drv/redact-10-basic.sh"
"$drv/check-redaction.sh" --skip-env "$ev" >"$drv/out" 2>&1
rc=$?
if [ "$rc" -ne 1 ] || ! grep -F -q 'FAIL 解码视图补扫函数未定义' "$drv/out" || grep -F -q '脱敏自检通过' "$drv/out"; then
  bad=1
  printf '不符 basic_scan_dv 未定义：期望 rc=1 且含 解码视图补扫函数未定义，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$drv/out"
fi

run_case 1 '跨行重复键第一个值' "$R_FIELD" f.json "" 'f.json:password' 'realsecret123' <<<$'{"password":"realsecret123",\n"password":"<redacted>"}'
neg_s '跨行重复键都是占位符' $'{"password":"<redacted>",\n"password":"***"}' f.json
SELFTEST_MIN_20=63; SELFTEST_DONE_20=1
