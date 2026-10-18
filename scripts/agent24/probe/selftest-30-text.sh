#!/usr/bin/env bash
# 文本规则。夹具都不是必须合法的 JSON，没有 redact-20 时也要能过。
pos_s "$R_QUERY" 'query token' 'https://probe.example/cb?token=fakevalue123456'
pos_s "$R_KV" 'key=value' 'password=supersecret1'
pos_s "$R_KV" 'YAML key: value' 'invite_code: fakevalue123'
pos_s "$R_HDR" '纯文本 X-External-User-Token' 'X-External-User-Token: abcdef1234567890'
pos_s "$R_HDR" '纯文本 X-Auth-Token' 'X-Auth-Token: abcdef1234567890'
pos_s "$R_HDR" 'X-API-Key 头' 'X-API-Key: fakexapikey123456'
pos_s "$R_FIELD" '正则路径内嵌 token' 'ts=1 body={"token":"realsecret1"}'
pos_s "$R_FIELD" '转义 pwd 标量' '{\"pwd\":\"secret1\"}'
pos_s "$R_FIELD" '正则路径 auth 整名' 'ts=1 body={"auth":"realsecret"}'
pos_s "$R_FIELD" '正则路径 sig 整名' 'ts=1 body={"sig":"sigvalue1"}'
pos_s "$R_FIELD" '数组真值在前（txt）' 'ts=1 body={"password":["real-secret-1","<redacted>"]} done'
pos_s "$R_FIELD" 'O3 转义数组真值' 'ts=1 body={\"password\":[\"real-secret-1\",\"<redacted>\"]}'
pos_s "$R_FIELD" 'O4 单引号字段' "note 'password': 'realsecret123' done"
pos_s "$R_FIELD" 'O6 单引号数组元素' "note 'password': ['realsecret123']"
pos_s "$R_FIELD" 'O6 双引号键单引号元素' "x \"password\": ['realsecret123']"
pos_s "$R_FIELD" 'O6 无引号数组元素' '"password": [realsecret123]'
pos_s "$R_FIELD" 'O6 authorization 单引号数组' "'authorization': ['rawtoken123456']"
pos_s "$R_KV" 'KV 双引号' 'password="super secret value"'
pos_s "$R_KV" 'KV 单引号' "token='super secret value'"
pos_s "$R_KV" 'KV 带空格' 'invite_code: has several spaces'
pos_s "$R_KV" 'G4 双引号转义' 'password="ab\"cdefghij"'
pos_s "$R_KV" 'G4 单引号转义' "token='ab\\'cdefghij'"
pos_s "$R_KV" 'G4 规范化副本' 'password=\"supersecret1\"'
pos_s "$R_KV" 'O1 引号左边界等号' '"password=supersecret1"'
pos_s "$R_KV" 'O1 引号左边界冒号' '"password: supersecret1"'
pos_s "$R_KV" 'key: secret' 'key: secret'
pos_s "$R_KV" 'key: "passwd"' 'key: "passwd"'
pos_s "$R_KV" 'key:SECRET' 'key:SECRET'
pos_s "$R_KV" 'key: realsecret1' 'key: realsecret1'
pos_s "$R_KV" 'key:noretr extra' 'key:noretr extra'
pos_s "$R_KV" '纯文本 key:noretr' 'key:noretr'
line=
i=0
while [ "$i" -lt 50 ]; do
  line="${line}token=<redacted> "
  i=$((i + 1))
