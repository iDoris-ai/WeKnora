# shellcheck shell=bash
# KB-CT-01 探测项（矩阵第 1–8、10–16 项；第 9 项在 probe-09-health.sh）。由 run.sh source，依赖 lib.sh 的数据与函数。

U="未测：向量检索（本机无 embedding 模型）"
[ -n "$EN" ] && U="未测：向量检索质量（${EN}）"
SEARCH_KW='"disable_vector_match":true'

kb_ids_have() {   # 响应的 .data[].id 是否同时含两个 KB
  jq --arg a "$1" --arg b "$2" '[.data[].id] | index($a) != null and index($b) != null' "$W/body"
}

p1() {
  call key:empty GET /api/v1/knowledge-bases
  f ids '[.data[].id]'
  ck "空白名单 key 列出全租户 KB（含 KB1、KB2）" true "$(kb_ids_have "$KB1" "$KB2")"
  call key:empty GET "/api/v1/knowledge-bases/$KB2"
  ck "空白名单 key 可读 KB2" 200 "$ST"
  call key:kb1 GET "/api/v1/knowledge-bases/$KB2"
  ck "白名单[KB1] 的 key 读 KB2 被拒" '403|404' "$ST"
  J key:kb1 POST "/api/v1/knowledge-bases/$KB1/hybrid-search" \
    "{\"query_text\":\"probe\",\"knowledge_base_ids\":[\"$KB2\"],$SEARCH_KW}"
  ck "请求体 knowledge_base_ids 越出白名单被拒" '403|404' "$ST"
  call key:noretr GET /api/v1/knowledge-bases
  ck "无 retrieve 的 key 访问 retrieve 路由被拒" 403 "$ST"
  call key:noretr GET /api/v1/auth/me
  ck "apiKeyAny 路由对任意有效 key 放行" 200 "$ST"
  call key:full GET "/api/v1/tenants/$TA/api-keys"
  ck "key 管理不对 API key 开放（含 full_access）" 403 "$ST"
  J key:empty POST /api/v1/knowledge-bases '{"name":"probe-should-deny"}'
  ck "retrieve key 不能建 KB" 403 "$ST"
  J jwt:A POST "/api/v1/tenants/$TA/api-keys" '{"name":"p-zero"}'
  f error .error.message
  obs "建不带 capability 的非 full key 被拒（API 层没有\"空范围\"可表达）" "$ST"
  emit 1 01-scoped-key.json "scoped API key 与空白名单" "25 个 capability 的全集为静态结论，本脚本不逐一实测"
}