done
run_case 1 'O3 超过单行检查上限' "$R_KV" f.txt "" '超过单行检查上限' '' <<<"${line}password=realsecret1"
pos_s "$R_KV" 'O7 Authorization 无方式词' 'Authorization: rawtokenvalue123'
pos_s "$R_KV" 'O7 auth 整名' 'auth: realsecret123'
pos_s "$R_KV" 'key 等号真值' 'key=supersecret1'
pos_only_s "$R_KV" 'G2 query 引号值' 'https://probe.example/cb?token="realsecret1"'
pos_only_s "$R_QUERY" '只命中 query' 'https://probe.example/only?access_token=queryonlyvalue1'
pos_only_s "$R_QUERY" 'O4 client_secret' 'https://probe.example/cb?client_secret=realsecret1'
pos_only_s "$R_QUERY" 'O4 session_id' 'https://probe.example/cb?x=1&session_id=sess-123456'
pos_only_s "$R_QUERY" 'O4 invite_code' 'https://probe.example/cb?x=1&invite_code=INVITE12345'
pos_only_s "$R_QUERY" 'O4 api-key' 'https://probe.example/cb?api-key=fakexapikey123'
pos_only_s "$R_HDR" '只命中 X-API-Key' 'X-API-Key: headeronlyvalue1'
pos_only_s "$R_HDR" '只命中 X-External-User-Token' 'X-External-User-Token: headeronlyvalue2'
pos_only_s "$R_HDR" '只命中 X-Auth-Token' 'X-Auth-Token: headeronlyvalue3'
pos_only_s "$R_HDR" 'O3 map 头真值' 'map[X-Api-Key:[realsecret123]]'
pos_s "$R_HDR" 'O6 占位符后拼接' 'X-API-Key: <redacted>realsecret123'
pos_s "$R_HDR" 'O6 占位符空格后真值' 'X-API-Key: <redacted realsecret123>'
pos_s "$R_HDR" 'O6 小于号在值中' 'X-Auth-Token: ab<realsecret123'
pos_s "$R_HDR" '占位符区分大小写' 'X-API-Key: <REDACTED>'
pos_s "$R_BEARER" 'O5 Bearer 制表符' "$(printf 'Bearer\trealtoken12345')"