p2() {
  local h now tv row x c
  J jwt:A PUT "/api/v1/tenants/$TA/api-principal-config" '{"mode":"direct"}'
  ck "发 direct 返回 400（源码取值是 direct_header，矩阵已按此改正）" 400 "$ST"
  f error .error.message
  J jwt:A PUT "/api/v1/tenants/$TA/api-principal-config" '{"mode":"direct_header"}'
  ck "切到 direct_header" 200 "$ST"
  J key:chat POST /api/v1/sessions "{\"title\":\"PROBE-SESSION-DIRECT-$RID\"}" -H 'X-External-User-ID: probe-ext-a'
  ck "direct：主体 a 建会话" '200|201' "$ST"
  local sa
  sa=$(bj .data.id)
  call key:chat GET "/api/v1/sessions/$sa" -H 'X-External-User-ID: probe-ext-b'
  ck "direct：主体 b 读不到主体 a 的会话" '403|404' "$ST"
  call key:chat GET "/api/v1/sessions/$sa" -H 'X-External-User-ID: probe-ext-a'
  ck "direct：持 key 自称 a 即可读 a 的会话（无签名）" 200 "$ST"
  call key:chat GET "/api/v1/sessions/$sa"
  obs "direct：不带头（回落租户主体）读 a 的会话" "$ST"
  J key:full POST "/api/v1/tenants/$TA/api-principal-test-token" '{"external_user_id":"x"}'
  ck "API key（含 full_access）不能签发主体 token" 403 "$ST"
  for x in a b; do
    J key:chat POST "/api/v1/knowledge-bases/$KB1/hybrid-search" "{\"query_text\":\"probe\",$SEARCH_KW}" \
      -H "X-External-User-ID: probe-ext-$x"
    ck "direct：主体 $x 的 KB 检索不受主体影响" 200 "$ST"
  done

  HMAC="PROBE-HMAC-$RID-$(openssl rand -hex 8)"
  SECRETS+=("$HMAC")
  J jwt:A PUT "/api/v1/tenants/$TA/api-principal-config" "{\"mode\":\"signed_token\",\"hmac_secret\":\"$HMAC\"}"
  f resp '.data | {mode, has_hmac_secret}'
  ck "配置 signed_token" 200 "$ST"
  ck_absent "响应不回显 HMAC 明文" "$(cat "$W/body")" "$(has "$HMAC")" false
  for x in a b; do
    J jwt:A POST "/api/v1/tenants/$TA/api-principal-test-token" "{\"external_user_id\":\"probe-ext-$x\"}"
    take "TK_$x" .data.token "签发主体 $x 的 token"
    SECRETS+=("$(bj .data.token)")
    f ttl .data.expires_in_seconds
  done
  J jwt:A POST "/api/v1/tenants/$TA/api-principal-test-token" '{"external_user_id":"probe-ext-a","expires_in_seconds":7200}'
  ck "服务端签发最长 1 小时" 400 "$ST"
  call key:full GET /api/v1/knowledge-bases
  obs "signed_token 模式下不带 token 的任何租户 key 请求（含 full_access）" "$ST"
  xh X-External-User-Token "$TK_a"
  J key:chat POST /api/v1/sessions "{\"title\":\"PROBE-SESSION-SIGNED-$RID\"}"
  ck "signed：主体 a 建会话" '200|201' "$ST"
  local ss
  ss=$(bj .data.id)
  xh X-External-User-Token "$TK_b"
  call key:chat GET "/api/v1/sessions/$ss"
  ck "signed：主体 b 读不到 a 的会话" '403|404' "$ST"
  call key:chat GET "/api/v1/sessions/$ss"
  ck "signed：缺 token 被拒" 401 "$ST"
  f error .error

  now=$(date +%s)
  for c in "aud 错误:{\"sub\":\"probe-ext-c\",\"tenant_id\":\"$TA\",\"aud\":\"other\",\"exp\":$((now + 600))}" \
    "exp 超过 24h:{\"sub\":\"probe-ext-c\",\"tenant_id\":\"$TA\",\"aud\":\"weknora\",\"exp\":$((now + 90000))}" \
    "无 exp:{\"sub\":\"probe-ext-c\",\"tenant_id\":\"$TA\",\"aud\":\"weknora\"}" \
    "tenant_id 不符:{\"sub\":\"probe-ext-c\",\"tenant_id\":\"$T_X\",\"aud\":\"weknora\",\"exp\":$((now + 600))}"; do
    h=$(jwt_sign "$HMAC" "${c#*:}")
    SECRETS+=("$h")
    xh X-External-User-Token "$h"
    call key:chat GET /api/v1/sessions
    ck "自签 token（${c%%:*}）被拒" 401 "$ST"
  done
  h=$(jwt_sign "$HMAC" "{\"sub\":\"probe-ext-any\",\"tenant_id\":\"$TA\",\"aud\":\"weknora\",\"exp\":$((now + 600))}")
  SECRETS+=("$h")
  xh X-External-User-Token "$h"
  call key:chat GET /api/v1/sessions
  ck "持 HMAC 可自签任意 sub 并被接受（无 jti）" 200 "$ST"
  for x in a b; do
    tv="TK_$x"
    xh X-External-User-Token "${!tv}"
    J key:chat POST "/api/v1/knowledge-bases/$KB1/hybrid-search" "{\"query_text\":\"probe\",$SEARCH_KW}"
    ck "signed：主体 $x 的 KB 检索不受主体影响（ACL 不看主体）" 200 "$ST"
  done

  xh X-External-User-Token "$TK_a"
  J key:full POST /api/v1/knowledge-bases "{\"name\":\"probe-audit-$RID\"}"
  ck "full key 建 KB" '200|201' "$ST"
  row=$(sql "select actor_user_id||'|'||details::text from audit_logs
    where tenant_id=$TA and action='kb.created' and details->>'name'='probe-audit-$RID' order by id desc limit 1")
  ck "审计 actor 是租户 Owner A（本栈无法区分'最早注册'与'Owner'），不是 key/主体" true \
    "$([ "${row%%|*}" = "$U_A" ] && echo true || echo false)"
  ck "审计 details 带 api_key_id" true "$(jq 'has("api_key_id")' <<<"${row#*|}" 2>/dev/null)"
  ck_absent "审计不记外部主体 probe-ext-a" "$row" \
    "$(grep -q probe-ext-a <<<"$row" && echo true || echo false)" false
  obs "读取校验（只读 SQL）：audit_logs 中该条 details 的键" "$(jq -c 'keys' <<<"${row#*|}" 2>/dev/null)"
  J jwt:A PUT "/api/v1/tenants/$TA/api-principal-config" '{"mode":"tenant"}'
  local u2="记忆、MCP OAuth、沙箱凭据归属未测（需模型或沙箱）；会话归属已测；"
  u2="${u2}审计 actor 取\"最早注册\"还是\"Owner\"未区分（本栈里 A 两者兼是，需另建先注册的非 Owner 用户才能分开）"
  emit 2 02-signed-token.json "外部主体 signed_token / direct_header" "$u2"
}

p3() {
  local q="PROBE-SENTINEL-QUERY3-$RID" t0 n u3 slow log
  sleep 1   # docker logs --since 精确到秒：先等 1 秒，保证 t0 之后只有本项的请求
  t0=$(date -u +%FT%TZ)
  J key:empty POST "/api/v1/knowledge-bases/$KB1/hybrid-search" "{\"query_text\":\"$q\",$SEARCH_KW}"
  ck "retrieve key 可用 POST hybrid-search" 200 "$ST"
  f n '.data | length'
  J key:empty POST "/api/v1/knowledge-bases/$KB1/hybrid-search" \
    "{\"query_text\":\"PROBE-SENTINEL-BODY-ALPHA-$RID\",$SEARCH_KW}"
  f n '.data | length'
  f result_keys '.data[0] | keys?'
  n=$(bj '.data | length')
  if [ "${n:-0}" -gt 0 ]; then
    ck "检索命中正文${FD}" true "$(has "BODY-ALPHA-$RID")"
    ck "结果不序列化 content_revision${FD}" false "$(bj '.data[0] | has("content_revision")')"
  fi
  call key:empty GET "/api/v1/knowledge-bases/$KB1/hybrid-search" \
    -H 'Content-Type: application/json' -d "{\"query_text\":\"$q\",$SEARCH_KW}"
  ck "GET 带 JSON body 也可用" 200 "$ST"
  J key:empty POST "/api/v1/knowledge-bases/$KB1/hybrid-search" '{"query_text":'
  obs "畸形 JSON 的状态码" "$ST"
  f error .error
  J key:empty POST "/api/v1/knowledge-bases/$KBV/hybrid-search" "{\"query_text\":\"$q\"}"
  f error .error
  ck "开向量且 embedding 模型不可用的 KB：服务层非 AppError 一律 500" 500 "$ST"
  # 非 AppError 被包成 InternalServerError（错误码 1007），message 是服务层原文，不是通用文案。
  ck "500 响应带回服务层原文（错误码 1007，message 以原始错误开头）" true \
    "$(bj '.error.code == 1007 and ((.error.message // "") | test("^(failed to embed text: |model download failed)"))')"
  J key:empty POST "/api/v1/knowledge-bases/$(uuid)/hybrid-search" "{\"query_text\":\"$q\"}"
  obs "不存在的 KB" "$ST"
  J key:empty POST "/api/v1/knowledge-bases/$KB1/hybrid-search" \
    "{\"query_text\":\"$q\",\"knowledge_base_ids\":[\"$KBX\"],$SEARCH_KW}"
  ck "请求体指向另一租户 KB 被拒" '403|404' "$ST"

  u3="${U}；revision 映射为静态结论"
  if [ "$EMBED" = fake-ollama ]; then
    # 取消：伪 Ollama 对 PROBE-SLOW 延迟 8 秒，客户端 1 秒后断开，看服务端怎么处理。
    # 预期 curl 超时得到 HTTP 000。ALLOW_000=1 只放行这一次。
    slow="PROBE-SLOW-CANCEL-$RID"
    TMO=1 ALLOW_000=1 J key:empty POST "/api/v1/knowledge-bases/$KBF/hybrid-search" "{\"query_text\":\"$slow\"}"
    obs "取消：客户端 1 秒超时断开时 curl 记到的状态码（000 表示未收到响应）" "$ST"
    sleep 3
    log=$(applog "$t0" | grep -F "$slow")
    ck "客户端断开传导到服务层（日志出现 context canceled）${FD}" true \
      "$(grep -q 'context canceled' <<<"$log" && echo true || echo false)"
    ck "取消被映射为 500（访问日志 status_code）${FD}" 500 \
      "$(grep -o 'status_code=[0-9]*' <<<"$log" | head -1 | cut -d= -f2)"
    obs "取消时的错误原文${FD}" \
      "$(grep -o 'error message: .*context canceled latency=' <<<"$log" | head -1 | sed 's/ latency=$//')"
    obs "取消时服务端记录的耗时${FD}" "$(grep -o 'latency=[0-9.]*[a-z]*' <<<"$log" | head -1)"
  else
    u3="${u3}；取消未测（需伪 Ollama 制造慢响应）"
  fi
  sleep 2
  applog "$t0" | grep -F "$q" >"$W/l3"
  ck "访问日志 request_body 记录 query 全文" '[1-9][0-9]*' "$(grep -c 'request_body=' "$W/l3")"
  ck "handler/服务层另行记录 query 全文（非访问日志行）" '[1-9][0-9]*' "$(grep -vc 'request_body=' "$W/l3")"
  emit 3 03-hybrid-search.json "hybrid-search" "$u3" "$EN"
}

p4() {
  local s nc bc u=""
  call key:empty GET "/api/v1/knowledge/$D_ALPHA"
  ck "retrieve key 读 knowledge 元数据" 200 "$ST"
  f meta '.data | {parse_status, has_file_hash: (.file_hash != ""), has_file_path: (.file_path != ""), error_message}'
  for s in spans stages; do
    call key:empty GET "/api/v1/knowledge/$D_ALPHA/$s"
    ck "retrieve key 读 /$s" 200 "$ST"
    f keys '.data | keys?'
    fv contains_body_sentinel "$(has "BODY-ALPHA-$RID")"
  done
  call key:empty GET "/api/v1/chunks/$D_ALPHA?page_size=500"
  ck "retrieve key 列 chunks" 200 "$ST"
  f page '{total, page, page_size, n: (.data | length)}'
  nc=$(bj '.data | length')
  if [ "${nc:-0}" -gt 0 ]; then
    ck "page_size 上限 100${FD}" 100 "$(bj .page_size)"
    ck "chunk 正文可读${FD}" true "$(has "BODY-ALPHA-$RID")"
    f chunk0 '.data[0] | {index_status, content_revision}'
    # 历史修订：改写 beta 的分块，再用 retrieve key 读 revisions，看是否带回旧正文。
    call key:empty GET "/api/v1/chunks/$D_BETA"
    bc=$(bj '.data[0].id // empty')
    J jwt:A PUT "/api/v1/chunks/$D_BETA/$bc" "{\"content\":\"改写后的合成内容 PROBE-SENTINEL-REV2-$RID\"}"
    obs "改写 beta 分块${FD}" "$ST"
    sleep 2
    call key:empty GET "/api/v1/chunks/$D_BETA/$bc/revisions"
    ck "retrieve key 读 revisions${FD}" 200 "$ST"
    f revisions '[.data[]? | {revision}]'
    ck "revisions 带回历史正文${FD}" true "$(has "BODY-BETA-$RID")"
  else
    u="未测：chunk 正文、page_size 上限、revisions（文档解析失败：${PERR}）；"
  fi
  call key:empty GET "/api/v1/chunks/$D_ALPHA?page=abc"
  ck "分页参数错误 400" 400 "$ST"
  call key:empty GET "/api/v1/chunks/by-id/$(uuid)"
  ck "单条 chunk 不存在 404" 404 "$ST"
  f error .error
  call key:kb1 GET "/api/v1/chunks/$D_BETA"
  ck "白名单外 KB 的 chunks 被拒" '403|404' "$ST"
  call key:empty GET "/api/v1/knowledge/$(uuid)"
  obs "不存在的 knowledge" "$ST"
  u="${u}chunk 列表、单条读取、revisions 的服务层 500 原文路径未测（需制造存储层故障）；"
  u="${u}按指定修订读并校验、stale 语义为静态结论"
  emit 4 04-knowledge-chunk-read.json "/knowledge/{id} 与 chunks" "$u" "$EN"
}

p5() {
  local fp
  call key:empty GET "/api/v1/knowledge/$D_ALPHA/download"
  ck "本租户 retrieve key 可下载原件" 200 "$ST"
  ck "原件内容（哨兵）" true "$(has "BODY-ALPHA-$RID")"
  ck "download 带 no-store" '.*no-store.*' "$(hd Cache-Control)"
  call key:kb1 GET "/api/v1/knowledge/$D_BETA/download"
  ck "白名单外下载被拒" '403|404' "$ST"
  call jwt:V GET "/api/v1/knowledge/$D_ALPHA/download"
  ck "JWT Viewer 不能下载" 403 "$ST"
  call jwt:C GET "/api/v1/knowledge/$D_ALPHA/download"
  ck "JWT Contributor 可下载" 200 "$ST"
  J key:empty POST "/api/v1/knowledge-bases/$KB1/knowledge/batch-download" "{\"ids\":[\"$D_ALPHA\"]}"
  ck "batch-download：retrieve key 可取 zip" 200 "$ST"
  fv content_type "$(hd Content-Type)"
  J key:kb1 POST "/api/v1/knowledge-bases/$KB2/knowledge/batch-download" "{\"ids\":[\"$D_BETA\"]}"
  ck "batch-download 白名单外被拒" '403|404' "$ST"

  fp=$(jq -rn --arg s "$FP_ALPHA" '$s | @uri')
  obs "file_path 形态（来自 GET /knowledge/:id）" "${FP_ALPHA%%//*}//…"
  call key:empty GET "/files?file_path=$fp"
  ck "/files：空白名单 retrieve key 可取原件" 200 "$ST"
  ck "/files 内容（哨兵）" true "$(has "BODY-ALPHA-$RID")"
  ck "/files 缓存头 public, max-age=86400" 'public, max-age=86400' "$(hd Cache-Control)"
  call key:kb1 GET "/files?file_path=$fp"
  ck "/files：非空白名单 key 被拒" '401|403' "$ST"
  call key:noretr GET "/files?file_path=$fp"
  ck "/files：无 retrieve 的 key 被拒" '401|403' "$ST"
  call jwt:V GET "/files?file_path=$fp"
  ck "/files：本租户 Viewer 可取原件（门槛低于 download）" 200 "$ST"
  call jwt:X GET "/files?file_path=$fp"
  ck "/files：另一租户被拒" '403|404' "$ST"
  call none GET /r/probe-not-a-token
  obs "/r/:token 鉴权前、无效 token" "$ST"
  call none GET "/api/v1/files/presigned?file_path=$fp"
  obs "presigned 无签名" "$ST"
  obs "resource_access_grants 行数（只读 SQL；本栈未设 APP_EXTERNAL_URL，无签发点）" \
    "$(sql 'select count(*) from resource_access_grants')"
  emit 5 05-download.json "原件出口 download / batch-download / files" \
    "跨租户共享（组织）未测；/r/:token 与 presigned 能否覆盖原件未测（本栈未设 APP_EXTERNAL_URL，GetFileURL 不签发）"
}

p6() {
  call key:empty GET "/api/v1/knowledge/$D_ALPHA/preview"
  ck "retrieve key 可 preview" 200 "$ST"
  ck "preview 返回原件字节" true "$(has "BODY-ALPHA-$RID")"
  fv content_type "$(hd Content-Type)"
  call jwt:V GET "/api/v1/knowledge/$D_ALPHA/preview"
  ck "JWT Viewer 可 preview 原件（download 不行）" 200 "$ST"
  call key:empty GET "/api/v1/knowledge/$D_HTML/preview"
  ck "html 强制 octet-stream" 'application/octet-stream.*' "$(hd Content-Type)"
  ck "html 带 nosniff" nosniff "$(hd X-Content-Type-Options)"
  ck "html 以附件下发" 'attachment.*' "$(hd Content-Disposition)"
  emit 6 06-preview.json "/preview" "组织共享、共享 Agent 授予的跨租户 Viewer 未测（未建组织共享）"
}

p7() {
  call key:empty GET /api/v1/system/info
  ck "retrieve key 读不到 /system/info" 403 "$ST"
  call key:mvs GET /api/v1/system/info
  ck "manage_vector_stores key 可读" 200 "$ST"
  f fields '.data | {version, edition, commit_id, build_time, keyword_index_engine, vector_store_engine,
    graph_database_engine, db_version}'
  ck "含 Version、Edition、CommitID、BuildTime、引擎和 DB 迁移版本" true \
    "$(bj '.data | [.version, .edition, .commit_id, .build_time, .keyword_index_engine, .vector_store_engine, .db_version]
      | all(. != null and . != "")')"
  ck "没有协议版本字段" false "$(bj '.data | keys | any(test("protocol"))')"
  call jwt:V GET /api/v1/system/info
  ck "JWT Viewer 可读" 200 "$ST"
  emit 7 07-system-info.json "/system/info" ""
}

p8() {
  call key:noretr GET /api/v1/system/capabilities
  ck "无 retrieve 的 key 也可读 capabilities" 200 "$ST"
  f keys '.data.capabilities | keys'
  ck "capabilities 共 14 个键" 14 "$(bj '.data.capabilities | length')"
  ck "含 v0.8.3 新增的 settings.sandbox.remote" true "$(bj '.data.capabilities | has("settings.sandbox.remote")')"
  f unsupported '[.data.capabilities | to_entries[] | select(.value.supported | not) | {(.key): .value.reason}]'
  call key:empty GET /api/v1/system/capabilities
  ck "retrieve key 可读" 200 "$ST"
  emit 8 08-system-capabilities.json "/system/capabilities" "\"快照而非健康\"为静态结论，未做依赖故障注入"
}

p10() {
  call key:empty GET /api/v1/knowledge-bases -H "X-Tenant-ID: $T_X"
  ck "租户 key 不能用 X-Tenant-ID 切租户" 403 "$ST"
  J key:empty POST "/api/v1/knowledge-bases/$KBX/hybrid-search" "{\"query_text\":\"probe\",$SEARCH_KW}"
  ck "跨租户 hybrid-search 被拒" '403|404' "$ST"
  call key:empty GET "/api/v1/knowledge/$D_GAMMA"
  ck "跨租户读 knowledge 被拒" '403|404' "$ST"
  call key:full GET "/api/v1/knowledge/$D_GAMMA/download"
  ck "跨租户 full key 下载被拒" '403|404' "$ST"
  call jwt:A GET /api/v1/knowledge-bases -H "X-Tenant-ID: $T_X"
  ck "JWT 切到非成员租户被拒" '401|403' "$ST"
  call jwt:V GET /api/v1/knowledge-bases
  ck "同租户 Viewer 看到全部 KB（无 per-user ACL）" true "$(kb_ids_have "$KB1" "$KB2")"
  obs "SystemAdmin 用户数（只读 SQL）" "$(sql 'select count(*) from users where is_system_admin')"
  local u="平台 API key 跨租户读取未测：/system/admin 整组（含提升 SystemAdmin 的 /promote、v0.8.3 新增的 /users 与 /users/status）"
  u="${u}只允许 SystemAdmin 调用，本栈 SystemAdmin 为 0，无法经 API 从零创建；"
  u="${u}只能先注册用户、再设 WEKNORA_BOOTSTRAP_SYSTEM_ADMIN_EMAIL 后重启 app（本次不改环境、不改库）；组织共享与跨租户超管未测"
  emit 10 10-tenant-isolation.json "租户隔离" "$u"
}

p11() {
  local h1 h2 t
  h1="PROBE-HMAC-$RID-r1-$(openssl rand -hex 8)"
  h2="PROBE-HMAC-$RID-r2-$(openssl rand -hex 8)"
  SECRETS+=("$h1" "$h2")
  call key:rev GET /api/v1/knowledge-bases
  ck "撤销前 key 可用" 200 "$ST"
  call jwt:A DELETE "/api/v1/tenants/$TA/api-keys/$KID_rev"
  obs "删除 key" "$ST"
  call key:rev GET /api/v1/knowledge-bases
  ck "撤销后下一请求即 401" 401 "$ST"
  call jwt:C GET "/api/v1/knowledge/$D_ALPHA/download"
  ck "降权前 Contributor 可下载" 200 "$ST"
  J jwt:A PUT "/api/v1/tenants/$TA/members/$U_C" '{"role":"viewer"}'
  obs "降为 viewer" "$ST"
  call jwt:C GET "/api/v1/knowledge/$D_ALPHA/download"
  ck "RBAC 开启时降权下一请求生效" 403 "$ST"
  call jwt:V GET /api/v1/knowledge-bases
  ck "移除前 Viewer 可读" 200 "$ST"
  call jwt:A DELETE "/api/v1/tenants/$TA/members/$U_V"
  obs "移除成员" "$ST"
  call jwt:V GET /api/v1/knowledge-bases
  ck "移除后访问原租户被拒" '401|403' "$ST"
  TN_V=""
  call jwt:V GET /api/v1/knowledge-bases
  ck "移除时吊销该用户全部 token（自己的租户也 401）" 401 "$ST"

  J jwt:A PUT "/api/v1/tenants/$TA/api-principal-config" "{\"mode\":\"signed_token\",\"hmac_secret\":\"$h1\"}"
  J jwt:A POST "/api/v1/tenants/$TA/api-principal-test-token" '{"external_user_id":"probe-ext-r"}'
  take t .data.token "签发主体 token"
  SECRETS+=("$t")
  xh X-External-User-Token "$t"
  call key:chat GET /api/v1/sessions
  ck "signed token 可用" 200 "$ST"
  J jwt:A PUT "/api/v1/tenants/$TA/api-principal-config" "{\"mode\":\"signed_token\",\"hmac_secret\":\"$h2\"}"
  xh X-External-User-Token "$t"
  call key:chat GET /api/v1/sessions
  ck "轮换 HMAC 后旧 token 失效（只能粗粒度）" 401 "$ST"
  J jwt:A PUT "/api/v1/tenants/$TA/api-principal-config" '{"mode":"tenant"}'
  xh X-External-User-Token "$t"
  call key:chat GET /api/v1/sessions
  ck "改回 tenant 模式后带旧 token 仍成功（以租户主体）" 200 "$ST"
  emit 11 11-revocation.json "撤权生效时机" \
    "在途流式请求不取消未测；RBAC 关闭时降权未测（需改 app 环境变量）；单主体撤销无接口（静态）"
}

# 第 12 项：区分"被全局 Auth 拦下"（401，错误体与对照相同）和"鉴权前路由自带鉴权"（401，错误体不同）。
auth_class() {
  if [ "$ST" != 401 ]; then
    echo reachable
  elif [ "$(cat "$W/body")" = "$AUTH401" ]; then
    echo global_auth
  else
    echo own_auth
  fi
}

p12() {
  local p
  call none GET /api/v1/knowledge-bases
  ck "对照：鉴权后路由无凭据 401" 401 "$ST"
  AUTH401=$(cat "$W/body")
  fv body "$AUTH401"
  call none GET /api/v1/probe-unregistered-route
  ck "对照：未注册路由无凭据也被全局 Auth 拦下" global_auth "$(auth_class)"
  for p in /health /api/v1/auth/config /r/probe-x /api/v1/local-browser/extension; do
    call none GET "$p"
    fv auth_class "$(auth_class)"
    ck "鉴权前路由 $p 不经全局 Auth" reachable "$(auth_class)"
  done
  call none GET /mcp/probe-x
  fv auth_class "$(auth_class)"
  f body .
  ck "鉴权前路由 /mcp/:endpoint_id 自带 bearer 鉴权（错误体不同于全局 Auth）" own_auth "$(auth_class)"
  call none GET /swagger/index.html
  ck "release 模式 swagger 无凭据时被全局 Auth 拦下" global_auth "$(auth_class)"
  call jwt:A GET /swagger/index.html
  ck "release 模式不注册 swagger（带凭据 404）" 404 "$ST"
  J none POST /api/v1/auth/register \
    "{\"username\":\"probe-R-$RID\",\"email\":\"probe-R-$RID@probe.invalid\",\"password\":\"$PW\"}"
  ck "自助注册默认开放" '200|201' "$ST"
  f tenant_created '(.user.tenant_id // 0) > 0'
  ck "注册即建个人空间" true "$(bj '(.user.tenant_id // 0) > 0')"
  emit 12 12-pre-auth-routes.json "鉴权前路由" "IM 回调、embed、sandbox WS 未逐条测（需对应渠道配置）"
}

# count_in_response 路径片段 哨兵 < 日志：统计路径匹配、且哨兵出现在 response_body= 之后的行数。
count_in_response() {
  awk -v p="$1" -v s="$2" 'index($0, p) { i = index($0, "response_body="); if (i && index(substr($0, i), s)) n++ }
    END { print n + 0 }'
}

# Langfuse 是否启用：LANGFUSE_ENABLED 非空时按它判断，否则 public/secret key 都非空就自动启用（config.go:79-84）。
langfuse_state() {
  local raw en pk sk on=false rc
  raw=$(docker exec "$(cid app)" sh -c \
    'printf "%s|%s|%s" "$LANGFUSE_ENABLED" "${LANGFUSE_PUBLIC_KEY:+1}" "${LANGFUSE_SECRET_KEY:+1}"')
  rc=$?
  # 取不到时不要写成“追踪启用=false”：那会把出口未测说成已确认关闭。
  if [ "$rc" -ne 0 ] || [ -z "$raw" ]; then
    LF_DESC="取不到（docker exec 失败）"
    LF_UNTESTED="Langfuse 状态取不到，追踪出口未测"
    return
  fi
  IFS='|' read -r en pk sk <<<"$raw"
  if [ -n "$en" ]; then
    case $(tr 'A-Z' 'a-z' <<<"$en") in 1 | true | t | yes | y | on) on=true ;; esac
  elif [ -n "$pk" ] && [ -n "$sk" ]; then
    on=true
  fi
  LF_DESC="LANGFUSE_ENABLED=${en:-<空>}；public key $([ -n "$pk" ] && echo 非空 || echo 为空)；"
  LF_DESC="${LF_DESC}secret key $([ -n "$sk" ] && echo 非空 || echo 为空)；判定追踪启用=$on"
  if [ "$on" = true ]; then
    LF_UNTESTED="Langfuse 追踪已启用但其出口未测"
  else
    LF_UNTESTED="Langfuse 追踪未启用，query 与正文进追踪的出口未测"
  fi
}

p13() {
  local t0 lv q="PROBE-SENTINEL-QUERY13-$RID" hm L=$W/l13 o=$EV/13-log-body.txt
  hm="PROBE-HMAC-$RID-13-$(openssl rand -hex 8)"
  local me="probe-A-$RID@probe.invalid" pv="path=/api/v1/knowledge/$D_ALPHA/preview" s
  SECRETS+=("$hm")
  sleep 1   # 同第 3 项：保证日志窗口里只有本项的请求
  t0=$(date -u +%FT%TZ)
  J key:empty POST "/api/v1/knowledge-bases/$KB1/hybrid-search" "{\"query_text\":\"$q\",$SEARCH_KW}"
  J key:empty POST "/api/v1/knowledge-bases/$KB1/hybrid-search" \
    "{\"query_text\":\"PROBE-SENTINEL-BODY-ALPHA-$RID\",$SEARCH_KW}"
  call key:empty GET "/api/v1/knowledge/$D_ALPHA/preview"
  call key:empty GET "/api/v1/chunks/$D_ALPHA"
  J jwt:A PUT "/api/v1/tenants/$TA/api-principal-config" "{\"mode\":\"signed_token\",\"hmac_secret\":\"$hm\"}"
  J jwt:A PUT "/api/v1/tenants/$TA/api-principal-config" '{"mode":"tenant"}'
  J none POST /api/v1/auth/login "{\"email\":\"$me\",\"password\":\"$PW\"}"
  SECRETS+=("$(bj .token)" "$(bj .refresh_token)")
  sleep 2
  applog "$t0" >"$L"
  lv=$(docker exec "$(cid app)" sh -c 'echo ${LOG_LEVEL:-unset}')

  ck "query 进访问日志 request_body" '[1-9][0-9]*' "$(grep -F "$q" "$L" | grep -c 'request_body=')"
  ck "preview 下发的 text 原件进 response_body（只数 preview 那一行）" '[1-9][0-9]*' \
    "$(grep -F "$pv " "$L" | grep -F "BODY-ALPHA-$RID" | grep -c 'response_body=')"
  # 只在 response_body= 之后找哨兵：以哨兵为 query 的那次请求，request_body 里也有哨兵，不能算。
  ck "自检：只出现在 request_body 的哨兵不计入 response_body 计数" 0 \
    "$(printf '%s\n' 'path=/x/hybrid-search request_body={"query_text":"S-1"} response_body={"data":null}' \
      | count_in_response hybrid-search S-1)"
  if [ "$EMBED" = fake-ollama ]; then
    ck "hybrid-search 命中的正文进 response_body${FD}" '[1-9][0-9]*' \
      "$(count_in_response hybrid-search "BODY-ALPHA-$RID" <"$L")"
    ck "GET /chunks 响应里的正文进 response_body${FD}" '[1-9][0-9]*' \
      "$(grep -F "path=/api/v1/chunks/$D_ALPHA " "$L" | grep -c "BODY-ALPHA-$RID")"
  fi
  ck "hmac_secret 明文进 request_body" '[1-9][0-9]*' "$(sgrep "$hm" "$L" | grep -c 'request_body=')"
  ck "本次登录行的 password 字段被打码" true \
    "$(grep -F "$me" "$L" | grep -F 'path=/api/v1/auth/login' | grep -q '"password":"\*\*\*"' && echo true || echo false)"
  # 日志文件是空的时，0 次不能证明口令没出现。
  ck_absent "口令明文不出现在日志" "$(grep -F "$me" "$L" | grep -F 'path=/api/v1/auth/login')" "$(sgrep "$PW" -c "$L")" 0
  obs "容器 LOG_LEVEL" "$lv"
  langfuse_state
  obs "Langfuse 配置（只记是否非空）" "$LF_DESC"

  {
    echo "# 13 日志是否记录正文（KB-CT-01 PR②）"
    jq -r 'to_entries[] | "# \(.key): \(.value | tostring)"' <<<"$ENVJ"
    echo "# 只摘录含哨兵的日志片段（前后各 ≤100 字符）。"
    echo "# hmac_secret 明文以 <redacted:hmac_secret> 替代，其余凭据以 <redacted> 替代。"
    for s in "$q" "BODY-ALPHA-$RID" "$hm" "$me"; do
      echo "## 哨兵 ${s/$hm/<redacted:hmac_secret>}"
      sgrep "$s" "$L" >"$W/m"
      [ "$s" = "$me" ] && grep -F '"password":' "$W/m" >"$W/m2" && mv "$W/m2" "$W/m"
      { grep -E '(request|response)_body=' "$W/m"; grep -vE '(request|response)_body=' "$W/m"; } \
        | grep -oE -f <(printf '.{0,100}%s.{0,100}\n' "$(sed 's/[][\.*^$/]/\\&/g' <<<"$s")") | head -4 \
        | while IFS= read -r l; do printf '%s\n' "${l//$hm/<redacted:hmac_secret>}"; done
    done
    echo "## 检查"
    jq -r '"- \(.check)：期望 \(.expect)，实际 \(.actual)，\(if .ok then "符合" else "不符" end)"' "$CHK"
    jq -r '"- 观察 \(.observation)：\(.value)"' "$NOTE"
    [ -n "$EN" ] && echo "- 说明：标\"依赖伪 embedding\"的检查，${EN}"
    echo "## 结论：$(jq -rs --arg u "$LF_UNTESTED" \
      'if all(.ok) then "已测部分与矩阵一致；其余未测（\($u)）" else "与矩阵不一致（见上方\"不符\"）" end' "$CHK")"
  } >"$o"
  [ -s "$o" ] || die "13-log-body.txt 为空"
  reset_rec
  echo "  -> 13-log-body.txt"
}

p14() {
  obs "app 容器 WEKNORA_TENANT_ENABLE_RBAC" \
    "$(docker exec "$(cid app)" sh -c 'echo ${WEKNORA_TENANT_ENABLE_RBAC:-<空，默认开启>}')"
  emit 14 14-rbac-off.json "RBAC 可配成放行" \
    "整项未测：需改 app 环境变量 WEKNORA_TENANT_ENABLE_RBAC=false 并重启，本次不改环境"
}

p15() {
  local app srv listen
  app=$(cid app)
  obs "docker port app" "$(docker port "$app" | tr '\n' ';')"
  srv=$(docker exec "$app" sed -n '/^server:/,/^[a-z]/p' /app/config/config.yaml \
    | grep -E '^ +(host|port):' | sed 's/^ *//' | sort | tr -d '"' | tr '\n' ';')
  ck "容器内 config.yaml 默认监听 0.0.0.0:8080" 'host: 0\.0\.0\.0;port: 8080;' "$srv"
  listen=$(docker exec "$app" sh -c 'cat /proc/net/tcp /proc/net/tcp6' | grep -cE ' 0+:1F90 0+:0000 0A ')
  ck "容器内实际监听通配地址 :8080（/proc/net/tcp{,6}，1F90=8080）" '[1-9][0-9]*' "$listen"
  obs "宿主侧发布地址（受测栈约束要求只绑 127.0.0.1，已由前置核对保证）" "$(docker port "$app" 8080/tcp | head -1)"
  obs "GIN_MODE" "$(docker exec "$app" sh -c 'echo $GIN_MODE')"
  emit 15 15-lite-listen.json "监听地址（标准栈）" \
    "Lite 无头二进制未测（本栈为标准栈，结果不能替代 Lite）；端口 0、SERVER_HOST 覆盖、--version 未测"
}

p16() {
  call none POST /api/v1/auth/auto-setup
  ck "标准版 auto-setup 返回 403（非 lite）" 403 "$ST"
  f error .error.message
  call none POST /api/v1/auth/auto-setup -H 'X-WeKnora-Desktop-Token: probe-wrong'
  ck "带任意桌面 token 仍 403" 403 "$ST"
  emit 16 16-auto-setup.json "auto-setup 范围" "无头 Lite 返回 401 未测（标准栈）；桌面版签发行为未测"
}