neg_s 'key=value 占位符' $'password=<redacted>\ninvite_code: ***'
neg '带引号的 KV 占位符' <<'EOF'
password="<redacted>"
token='******'
invite_code: "<redacted:api_key>"
secret: '***'
EOF
neg_s 'O1 引号内占位符' '"password: <redacted>"'
neg_s 'O4 单引号占位符' "note 'password': '<redacted>' done"
neg_s 'O6 单引号数组占位符' "note 'password': ['<redacted>']"
neg_s 'O6 双引号键单引号占位符' "x \"password\": ['******']"
neg_s 'O6 无引号数组占位符' '"password": [<redacted>]'
neg 'O6 authorization 数组占位符' <<'EOF'
'authorization': ['Bearer <redacted>']
'authorization': ['<redacted>']
EOF
neg_s 'query 占位符' $'https://probe.example/cb?token=<redacted>&x=1\nhttps://probe.example/cb?access_token=<redacted:hmac_secret>'
neg_s '凭据头占位符' $'X-API-Key: <redacted>\nX-External-User-Token: ******'
neg_s 'O3 map 头占位符' 'map[X-Api-Key:[<redacted>]]'
neg '转义 JSON 占位符（txt 内嵌）' <<'EOF'
ts=1 body={\"password\":\"<redacted>\"} done
also {\"token\":[\"<redacted>\",\"******\"]}
EOF
neg_s 'O3 转义数组全占位符' 'ts=1 body={\"password\":[\"<redacted>\",\"******\"]}'
neg_s 'G4 转义后是占位符' 'password="\<redacted\>"'

pos_s "$R_KV" '凭据头后的 password' 'X-API-Key: <redacted> password=Zq8vN3pLx7Wm'
pos_s "$R_KV" '凭据头后的 X-Signature' 'map[Content-Type:[application/json] X-Api-Key:[******] X-Signature:[Zq8vN3pLx7Wm]]'
pos_s "$R_KV" '凭据头后的 client_secret' 'X-Auth-Token: ****** client_secret: Zq8vN3pLx7Wm'
neg_s '凭据头后的请求号' 'map[X-Api-Key:[<redacted>] X-Request-Id:[abc]]'
pos_s "$R_FIELD" '双引号数组逗号' 'x "password":["Zq8vN3pLx7,Wm"]'
pos_s "$R_FIELD" '单引号数组逗号' "note 'password': ['realsecret,x1']"
pos_s "$R_FIELD" '数组逗号' '"password": ["realsecret,x1"]'
pos_s "$R_FIELD" 'JSON 数组逗号' '{"password":["Tr0ub4dor,&3"]}'
pos_s "$R_FIELD" '内嵌 JSON 数组逗号' '{"log":"ts=1 body={\"password\":[\"Zq8vN3pLx7,Wm\"]} done"}'
pos_s "$R_FIELD" '半截引号' '"password": ["realsecret1]'
pos_s "$R_FIELD" '占位符后带右括号' 'x "password":["<redacted>","Zq8v]N3pLx7Wm"]'
pos_s "$R_FIELD" '抽不出元素' '"password":[,]'
pos_s "$R_FIELD" '闭合引号后跟杂字符' 'x "password":["abc"Zq8vN3pLx7Wm"]'
pos_s "$R_FIELD" '占位符闭合后跟杂字符' '"password":["<redacted>"Zq8vN3pLx7Wm]'
neg_s '非 JSON 文件里整行 "key:noretr"' $'foo\n  "key:noretr"'
neg_s '占位符数组' '"password": ["<redacted>", "******"]'
neg_s '一行多个 KV 占位符' 'password=<redacted> token=<redacted>'
pos_s "$R_KV" 'actor 里的真值' '{"actor":"key:realsecret1"}'
neg_s 'actor 里的主体标签' '{"actor":"key:noretr"}'
pos_s "$R_KV" '星号不是整值占位符' 'password=fake*secret1'
pos_s "$R_KV" '转义引号不算结束' 'x "token: ab\"cdefghij" y'
pos_s "$R_KV" 'X-Session-Id' 'map[X-Session-Id:[sess-123456]]'
pos_s "$R_KV" 'passphrase' 'passphrase=realsecret1'

n=$((n + 1))
d=$BASE/c$n
mkdir -p "$d"
printf 'prefix\0"password":"supersecret1"\n' >"$d/f.txt"
"$CHECK" --skip-env "$d" >"$d/out" 2>&1
rc=$?
if [ "$rc" -ne 1 ] || ! grep -F -q -- "$R_FIELD" "$d/out" || grep -F -q 'jq 出错' "$d/out" || grep -F -q 'jq 不可用' "$d/out"; then
  bad=1
  printf '不符 G5 NUL 文件：期望 rc=1 且含敏感字段，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$d/out"
fi

# O4 驱动行为。只改临时副本：缺模块、编号不唯一、不认识的名字不加载。
copy_drv() {
  local dest=$1
  mkdir -p "$dest"
  cp "$CHECK" "$dest/check-redaction.sh"
  cp "$HERE"/redact-*.sh "$dest/"
}
first_non00() {
  sed -n 's/^REQUIRED_MODULES="\([^"]*\)".*/\1/p' "$1" | awk '{
    for (i = 1; i <= NF; i++) if ($i != "00") { print $i; exit }
  }'
}
# drv_rc 名称 输出必须含 副本目录 证据目录 [环境变量赋值…]
drv_rc() {
  local name=$1 msg=$2
  env "${@:5}" "$3/check-redaction.sh" --skip-env "$4" >"$4/out" 2>&1
  rc=$?
  if [ "$rc" -ne 1 ] || ! grep -F -q -- "$msg" "$4/out" || grep -F -q '脱敏自检通过' "$4/out"; then
    bad=1
    printf '不符 %s：期望 rc=1 且含 %s，实际 rc=%s\n' "$name" "$msg" "$rc"
    sed 's/^/  /' "$4/out"
  fi
}

# 变量都用 local，不碰框架和其他用例的全局变量。
o4_missing() {
  local drv=$BASE/o4-missing ev=$BASE/o4-missing-ev mod_nn gone rc
  mkdir -p "$ev"
  copy_drv "$drv"
  mod_nn=$(first_non00 "$drv/check-redaction.sh")
  shopt -s nullglob
  gone=("$drv"/redact-"$mod_nn"-*.sh)
  shopt -u nullglob
  if [ -z "$mod_nn" ] || [ "${#gone[@]}" -eq 0 ]; then
    bad=1
    echo "不符 O4 缺少脱敏模块：读不到 REQUIRED_MODULES 里的非 00 模块"
    return
  fi
  rm -f "${gone[@]}"
  "$drv/check-redaction.sh" --skip-env "$ev" >"$ev/out" 2>&1
  rc=$?
  if [ "$rc" -ne 1 ] || ! grep -F -q -- "FAIL 缺少脱敏模块" "$ev/out"; then
    bad=1
    printf '不符 O4 缺少脱敏模块：期望 rc=1 且含 FAIL 缺少脱敏模块，实际 rc=%s\n' "$rc"
    sed 's/^/  /' "$ev/out"
  fi
}
n=$((n + 1))
o4_missing

n=$((n + 1))
drv=$BASE/o4-dup
ev=$BASE/o4-dup-ev
mkdir -p "$ev"
copy_drv "$drv"
if ! cp "$drv/redact-10-basic.sh" "$drv/redact-10-basic-copy.sh"; then
  bad=1
  echo "不符 O4 编号不唯一：复制模块失败"
else
  "$drv/check-redaction.sh" --skip-env "$ev" >"$ev/out" 2>&1
  rc=$?
  if [ "$rc" -ne 1 ] || ! grep -F -q -- "不唯一" "$ev/out"; then
    bad=1
    printf '不符 O4 编号不唯一：期望 rc=1 且含 不唯一，实际 rc=%s\n' "$rc"
    sed 's/^/  /' "$ev/out"
  fi
fi

n=$((n + 1))
drv=$BASE/o4-unknown
ev=$BASE/o4-unknown-ev
mkdir -p "$ev"
copy_drv "$drv"
printf 'bad=1\n' >"$drv/redact-x.sh"
"$drv/check-redaction.sh" --skip-env "$ev" >"$ev/out" 2>&1
rc=$?
if [ "$rc" -ne 0 ] || ! grep -F -q -- "脱敏自检通过" "$ev/out" || grep -F -q -- "redact-x.sh" "$ev/out"; then
  bad=1
  printf '不符 O4 不认识的名字：期望不被加载且干净目录 rc=0，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$ev/out"
fi

n=$((n + 1)); drv=$BASE/d-un; ev=$BASE/d-un-ev; mkdir -p "$ev"; copy_drv "$drv"
chmod 000 "$drv/redact-10-basic.sh"
drv_rc '模块不可读' '模块加载出错' "$drv" "$ev"
chmod u+rwx "$drv/redact-10-basic.sh" 2>/dev/null || true

n=$((n + 1)); drv=$BASE/d-syn; ev=$BASE/d-syn-ev; mkdir -p "$ev"; copy_drv "$drv"
{ printf 'if then\n'; cat "$drv/redact-10-basic.sh"; } >"$drv/redact-10-basic.sh.n"
mv "$drv/redact-10-basic.sh.n" "$drv/redact-10-basic.sh"
drv_rc '模块语法错误' '模块加载出错' "$drv" "$ev"

n=$((n + 1)); drv=$BASE/d-ty; ev=$BASE/d-ty-ev; mkdir -p "$ev"; copy_drv "$drv"
sed 's/basic_named scan/basic_named_typo scan/' "$drv/redact-10-basic.sh" >"$drv/redact-10-basic.sh.n"
mv "$drv/redact-10-basic.sh.n" "$drv/redact-10-basic.sh"
drv_rc '函数名拼错' '规则未跑够' "$drv" "$ev"

n=$((n + 1)); drv=$BASE/d-em; ev=$BASE/d-em-ev; mkdir -p "$ev"; copy_drv "$drv"
sed 's/^REQUIRED_MODULES="[^"]*"/REQUIRED_MODULES=""/' "$drv/check-redaction.sh" >"$drv/check-redaction.sh.n"
mv "$drv/check-redaction.sh.n" "$drv/check-redaction.sh"
chmod +x "$drv/check-redaction.sh"
drv_rc '模块列表为空' 'FAIL 缺少脱敏模块' "$drv" "$ev"

n=$((n + 1)); drv=$BASE/d-nd; ev=$BASE/d-nd-ev; mkdir -p "$ev"; copy_drv "$drv"
sed 's/MOD_DONE_10=1//' "$drv/redact-10-basic.sh" >"$drv/redact-10-basic.sh.n"
mv "$drv/redact-10-basic.sh.n" "$drv/redact-10-basic.sh"
drv_rc '模块缺完成哨兵' 'FAIL 模块未完成' "$drv" "$ev"

# 模块中段 exit 0：驱动不能以 0 退出。
n=$((n + 1)); drv=$BASE/d-ex; ev=$BASE/d-ex-ev; mkdir -p "$ev"; copy_drv "$drv"
sed 's/^unreadable "\$FIND_RC"$/exit 0; &/' "$drv/redact-10-basic.sh" >"$drv/redact-10-basic.sh.n"
mv "$drv/redact-10-basic.sh.n" "$drv/redact-10-basic.sh"
drv_rc '模块中段 exit 0' 'FAIL 驱动未正常结束' "$drv" "$ev"

# 环境里预设了哨兵和次数，模块在顶层提前 return：驱动 source 前要清掉它们。
n=$((n + 1)); drv=$BASE/d-pre; ev=$BASE/d-pre-ev; mkdir -p "$ev"; copy_drv "$drv"
{ printf 'return 0\n'; cat "$drv/redact-10-basic.sh"; } >"$drv/redact-10-basic.sh.n"
mv "$drv/redact-10-basic.sh.n" "$drv/redact-10-basic.sh"
drv_rc '预设哨兵加提前 return' 'FAIL 模块未完成' "$drv" "$ev" MOD_DONE_10=1 MOD_RULES_10=0

# 声明的次数不是整数、超出 4 位，都判失败。
n=$((n + 1)); drv=$BASE/d-ni; ev=$BASE/d-ni-ev; mkdir -p "$ev"; copy_drv "$drv"
sed 's/MOD_RULES_10=[0-9]*/MOD_RULES_10=1x/' "$drv/redact-10-basic.sh" >"$drv/redact-10-basic.sh.n"
mv "$drv/redact-10-basic.sh.n" "$drv/redact-10-basic.sh"
drv_rc '规则次数非整数' 'FAIL 模块未声明规则次数（MOD_RULES_10' "$drv" "$ev"

n=$((n + 1)); drv=$BASE/d-big; ev=$BASE/d-big-ev; mkdir -p "$ev"; copy_drv "$drv"
sed 's/MOD_RULES_10=[0-9]*/MOD_RULES_10=99999999999999999999/' "$drv/redact-10-basic.sh" >"$drv/redact-10-basic.sh.n"
mv "$drv/redact-10-basic.sh.n" "$drv/redact-10-basic.sh"
drv_rc '规则次数超长' 'FAIL 模块未声明规则次数（MOD_RULES_10' "$drv" "$ev"

# 目录里有列表外的模块号。
n=$((n + 1)); drv=$BASE/d-ex40; ev=$BASE/d-ex40-ev; mkdir -p "$ev"; copy_drv "$drv"
printf 'MOD_RULES_40=0; MOD_DONE_40=1\n' >"$drv/redact-40-x.sh"
drv_rc '列表外模块' '脱敏模块 40 不在 REQUIRED_MODULES 里' "$drv" "$ev"

# drv_sed 名称 输出必须含 目录名 sed 表达式：副本里改 redact-10，断言确实改到了，再跑 drv_rc。
drv_sed() {
  local drv=$BASE/$3 ev=$BASE/$3-ev f
  n=$((n + 1)); mkdir -p "$ev"; copy_drv "$drv"; f=$drv/redact-10-basic.sh
  sed "$4" "$f" >"$f.n"
  if cmp -s "$f" "$f.n"; then bad=1; echo "不符 $1：注入没有改到 redact-10"; return; fi
  mv "$f.n" "$f"; drv_rc "$1" "$2" "$drv" "$ev"
}
# 模块顶层 break/continue 会跳出驱动的加载循环：必须因缺完成哨兵而失败。实际次数多于声明也失败。
drv_sed '模块顶层 break' 'FAIL 模块未完成' d-brk 's/^unreadable "\$FIND_RC"$/break; &/'
drv_sed '模块顶层 continue' 'FAIL 模块未完成' d-cont 's/^unreadable "\$FIND_RC"$/continue; &/'
drv_sed '规则次数多于声明' 'FAIL 规则次数多于声明：redact-10-basic.sh' d-more 's/^basic_email$/&; basic_email/'

# 自测框架：用例文件缺完成哨兵时整套 FAIL。副本里只放一个桩 selftest-10：1 个不碰 lib.sh 的反例、
# 下限 1、故意不写哨兵。不复制真的 selftest-10：它会 source lib.sh，而 lib.sh 在临时目录里过不了前置核对。
n=$((n + 1)); drv=$BASE/st-nd; mkdir -p "$drv"; copy_drv "$drv"
sed 's/^REQUIRED_SELFTESTS="[^"]*"/REQUIRED_SELFTESTS="10"/' "$HERE/check-redaction-selftest.sh" \
  >"$drv/check-redaction-selftest.sh"
printf '%s\n' "neg_s '桩：干净文本' 'hello world'" 'SELFTEST_MIN_10=1' >"$drv/selftest-10-basic.sh"
/bin/bash "$drv/check-redaction-selftest.sh" >"$drv/out" 2>&1
rc=$?
if [ "$rc" -ne 1 ] || ! grep -F -q 'FAIL 自测未完成：selftest-10-basic.sh' "$drv/out" \
  || grep -F -q '不符 ' "$drv/out" || grep -F -q '全部符合' "$drv/out"; then
  bad=1
  printf '不符 自测缺完成哨兵：期望 rc=1 且只报 自测未完成，实际 rc=%s\n' "$rc"
  sed 's/^/  /' "$drv/out"
fi

SELFTEST_MIN_30=105
SELFTEST_DONE_30=1
