# WeKnora 能力矩阵（KB-CT-01 PR①）

- **受测 SHA**：`4b363d153122797c857871ce7e4bb63626efc637`（`a24/develop`，简写 `4b363d15`）。
- **与上游的关系**：上游 `v0.8.2` = `3e8b0bfc80b845b2d4b2ed683994748741450a97`；受测 SHA = v0.8.2 + 2 个 Agent24 提交（`9d4e79f`、`4b363d1`）。
  这两个提交只新增 `docs/agent24/*` 和 `.github/workflows/agent24-upstream-sync.yml`，Go 源码、配置和前端与 v0.8.2 逐字节相同（`git diff --stat 3e8b0bf 4b363d1`）。
- **文档性质**：静态源码阅读得出的**探测依据**，每条 `file:line` 都已在受测 SHA 上打开文件核对。PR②（#19、#20，`967d822`）已对 v0.8.2 标准栈实测，证据在 `docs/agent24/probe-evidence/4b363d15/`（2026-10-17，`embed_mode: fake-ollama`：向量为伪造的固定值，只证明分块和读取路径，不代表检索质量）。
  逐项的“证据”一行摘录实测结论与未测部分；运行结论以证据文件为准。回写的内容：逐项的实测结论；第 2、11 项的模式名改为 `direct_header`；第 16 项旧稿“无头 `cmd/server` 一律 401”被实测改正为“标准版返回 403”；第 15 项 Edition 的注入方式；#8 列出的静态补充（第 3、4、5、10、11、13 项）。除模式名的写法和第 16 项那句旧说法外，证据没有推翻本表的其他结论。
- **来源**：iDoris-ai/WeKnora#5；事实台账 iDoris-ai/Agent24#989；任务 ID 见 Agent24 #1000–#1013 和 WS 各 epic。
- 文中路径都相对 WeKnora 仓库根目录。本文不含凭据，也不含任何真实资料。

## 总览

| # | 能力 | 现状（一句话） | 主要缺口 | 任务 ID |
|---|---|---|---|---|
| 1 | scoped API key / 空白名单 | 25 个 capability；路由默认拒绝；白名单为空 = 全租户 KB | 空白名单放大权限；不能表达“空范围拒绝” | KB-CT-02a、KB-C1-03、KB-C1-01 |
| 2 | 外部主体（signed_token / direct_header） | signed_token：HS256、aud、exp≤24h，只取出 sub；direct_header：直接信任请求头 | 不参与 KB ACL；决定会话、记忆、MCP OAuth、沙箱凭据归属；审计 actor 是租户最早的真人（key id 只在 Details）、不记 principal；无 jti、无单主体撤销 | KB-C1-03、KB-CT-02a |
| 3 | hybrid-search | POST/GET `/knowledge-bases/:id/hybrid-search`，retrieve 可用 | 无调用方 deadline、无契约要求的错误分类（非 AppError 一律 500 并带回内部错误原文）、无 revision；handler 记录 query 全文；向量检索开启且调用方没给 query_embedding 时会算一次 | KB-C1-01c、KB-OBS-01、KB-SVC-POLICY-01 |
| 4 | /knowledge/{id} 与 chunks | 元数据和分块正文都能读，分页≤100；有历史修订列表和 index_status | 不能按指定修订读并校验；没有 stale 语义 | KB-C1-01c、KB-CT-02a |
| 5 | 原件出口：/download、batch-download、/files | /download 与 batch-download：JWT 需 Contributor+Editor，本租户 API key 有 retrieve 即可；/files：本租户任何成员拿到 file_path 即可，不经 API-key gate | 机器主体和普通成员取原件的门槛比 /download 低；/files 在 gate 外、按 public 缓存；凭据型出口 /r/:token、presigned 的覆盖面待探测 | KB-C1-01、KB-ORG-02a、KB-OBS-01、#996 |
| 6 | /preview | Viewer（含组织共享、共享 Agent 授予的跨租户 Viewer）可取**原始文件** | 不是“解析后预览”；集成模式应关闭或另定 | KB-C1-01、KB-C2-02、KB-ORG-02a |
| 7 | /system/info | 有版本、commit、引擎信息 | retrieve key 读不到 | KB-C1-01b-a |
| 8 | /system/capabilities | 任意 API key 可读 | 只表示路由是否注册，不表示依赖是否健康；没有 Agent24 集成和协议版本的扩展字段 | KB-C1-01b-a、KB-C1-01 |
| 9 | /health | 写死返回 ok | 没有 /ready，也没有按操作的就绪探针 | KB-C1-01b-b |
| 10 | 租户隔离 | KB 按租户隔离；跨租户经组织共享、共享 Agent、平台 API key、跨租户超管 | 同租户内没有 per-user ACL；平台 retrieve key 可读任意租户 | KB-C1-01、KB-C1-03、KB-C4-02a、#993 |
| 11 | 撤权生效时机 | key、JWT、成员关系都按请求查库；移除成员会吊销其全部 token | signed_token 只能粗粒度失效；在途流式请求不取消；RBAC 关闭时降权不生效 | KB-C1-03、KB-CT-03①、KB-C1-04 |
| 12 | 鉴权前路由 | 有 10 组路由注册在 Auth 之前，另有 noAuthAPI 白名单（含默认开放的注册） | 集成模式无法整体关闭这些路由 | KB-C1-01 |
| 13 | 日志记正文 | 请求体、响应体各最多 10KB 写入访问日志，鉴权前路由也一样；完整 `path?query` 只对少数参数脱敏；启用 Langfuse 时 query 和命中预览进追踪 | 正文、query、HMAC 明文、query 与路径里的 bearer 凭据落日志；Lite 默认 debug 级 | KB-OBS-01 |
| 14 | RBAC 可配成放行 | `WEKNORA_TENANT_ENABLE_RBAC=false` 后只记日志不拒绝，非成员按 Admin 放行 | 任何成员都能管理 key、主体配置并签发主体 token；集成模式必须禁止这样配置 | KB-C1-01、KB-C1-03 |
| 15 | Lite 无头默认监听 | `0.0.0.0:8080`；端口 0 通不过配置校验；默认开放自助注册 | 没有 loopback 默认、端口回报、ready 握手、--version；同网段任何人可注册 | KB-LITE-01a、KB-C1-01b-a |
| 16 | auto-setup 范围 | 仅 Wails 桌面版能用；只建默认用户和空间并签发令牌 | 无头版不能用；不建 KB、不配模型 | KB-LITE-01a、KB-C4-02a/02b |

## 逐项

### 1. scoped API key 与空白名单的语义
- **现状**：key 有 `full_access`、`knowledge_base_ids`、`capabilities` 三个字段（`internal/types/tenant_api_key.go:25-32`），共 25 个 capability（`:78-164`）。
  `/api/v1` 上的 API-key gate 对没有声明策略的路由一律拒绝（`internal/middleware/api_key_gate.go:130-134`）。
  **白名单为空时放行全租户 KB**：`AllowsKnowledgeBase` 遇到空列表直接返回 true（`internal/types/tenant_api_key.go:328-330`），`AllowsKnowledgeBases` 同样如此（`:345-347`）。
  另外，既不要求 full access、又没列 capability 的策略（`apiKeyAny()`）对任何有效 key 都放行（`api_key_gate.go:149-151`；`internal/router/rbac.go:226-228`）。
- **缺口**：“空 = 全部”和契约要求的“空交集即拒绝、默认 deny-all”正好相反。key 范围也没有和用户或权威授权取交集。
- **任务**：KB-CT-02a（#1011 §2 范围语义）、KB-C1-03、KB-C1-01。
- **证据**：`docs/agent24/probe-evidence/4b363d15/01-scoped-key.json`。已测部分一致：空白名单 key 列出并读到全租户 KB；白名单外的 KB 和请求体里越界的 `knowledge_base_ids` 都 403；`apiKeyAny` 路由对任意有效 key 放行；key 管理对 API key（含 full_access）不开放。
  另观察到：建一个既非 full access、又不带任何 capability 的 key 返回 400，API 层没有“空范围”可表达。25 个 capability 的全集仍是静态结论。

### 2. 外部主体：signed_token 与 direct_header（是否影响 ACL 和审计）
- **模式**：租户 `APIPrincipalConfig.Mode` 为空或 `tenant` 时，主体就是租户本身（`internal/middleware/auth.go:575-577`）。
  `direct_header` 模式（常量 `APIPrincipalModeDirect` 的取值，`internal/types/tenant.go:201`）直接读 `X-External-User-ID`，没有任何签名（`auth.go:580-594`），持 key 的人可以自称任意主体；不带这个头时回落到租户主体，除非配置了 `RequireDirectHeader`（`:582-587`）。
- **signed_token**：租户 `APIPrincipalConfig.Mode=signed_token` 时，从 `X-External-User-Token` 取出 JWT 校验（`internal/middleware/auth.go:595-607`）。
  校验函数在 `auth.go:613-659`：要求 HS256、`aud=weknora`、带 exp，剩余有效期≤24h（`:26`、`:644`）；校验 nbf；token 的 tenant_id 必须等于 key 所属租户（`:650`）；最后只返回 `sub`（`:653-658`）。
  得到的主体是 `api_external_user:<tenant>:<sub>`（`:604-607`），而 key 的权限范围仍原样从 key 复制（`auth.go:550-558`）。
- **服务端签发与管理**：`POST /tenants/:id/api-principal-test-token`（`internal/router/routes_auth_tenant.go:103`）按租户 HMAC 给请求里任意的 `external_user_id` 签发 token（`internal/handler/tenant.go:1021-1095`；要求已是 signed_token 模式并存有 HMAC，`:1051-1059`；默认 15 分钟、最长 1 小时，`:176-177`）。
  这个接口和 key、主体配置的管理（`routes_auth_tenant.go:97-103`）都只挂 JWT `g.Owner()`，API key 进不来；RBAC 关闭时它们对任何成员放行（见第 14 项）。
- **ACL**：KB 访问只看 key 范围和调用方的租户与角色，经同租户、组织共享、共享 Agent 三条路径判定（`internal/middleware/kb_access.go:202`；`internal/application/access/knowledgebase.go:104-154`），外部主体不参与。
- **审计**：`audit_logs` 只有 `actor_user_id`（`internal/types/audit_log.go:220`），外部主体不进表。
  租户 API key 请求的 user 取自 `GetUserByTenantID`，即 `users.tenant_id`（用户的 home 指针）指向该租户的用户里 `created_at` 最早的一个，不看成员关系和角色（`auth.go:513`；`internal/application/repository/user.go:101-104`）；查不到时改用合成用户 `system-<tenantID>`（`auth.go:513-524`）。
  这个 user 写进上下文（`internal/middleware/auth_context.go:63-65`）后成为审计 actor（`internal/application/service/tenant_member.go:133-135`、`internal/application/service/kb_activity.go:175-181`）。
  查到真人时，机器 key 的写操作会**记到这个真人名下**，KB 活动审计的 trigger 也记为 `user`（`kb_activity.go:36-44`）；key id 和名称只放在 KB 活动审计的 Details 里（`:46-54`、`:135-142`），成员和角色审计里没有。所有审计都不记 principal。
  平台 key 用合成身份 `api_platform:<id>`，不受影响（`auth.go:504-511`）。
- **主体真正影响的地方**：
  - 会话归属（`internal/types/principal.go:195-199`；`internal/application/service/session.go:42-44`）和记忆作用域（`internal/application/service/memory/scope.go:33-41`）；
  - MCP OAuth 令牌归属（`principal.go:155-163`；`internal/application/service/mcp_metadata.go:15-23`）；
  - 沙箱环境变量与技能凭据（`internal/application/service/user_env.go:81-92`、`user_env_resolver.go:79-80`）；
  - 工具执行的 UserID（`internal/agent/act.go:530-537`）；
  - 用量日志行里的 `principal=`（`internal/models/api/usage_log.go:39-44`）。
- **缺口**：主体不受权威绑定。拿到租户 HMAC（signed_token）、只是持有 key（direct_header），或者是租户 Owner（服务端签发），就能冒充任意 sub，读写它的会话、记忆、MCP 令牌和沙箱凭据。没有 jti 消费和单主体撤销；没有 principal 级审计，key 的操作还可能记到真人名下。配置 HMAC 时它的明文还会落进访问日志（见第 13 项）。
- **任务**：KB-C1-03（依赖 #992）、KB-CT-02a §1 信任边界、§10 审计最小字段。
- **证据**：`docs/agent24/probe-evidence/4b363d15/02-signed-token.json`。已测部分一致：
  - 主体模式写 `direct` 返回 400，要写 `direct_header`（本表已按源码取值改正）；
  - 两种模式下，主体 b 都读不到主体 a 的会话（404），但 direct_header 下持 key 自称 a 就能读；
  - KB 检索结果不受主体影响；API key（含 full_access）不能签发主体 token；服务端签发超过 1 小时返回 400；
  - 自签 token 在 aud 错误、exp 超过 24h、无 exp、tenant_id 不符时都 401；持 HMAC 自签任意 sub 被接受（无 jti）；signed_token 模式下不带 token 的租户 key 请求（含 full_access）一律 401；
  - 审计 actor 是租户 Owner，details 只有 `api_key_id`、`api_key_name`、`name`、`type`，不记外部主体。
  未测：记忆、MCP OAuth、沙箱凭据的归属（需要模型或沙箱）；审计 actor 取“最早注册”还是“Owner”没有分开（本栈里是同一个用户）。

### 3. hybrid-search
- **现状**：POST/GET `/api/v1/knowledge-bases/:id/hybrid-search`，路由要求 Viewer 和 KB 读权限，API key 需 retrieve 或 full access（`internal/router/routes_knowledge.go:200`、`:232-233`）。
  请求体的 `knowledge_base_ids` 可以覆盖路径里的 KB，一次检索多个库（`internal/types/search.go:244-247`；`internal/application/service/knowledgebase_search.go:133-137`）；对每个库都会再做 key 白名单和共享权限校验（`knowledgebase_search_storegroup.go:211-229`），并要求 embedding 模型一致（`knowledgebase_search.go:178`）。
  handler 在 `internal/handler/knowledgebase.go:321-381`，用的是请求上下文（`:359`），所以客户端断开会传导下去。
  调用方没给 query_embedding、KB 开了向量、配置了 embedding 模型、且没有禁用向量匹配时，服务层会算一次 query embedding（`internal/application/service/knowledgebase_search.go:206-213`）。
- **缺口**：
  - 没有调用方 deadline 和预算参数；引擎解析阶段有 12s 内部上限（`internal/application/service/knowledgebase_search_storegroup.go:21`、`:114`）；跨多个 store group 检索时每组另有 30s 超时，可用 `MULTI_STORE_RETRIEVE_TIMEOUT_SEC` 调整，只有一组时没有这层（`internal/application/service/knowledgebase_search_fanout.go:23`、`:56-72`、`:196-206`），多组时，父 ctx 被取消返回原始 `ctx.Err()`（`:95-97`），其余失败（含 `DeadlineExceeded`）收成不带原文的 VectorStoreUnavailable（`:98-104`，HTTP 400，`internal/errors/errors.go:267-275`）；`http.Server` 也没有设超时（`cmd/server/main.go:72-74`）。
  - 有统一错误信封（`internal/middleware/error_handler.go:23-43`），但没有契约要求的错误分类；服务层的非 AppError 一律包成 500（`knowledgebase.go:366-372`），并把内部错误原文 `err.Error()` 返回给客户端（`:371`；`internal/errors/errors.go:144-151`；`internal/middleware/error_handler.go:25-29`）。
  - 取消没有类型化的错误：多组时父 ctx 被取消返回原始 `ctx.Err()`（`knowledgebase_search_fanout.go:95-97`，`isParentCancelled` 只认 `context.Canceled`，`:222-224`）；只有一组时引擎错误原样返回（`:56-57`）；query embedding 阶段在检索之前，取消也原样上抛（`knowledgebase_search.go:206-211`；`internal/models/embedding/ollama.go:77`、`:115`）。这些非 AppError 都被 handler 包成 500 并带出 "context canceled"（`knowledgebase.go:370-371`）。多组超时则是 400、不带原文（见上一条）。
  - `SearchResult.ContentRevision` 不序列化，标的是 `json:"-"`（`internal/types/search.go:220`）。
  - handler 和服务层都用 `logger.Infof` 记录 query 全文（`knowledgebase.go:346-347`；`internal/application/service/knowledgebase_search.go:142-143`），只去掉控制字符，不受访问日志 10KB 上限约束。
  - 相关的 `/knowledge-search` 用 `logger.CloneContext` 脱开了请求 ctx（`internal/handler/session/qa.go:829`；`internal/logger/logger.go:511-512`），参数错误返回 400（`qa.go:834-844`、`:874-878` 的 tag 校验、`:881-884`），key 白名单不允许的 KB 返回类型化的 Forbidden（`:886-889`；`internal/types/tenant_api_key.go:398-401`），服务层错误一律 500 并带回内部错误原文（`:902-905`），也会记录 query（`qa.go:897`）。
- **任务**：KB-C1-01c（PR1：deadline 与错误信封；PR3：修订映射）、KB-OBS-01、KB-SVC-POLICY-01、KB-CT-02a §3、§6（错误分类）。
- **证据**：`docs/agent24/probe-evidence/4b363d15/03-hybrid-search.json`。已测部分一致：retrieve key 可用 POST，GET 带 JSON body 也可用；结果不序列化 `content_revision`；开向量而 embedding 模型不可用的 KB 返回 500，响应带回服务层原文；请求体指向另一租户的 KB 返回 404；访问日志和 handler/服务层日志都记 query 全文。
  取消已实测，走的是 query embedding 阶段（错误原文以 `failed to embed text:` 开头）：客户端 1 秒断开后，服务端日志出现 context canceled，访问日志记为 500，客户端没有收到响应体。未测：超时（多组 400、单组原样）、fanout 里的取消分支、检索质量（伪 embedding）、修订映射（静态结论）。

### 4. /knowledge/{id} 与 chunks 读取
- **现状**：
  - `GET /knowledge/:id`、`/:id/spans`、`/:id/stages` 要求 Viewer 和父 KB 读权限（`internal/router/routes_knowledge.go:105-107`）；handler 分别是 `GetKnowledge`（`internal/handler/knowledge.go:600`）和 `GetKnowledgeSpans`（`:649`，spans 与 stages 共用）。
  - `GET /chunks/:knowledge_id`、`/chunks/by-id/:id`、`/chunks/:knowledge_id/:id/revisions` 走 retrieve（`routes_knowledge.go:31-38`）。分页默认 10、最大 100（`internal/handler/chunk.go:117-125`），默认只返回 text 类型分块（`chunk.go:128`）。
  - Chunk 带 `content_revision` 和 `index_status`（ready/processing/failed）字段（`internal/types/chunk.go:132-135`），Knowledge 带 `parse_status` 和 `file_hash`（`internal/types/knowledge.go:189`、`:218`）。
  - 同属 retrieve 档的其他读接口（示例，非穷举）：读元数据的 `GET /knowledge-bases/:id/knowledge`（`routes_knowledge.go:76`）、`/folders`（`:79`）、`GET /knowledge/batch`（`:104`，KB 校验在 handler 内）、`GET /knowledge/search`（`:123`）、`/move/progress/:task_id`（`:124`）、tags（`:283`）、FAQ 读取（`:157-159`、`:170`）；返回正文的 wiki 页面与搜索（`:304-336`）；返回 `exports/` 下文件字节的 `/knowledge-bases/:id/files`（`internal/router/files.go:336-341`）。集成模式的允许列表要逐一表态。
  - `/revisions` 返回历史修订列表，带历史正文（`internal/handler/chunk.go:257-268`；`types/chunk.go:189`）。
  - `/spans`、`/stages` 把 span 的 `input`、`output` 序列化返回（`internal/types/knowledge_span.go:92-93`；`internal/handler/knowledge.go:855`、`:718`）。启用了图谱抽取、多模态、摘要、wiki 摄取这些阶段时，output 里带截断的正文预览：`chunk_preview`（`internal/application/service/extract.go:309`）、`ocr_preview` 与 `caption_preview`（`internal/application/service/image_multimodal.go:292`、`:308`），各 200 字；`summary_preview`（`internal/application/service/knowledge_process.go:1482`，240 字）；wiki 摄取：`body_preview`、`content_preview`（各 320 字）和 `page_summary`（200 字）（`internal/application/service/wiki_ingest_batch.go:1366-1369`、`:1809-1810`）。所以它不是“不含正文”的接口。
- **缺口**：有历史修订列表，但不能按指定修订读并校验“检索时的修订”；没有 stale 返回；源修订、索引构建、chunk 修订三者之间没有映射。
  单条 chunk 找不到时返回 404（`chunk.go:69-72`），列表的参数错误返回 400（`:104-107`、`:112-115`）；其余服务层错误一律 500 并带回内部错误原文（`:74-75`、`:139-141`）。
- **任务**：KB-C1-01c（PR2：正文读取与 stale；PR3：修订映射）、KB-CT-02a §4–§5。
- **证据**：`docs/agent24/probe-evidence/4b363d15/04-knowledge-chunk-read.json`。已测部分一致：retrieve key 能读元数据、`/spans`、`/stages`、chunk 列表（page_size 上限 100）和 revisions；revisions 带回历史正文（改写分块后 revision 0 是旧内容）；分页参数错误 400，chunk 不存在 404，白名单外 KB 403。
  本次 txt 文档的 `/spans`、`/stages` 响应里没有正文哨兵；证据没有记录增强阶段的配置；摘要阶段默认会跑，但没有摘要模型时跳过、不写预览（`internal/application/service/knowledge_process.go:1290-1294`），带预览的情形没有测到。未测：服务层 500 原文路径（需制造存储故障）；按修订读与 stale（静态结论）。

### 5. /download 与 batch-download
- **现状**：`GET /knowledge/:id/download`，JWT 需要 Contributor，并对父 KB 有写（Editor）权限（`internal/router/routes_knowledge.go:120`）；handler 里再查一次 Editor（`internal/handler/knowledge.go:1615-1622`）。返回原件，带 `no-store`（`:1634-1636`）。
  角色守卫对 API key 直接放行（路由注释 `routes_knowledge.go:114-119`；`internal/middleware/rbac.go:75-78`），但 KB 写权限检查仍在（`:120`；`knowledge.go:1618`）。
  本租户 KB 按 Admin 授予（`internal/application/access/knowledgebase.go:117-118`），所以**本租户 key 有 retrieve 就能下载**。KB 白名单仍然生效（`kb_access.go:202`）。
  跨租户共享时，非 full_access key 的角色上下文是 Viewer（`auth.go:536-543`），编辑共享会被 `applyTenantRoleCap` 压成 Viewer（`internal/application/service/kbshare.go:69-73`、`:505-507`），所以下载不了。full_access key（Owner）不受这层压缩，但还要满足：源租户仍在组织里，调用方租户在组织里的成员角色不低于 Editor（`kbshare.go:496-507`，取 `MinOrgRole(共享权限, 成员角色)`）。
  平台 retrieve key 用 X-Tenant-ID 进入目标租户后也算本租户（见第 10 项）。
- **第二个原件出口**：`POST /api/v1/knowledge-bases/:id/knowledge/batch-download`（retrieve 档的 `kbRead` 分组在 `routes_knowledge.go:71`，路由在 `:78`）。边界与单文件下载相同，handler 要求 Editor（`internal/handler/knowledge_download.go:68-70`），返回多个原件的 zip。
- **第三个原件出口**：`GET /files?file_path=`（`internal/router/files.go:251-255`）挂在全局 Auth 之后，但不在 `/api/v1` 的 API-key gate 里（`internal/router/router.go:208`、`:211`）。
  它自己的守卫对 JWT 直接放行；API key 只要是 full_access，或者白名单为空且有 retrieve，也放行（`internal/middleware/api_key_gate.go:186-203`）。handler 只校验租户：knowledge 原件的 `file_path` 是 `resource://…`，按 catalog 记录的租户比对（`files.go:110-121`）；不在 catalog 里的路径才校验路径里的租户段（`:193-200`；`internal/utils/presign.go:119-127`）。不查角色和 KB 授权，响应头是 `public, max-age=86400`（`files.go:220`）。
  原件的 `file_path` 可以从只要 Viewer 的 `GET /knowledge/:id` 响应里拿到（`internal/types/knowledge.go:220`），所以**本租户任何成员**都能经 /files 取原件。跨租户共享走不通。
- **凭据型出口（盘点）**：`/r/:token`（`internal/router/files.go:258-313`，路由在 `:312-313`，在 Auth 之前注册，`internal/router/router.go:195`）和 `/api/v1/files/presigned`（`files.go:534-538`，HMAC 签名、免鉴权，`router.go:214`）。它们能否覆盖 knowledge 原件，取决于签发点。
  实测（见下面的证据）只确认了：无效 token 的 `/r/` 返回 404，不带签名的 presigned 返回 400；本栈没设 `APP_EXTERNAL_URL`，没有签发点，`resource_access_grants` 为 0 行。所以覆盖面仍待在设了 `APP_EXTERNAL_URL` 的栈上探测。
- **缺口**：机器主体和普通成员取原件的门槛（retrieve 或 Viewer）都低于 /download（Contributor+Editor）。谁能下载原件还没定（#996）。集成模式的允许列表要对三个出口和两个凭据型出口都明确开或关，而且要覆盖 gate 之外的 /files；/files 的 public 缓存头归 KB-C1-01 处理，KB-OBS-01 只在“文本原件落日志”这一点上相关。
- **任务**：KB-C1-01（含 /files 缓存头）、KB-ORG-02a（预览/下载 ACL 负例）、KB-OBS-01、#996。
- **证据**：`docs/agent24/probe-evidence/4b363d15/05-download.json`。已测部分一致：本租户 retrieve key 能下载原件（带 `private, no-store`），白名单外 403；JWT Viewer 下载 403、Contributor 200；batch-download 同样对 retrieve key 开放；`/files` 对空白名单 retrieve key 和本租户 Viewer 都返回原件，缓存头 `public, max-age=86400`，非空白名单 key、无 retrieve 的 key、另一租户都 403。
  未测：组织跨租户共享；凭据型出口能否覆盖原件（见上）。

### 6. /preview
- **现状**：`GET /knowledge/:id/preview` 要求 Viewer 和读权限，retrieve key 也可用（`routes_knowledge.go:121`）。handler 返回的是**原始文件**（`internal/handler/knowledge.go:1653-1679`）：一般类型内联下发；svg、html、xml、js、css 等主动内容强制为 `application/octet-stream` 附件并带 nosniff（`internal/filetransport/response.go:31-43`；`internal/utils/fileutil.go:9-13`、`:73-84`）。
- **缺口**：preview 实际上等于“Viewer 可取原件”，包括组织共享授予的 Viewer，以及经共享 Agent 可见该 KB 的其他租户（`access/knowledgebase.go:127-154`），绕过了 /download 更严的边界（`routes_knowledge.go:114-117` 的注释明确说 download 不允许这样）。集成模式应当关闭，或者限定为解析后的内容。
- **任务**：KB-C1-01、KB-C2-02（嵌入页首版只读）、KB-ORG-02a、#996。
- **证据**：`docs/agent24/probe-evidence/4b363d15/06-preview.json`。已测部分一致：retrieve key 经 preview 拿到原件字节（核对了哨兵）；JWT Viewer 的 preview 返回 200（只核对了状态码），而 Viewer 下载被拒；html 强制 `application/octet-stream`、带 nosniff、以附件下发。未测：组织共享和共享 Agent 授予的跨租户 Viewer。

### 7. /system/info
- **现状**：`GET /api/v1/system/info` 要求 Viewer（`internal/router/routes_auth_tenant.go:255`），返回 Version、Edition、CommitID、BuildTime、引擎和 DB 迁移版本（`internal/handler/system.go:319-380`）。
  它所在的路由组对 API key 默认要求 manage_vector_stores 或 full access（`routes_auth_tenant.go:252`）。
- **缺口**：只有 retrieve 的 key 读不到版本；也没有协议版本字段。
- **任务**：KB-C1-01b-a。
- **证据**：`docs/agent24/probe-evidence/4b363d15/07-system-info.json`。与本表一致：retrieve key 403，manage_vector_stores key 和 JWT Viewer 200；字段齐全，没有协议版本字段。

### 8. /system/capabilities
- **现状**：任意有效 API key 和 Viewer 都能读（`routes_auth_tenant.go:254`）。13 个键（`internal/handler/deployment_capabilities.go:13-27`），是启动时的快照，来源包括 handler 是否注入（`internal/router/deployment_capabilities.go:8-29`，在 `internal/router/router.go:309` 绑定）、版本（Lite 下 organizations，`internal/handler/deployment_capabilities.go:70-74`）和平台（sandbox.host，`internal/handler/deployment_capabilities.go:85-93`）；只有 sandbox.docker 是实时值（`internal/handler/deployment_capabilities.go:138-154`）。
- **缺口**：capabilities 表示“路由已注册”，不表示依赖健康或业务就绪。已有的 `integrations.im/embed/api/mcpserver`（`internal/handler/deployment_capabilities.go:16-19`）是上游自己的集成开关，没有 Agent24 集成、knowledge 协议版本这类扩展字段；集成模式下也不会如实反映已禁用的路由。
- **任务**：KB-C1-01b-a、KB-C1-01（如实声明）、KB-CT-02a §9。
- **证据**：`docs/agent24/probe-evidence/4b363d15/08-system-capabilities.json`。已测部分一致：无 retrieve 的 key 也能读，共 13 个键。“快照而非健康”仍是静态结论，没做依赖故障注入。

### 9. /health
- **现状**：注册在 Auth 之前，写死返回 `{"status":"ok"}`（`internal/router/router.go:145-147`），并在 noAuthAPI 白名单里（`internal/middleware/auth.go:37`）。
- **缺口**：不检查 DB、存储、向量库、模型；没有 /ready；没有按操作（search/read/ingest）的就绪探针。
- **任务**：KB-C1-01b-b、KB-CT-02a §8。
- **证据**：`docs/agent24/probe-evidence/4b363d15/09-health.json`。已测部分一致：`/health` 无鉴权返回 `ok`；带凭据访问 `/ready` 返回 404（无凭据时先被全局 Auth 拦成 401）。未测：依赖故障时是否仍返回 ok。

### 10. 租户隔离
- **现状**：
  - 普通 key 不能用 X-Tenant-ID 切换租户（`internal/middleware/auth.go:416-429`）；JWT 切换要经过 `IsTenantAccessible` 检查（`:314-321`）。
  - KB 判定：同租户直接授予 Admin 级访问（`internal/application/access/knowledgebase.go:117-118`）；跨租户经组织共享（`:120-126`）或共享 Agent（`:127-154`）。
  - **平台 API key**（SystemAdmin 创建，设计上“可经 X-Tenant-ID 指向任意空间”，`internal/handler/system.go:132`）：用 X-Tenant-ID 选租户，只查租户存在、不查成员关系（`internal/middleware/auth.go:388-408`）。
    它不能带 full_access，但可以带 retrieve（`internal/application/service/tenant_api_key.go:41-47`）；创建请求里没有 KB 白名单字段（`system.go:101-105`），所以白名单为空，即全放行。
    进入目标租户后按同租户 Admin 授予（`access/knowledgebase.go:117-118`），角色守卫对 API key 放行（`rbac.go:75-78`），所以带 retrieve 的平台 key 应能对任意租户做 hybrid-search、读 chunk、preview 和 download。
    同理 `/files` 和 batch-download 也可用：平台 key 白名单为空且有 retrieve，过得了 `/files` 的 key 守卫（`internal/middleware/api_key_gate.go:193-197`），`/files` 又按进入后的目标租户比对 catalog 记录的租户（`internal/router/files.go:118-121`）；batch-download 在 retrieve 档（`routes_knowledge.go:71`、`:78`）。
    测试 `internal/middleware/auth_platform_api_key_test.go:63-95` 只证明上下文挂到了目标租户；后面的接口链路都是静态推理，PR② 没能实测（见下面的证据）。
  - **跨租户超管**：`EnableCrossTenantAccess` 加 `CanAccessAllTenants` 的 JWT 用户可以进入任意租户（`internal/middleware/access.go:90-92`；`auth.go:788-793`），默认关闭（`config/config.yaml:116`），也可以用环境变量 `WEKNORA_TENANT_ENABLE_CROSS_TENANT_ACCESS=true` 打开（`internal/config/config.go:893-895`）。
- **缺口**：同租户内没有 per-user ACL（任何成员、任何同租户 key 都按 KB 白名单看到全部）；集成模式还没有禁止消费既存的共享授权；平台 key 不受 KB 白名单约束，绝不能进入 Desktop 或用于知识读取。
- **任务**：KB-C1-01（禁止消费既存共享、允许列表）、KB-C1-03、KB-C4-02a（平台 key 不进 Desktop、不扩权）、#993。
- **证据**：`docs/agent24/probe-evidence/4b363d15/10-tenant-isolation.json`。已测部分一致：租户 key 不能用 X-Tenant-ID 切租户；跨租户 hybrid-search、读 knowledge、full key 下载都 403；JWT 切到非成员租户 403；同租户 Viewer 看到全部 KB。
  未测：平台 key 跨租户读取。提升 SystemAdmin 的接口只允许 SystemAdmin 调用（`internal/router/routes_auth_tenant.go:289-292`），本栈 SystemAdmin 为 0，没法从零自举；启动引导只提升已注册的邮箱（`cmd/server/bootstrap.go:72-78`），要先注册用户、再设 `WEKNORA_BOOTSTRAP_SYSTEM_ADMIN_EMAIL` 并重启，本次不改环境。组织共享与跨租户超管也未测。

### 11. 撤权生效时机
- **现状**：
  - API key 每次请求都按 hash 查库，并检查 `revoked_at` 和 `expires_at`（`internal/application/service/tenant_api_key.go:91-100`；`internal/application/repository/tenant_api_key.go:27-31`），没有缓存。
  - JWT 每次请求查 token 记录的 IsRevoked（`internal/application/service/user.go:1252-1253`）。
  - 成员关系每次请求查询（`internal/middleware/auth.go:761-767`）。移除成员时 `cleanupRemovedMemberState` 会清掉指向该租户的 TenantID/LastActiveTenantID，并吊销该用户的全部 token（`internal/application/service/tenant_member.go:400-467`）；清理是 best-effort，失败只打 warn（`:463-466`）。
  - 所以下一个请求就会生效。Auth 只在请求进入时执行一次（`internal/router/router.go:208`），之后不再复查。
- **缺口**：
  - signed_token 没有 jti，不能撤销单个主体。只能粗粒度地让它失效：撤销或过期这把 key（`tenant_api_key.go:95-100`），或者轮换租户 HMAC。
    把 Mode 改成 `tenant` 或空值时，外部主体失效，但请求仍以租户主体成功（`auth.go:575-577`）；改成 `direct_header` 反而放宽成可自称任意主体（`:580-594`）。所以改 Mode 不能算撤权手段。
  - 已经开始的流式或长请求不会因撤权而中止：鉴权只在请求进入时做一次（`router.go:208`）。另外，`/knowledge-search` 用 `CloneContext` 脱开了请求 ctx（`internal/handler/session/qa.go:829`；`internal/logger/logger.go:511-512`），客户端断开也不会中止它，这一点与撤权无关。
  - RBAC 关闭时，降权不生效：`UpdateRole` 不吊销 token（`tenant_member.go:349-352`），`RequireRole` 用较低角色也只记日志、照常放行（`internal/middleware/rbac.go:89-95`）。成员查询出错，或者移除成员后清理失败、token 没被吊销时，也会落到兜底分支并按 Admin 放行（`auth.go:768-772` → `:824-828`）。见第 14 项。
- **任务**：KB-C1-03（撤销、jti）、KB-CT-03①（撤权状态机、generation）、KB-C1-04、KB-CT-02a §7（取消语义）。
- **证据**：`docs/agent24/probe-evidence/4b363d15/11-revocation.json`。已测部分一致：撤销 key 后下一请求 401；RBAC 开启时降为 Viewer 后下一请求 403；移除成员后访问原租户 401，且该用户的全部 token 被吊销（自己的租户也 401）；轮换 HMAC 后旧 signed token 401；改回 tenant 模式后带旧 token 仍以租户主体成功。
  未测：在途流式请求不取消；RBAC 关闭时的降权与兜底（要改 app 环境变量）。

### 12. 注册在鉴权之前的路由
- **现状**：全局 Auth 在 `router.go:208` 才挂上。在它之前注册的有：
  - `/health`（`:145`）；
  - 非 release 模式下的 `/swagger/*any`（`:151-158`；`.env.lite.example:5` 默认 `GIN_MODE=debug`）；
  - Lite 前端静态文件（`:170-172`）；
  - IM 回调（`:175`）、embed 公开路由（`:178-187`）、`/mcp/:endpoint_id`（`:191`）、资源 grant（`:195`）、sandbox terminal 和 desktop WS（`:201-202`）、local-browser 三条（`:203-205`）。
  - 此外 noAuthAPI 白名单（`internal/middleware/auth.go:36-63`）还放行 register、login、auto-setup、refresh、OIDC、presigned 等。
  - 注册默认开放：注册模式默认 `self_serve`（`internal/config/config.go:859-860`），只有 `invite_only` 才拒绝（`internal/handler/auth.go:185-195`，数据库里的系统设置优先）；新用户默认建个人空间（`config.go:872-874`）；`.env.lite.example:49` 是 `DISABLE_REGISTRATION=false`。
- **缺口**：这些路由不经过 API-key gate，也没有集成模式总开关，无法按允许列表关闭。
- **任务**：KB-C1-01。
- **证据**：`docs/agent24/probe-evidence/4b363d15/12-pre-auth-routes.json`。已测部分一致：`/health`、`/api/v1/auth/config`、`/r/…`、`/api/v1/local-browser/extension` 都不经全局 Auth；`/mcp/:endpoint_id` 有自己的 bearer 鉴权；release 模式不注册 swagger；自助注册默认开放，注册即建个人空间。未测：IM 回调、embed、sandbox WS（需要对应渠道配置）。

### 13. 日志是否记录正文
- **现状**：
  - Logger 中间件挂在所有路由之前（`internal/router/router.go:140`），所以鉴权前路由（登录、注册、IM、embed、MCP 回调等）也一样记录。
  - 对 POST/PUT/PATCH 读取 json、form、text 类请求体（`internal/middleware/logger.go:96-102`、`:179-182`），截断到 10KB（`:20`、`:114-124`）；JSON 和 text 类响应体也捕获，同样截断（`:221-233`）。两者以 `request_body`、`response_body` 写入（`:252-258`）。
  - 跳过的只有：`/assets/` 和 wiki stats 整条不记（`:168-172`），local-browser 流量（`/api/v1/local-browser/` 前缀、`/api/v1/me/browser`、`*/local-browser` 后缀）请求体和响应体都不记（`:176-177`、`:180`、`:190-192`），SSE 响应体不记（`:223-224`）。
  - 脱敏只覆盖 password、token、key 这类字段名（`:45-56`）。所以 chunk 正文（GET 响应）、search query（POST 请求体）、经 preview 下发的 text 类原件，都会落日志。
  - 脱敏正则要求整个字段名等于列表中的某一项（两侧都有引号，`:45-50`），所以 `PUT /tenants/:id/api-principal-config` 请求体里的 `hmac_secret`（`internal/handler/tenant.go:112`；`internal/router/routes_auth_tenant.go:102`）匹配不上：**租户 HMAC 明文会进访问日志的 `request_body`**。这正是第 2 项里“持有就能冒充任意主体”的那个根秘密。
  - 访问日志把完整的 `path?query` 写进去（`internal/middleware/logger.go:215-217`），query 只对 `access_token`、`code`、`id_token`、`refresh_token`、`state`、`ticket`、`token` 等少数参数脱敏（`:58-71`）。所以整条 presigned URL（`sig` 与 `file_path`、`tenant_id`、`expires` 绑定，`internal/utils/presign.go:47-48`、`internal/router/files.go:582`）、`/r/:token` 路径里的令牌、`GET /knowledge/search` 的关键词都以明文落日志；前两者是有时效、可重放的 bearer 凭据。
  - 另外 handler 和服务层还单独记录 query 全文（`internal/handler/knowledgebase.go:346`、`internal/handler/session/qa.go:897`、`internal/application/service/knowledgebase_search.go:142-143`）。
  - `LANGFUSE_ENABLED` 为空且配置了 Langfuse 凭据时自动启用追踪，该变量非空时以它为准（`internal/tracing/langfuse/config.go:79-84`）：retrieve span 的输入带 `query_text`（`internal/application/service/knowledgebase_search.go:233-236`），命中带 160 字正文预览（`internal/tracing/langfuse/retrieval_obs.go:163`、`:258`）。默认不启用，但它是 query 和正文外流的另一个出口。
- **缺口**：没有关闭正文记录的开关。访问日志按状态码分级：5xx 用 Error、4xx 用 Warn、其余用 Info（`logger.go:263-270`）。`LOG_LEVEL=warn` 时 2xx 不输出，但 4xx/5xx 仍带正文；`LOG_LEVEL=error` 时只剩 5xx。`LOG_LEVEL` 未设置或无效时就是 debug（`internal/logger/logger.go:337-338`），Lite 的示例配置也是 debug（`.env.lite.example:8`）。
- **任务**：KB-OBS-01（哨兵秘密负例，须覆盖 `hmac_secret`、presigned `sig`、`/r/:token` 路径令牌和 query 关键词）、KB-C1-03（HMAC 的配置与轮换不得经过记录正文的路径）。
- **证据**：`docs/agent24/probe-evidence/4b363d15/13-log-body.txt`（只摘录含哨兵的片段）。已测部分一致：query 进访问日志的 `request_body`；preview 下发的 text 原件、hybrid-search 命中的正文、`GET /chunks` 的正文都进 `response_body`；`hmac_secret` 明文进 `request_body`；登录行的 password 被打码，口令明文不出现。容器 `LOG_LEVEL` 为 debug。
  未测：Langfuse（本栈配了凭据，但 `LANGFUSE_ENABLED=false` 显式关闭）、10KB 截断、local-browser 和 SSE 不记录、`LOG_LEVEL=warn/error` 的分级、`path?query` 里的凭据（本栈没有签发点）、`GET /knowledge/search` 的关键词进日志（没有放哨兵）。

### 14. RBAC 能否配成放行
- **现状**：默认强制执行（`internal/config/config.go:880-885`），但 `WEKNORA_TENANT_ENABLE_RBAC` 设为非 true 的值、或 yaml 写 `enable_rbac: false`，都会关闭（`config.go:876-879`、`:211-220`）。
  关闭后：
  - `RequireRole` 只记日志、照常放行（`internal/middleware/rbac.go:89-95`）；
  - 没有成员关系的 JWT 用户直接按 Admin 放行（`internal/middleware/auth.go:817-828`）；
  - 只挂 `g.Owner()` 的 key 管理、主体配置和主体 token 签发（`internal/router/routes_auth_tenant.go:97-103`）对本租户任何成员放行：可以新建 full_access key、改 principal mode 或 HMAC、为任意外部主体签发 token（见第 2 项）。
- **缺口**：集成模式必须拒绝这项配置，或在启动时失败。
- **任务**：KB-C1-01（RBAC 不可放行）。
- **证据**：`docs/agent24/probe-evidence/4b363d15/14-rbac-off.json`。整项未测：要把 `WEKNORA_TENANT_ENABLE_RBAC` 设为 false 并重启 app，本次不改环境（容器里该变量为空，即默认开启）。

### 15. Lite 无头模式的默认监听地址
- **现状**：
  - 无头 Lite 是以 `make build-lite` 构建的 `cmd/server`（`Makefile:77`、`:288-304`）：Edition 在编译时经 ldflags 注入（`scripts/get_version.sh:71`），运行时设置 `EDITION` 环境变量无效。监听地址取 `cfg.Server.Host:Port`（`cmd/server/main.go:76-77`），默认 `0.0.0.0:8080`（`config/config.yaml:3-4`）。打包脚本会把 `config/` 原样带上（`scripts/package-lite.sh:77-78`）。
  - 端口必须在 1–65535，`0` 通不过配置校验（`internal/config/config.go:686`）。
  - 用配置的 addr 直接 `net.Listen`（`cmd/server/listen.go:14`），端口固定，所以打印的地址就是实际地址（`cmd/server/main.go:132`）；但没有机器可读的 ready 行，也没有 `--version`。读 `listener.Addr()` 回报端口的只有桌面版（`cmd/desktop/main.go:242-243`）。
  - 自助注册默认开放（见第 12 项）。叠加 `0.0.0.0` 监听，同一网段里能访问这个端口的人都能注册并拿到个人空间和 JWT。
  - `JWT_SECRET` 为空或是占位值时，每次启动随机生成（`internal/application/service/user.go:81-94`），重启后旧 token 全部失效。
  - 对照：Wails 桌面版默认 `127.0.0.1:0`（`cmd/desktop/main.go:447-456`）。
- **缺口**：没有 loopback 默认、`:0` 端口回报、受控握手、密钥持久化；托管模式下没有关闭注册的默认值。
- **任务**：KB-LITE-01a（loopback、端口回报、握手、密钥持久化）、KB-C1-01b-a（`--version`）、KB-C1-01（注册入口）。
- **证据**：`docs/agent24/probe-evidence/4b363d15/15-lite-listen.json`（标准栈，不能替代 Lite）。已测部分一致：容器内 `config.yaml` 默认监听 `0.0.0.0:8080`，`/proc/net/tcp` 里确实监听通配地址；宿主侧只发布到 `127.0.0.1:8080`（受测栈约束）。未测：无头 Lite 二进制、端口 0、`SERVER_HOST` 覆盖、`--version`。

### 16. auto-setup 的范围
- **现状**：
  - `POST /api/v1/auth/auto-setup`（`internal/router/routes_auth_tenant.go:217`）在 noAuthAPI 白名单里（`internal/middleware/auth.go:40`）。
  - handler 要求 `Edition=lite`，标准版一律返回 403（`internal/handler/auth.go:907-910`）；Lite 还要求请求头 `X-WeKnora-Desktop-Token` 和进程内 token 恒时比较相等，否则 401（`:913-916`）。这个 token 只有 Wails 桌面版设置（`cmd/desktop/main.go:217`；setter 在 `internal/handler/auth.go:30`），所以无头 Lite 调用一律 401。
  - 默认用户不存在时，用固定的默认邮箱常量和随机口令注册（空间由 Register 流程建立）；已存在时不再注册。两种情况最后都签发 access/refresh token（`auth.go:919-976`，判断在 `:921-922`，签发在 `:953`），所以重复调用就会重复签发。
- **缺口**：不建默认 KB，不配置模型和配置档；无头托管没有可用的引导入口；引导凭据和日常凭据没有分开。
- **任务**：KB-LITE-01a（握手通道）、KB-C4-02a（可信 bootstrap）、KB-C4-02b（幂等建库）。
- **证据**：`docs/agent24/probe-evidence/4b363d15/16-auto-setup.json`（标准栈）。与本表一致：标准版返回 403，带任意桌面 token 仍 403。（证据脚本按源码预期 403，所以记为一致；本表旧稿“无头 `cmd/server` 一律 401”据此改正。）未测：无头 Lite 返回 401、桌面版的签发行为。

## 与 iDoris-ai/Agent24#989 的差异（待回写）

1. **signed_token 行号**：#989 写的是 `auth.go:551-658`。实际 signed_token 分支在 `auth.go:595-607`，校验函数在 `:613-659`；551-558 是 key 范围的赋值。
2. **“signed_token 不影响……”说得太绝对**：ACL 确实不受影响，但主体会决定会话、记忆、MCP OAuth 令牌、沙箱凭据和工具执行的归属；租户 key 的审计还会记到最早的真人名下。另有 `direct_header` 模式完全不签名（见第 2 项）。
3. **“:0 不回报端口”**：在无头 `cmd/server` 上，端口 0 根本通不过配置校验（`config.go:686`）；`:0` 只出现在桌面版（`cmd/desktop/main.go:456`）。
4. **日志**：#989 写了“记录请求/响应正文”，但行号只指向请求体（`logger.go:180/253`）。应补上响应体的位置（`logger.go:221-233`、`:258`），以及服务层（`knowledgebase_search.go:142-143`）和 `/knowledge-search`（`qa.go:897`）也记录 query。
5. **/system/info 列在“可复用”**：retrieve key 读不到（`routes_auth_tenant.go:252`、`:255`），需要 manage_vector_stores 或 full access。
6. **/download、/preview 列在“可复用”**：没写出三点。一是本租户 API key 只要 retrieve 就能下载原件（`routes_knowledge.go:114-120`）；二是还有 batch-download 这个出口（`:78`）；三是 preview 返回的是原件，跨租户共享的 Viewer 也能拿到（`knowledge.go:1653-1679`）。
7. **“RBAC 可配置为放行 rbac.go:89”**：漏了更严重的一条。RBAC 关闭时，没有成员关系的用户在已解析到的租户上会被授予 Admin（`auth.go:817-828`）；正常移除成员会吊销 token，所以主要缺口是降权不生效，以及成员查询出错时兜底给 Admin；只有移除后的清理失败时，被移除的成员才可能继续访问。
8. **“撤权下一请求生效”**：对 API key、JWT、成员关系成立；对 signed_token 只能粗粒度失效（撤 key、轮换 HMAC），不能撤单个主体。在途请求也不取消。
9. **鉴权前路由**：#989 只引用 `router.go:174-205`。还应加上 `/health`（`:145`）、非 release 模式的 swagger（`:151`）、Lite 静态文件（`:170`）和 noAuthAPI 白名单（`auth.go:36-63`）。
10. **“v0.8.2 + 2 文档提交”**：这两个提交还新增了 `.github/workflows/agent24-upstream-sync.yml`，不全是文档；Go 源码确实未变。
11. **“/knowledge-search 错误一律 500”**：参数错误返回 400（`qa.go:834-844`、`:874-884`），只有服务层错误返回 500 并带回原文（`:902-905`）。
12. **#989 没提到的**：平台 API key 可经 X-Tenant-ID 读任意租户（第 10 项）、默认开放自助注册（第 12、15 项）、500 响应带回内部错误原文（第 3、4 项）、启用 Langfuse 时 query 和正文进追踪（第 13 项）、`/files` 这个 gate 外的原件出口（第 5 项）、服务端主体 token 签发和 RBAC 关闭后的管理放大面（第 2、14 项）。
13. **主体模式名**：源码取值是 `direct_header`（`internal/types/tenant.go:201`），写 `direct` 会被拒（400，PR② 第 2 项实测）。#989 和本表旧稿都写作 direct。
14. **#8 补充的静态结论**（PR② 没有反证）：访问日志写入完整 `path?query`，presigned `sig`、`/r/:token` 等凭据会落日志（第 13 项）；`/r/:token` 和 presigned 两个凭据型出口（第 5 项）；`/spans`、`/stages` 在启用增强阶段时带截断的正文预览（第 4 项）；平台 key 也能经 `/files` 和 batch-download 取原件（第 10 项）；鉴权只在请求进入时做一次，在途请求不因撤权中止（第 11 项）；取消没有类型化的错误，多组超时是不带原文的 400（第 3 项）。另外，embedding 阶段的取消已实测记为 500。
15. **实测结论**：除模式名的写法外，只有第 16 项旧稿“无头 `cmd/server` 一律 401”被推翻，标准版实测返回 403，已改正；其余结论没有被证据推翻。第 14 项整项、第 10 项平台 key 跨租户读取、第 5 项凭据型出口的覆盖面、无头 Lite（第 15、16 项）、第 11 项的在途流式请求与 RBAC 关闭时的降权仍未测。

#989 里还有几条不在本表 16 项之内，本 PR 不覆盖，留给对应任务：主 SPA 不支持嵌入（`internal/router/static.go:53` 的 `frame-ancestors 'self'`、前端 token 存在 localStorage）、鉴权后路由对 JWT 没有集成模式总开关（例如 `internal/router/routes_agent.go:24`、`routes_chat.go:51`）、`WEKNORA_SANDBOX_MODE` 是死配置、`release-lite.yml` 不按 tag 检出。

## 未核实

- PR② 证据覆盖了各项的主路径，各项“证据”一行列出了没测到的部分。最需要补测的仍是第 10 项平台 key 跨租户读取、第 11 项和第 14 项 RBAC 关闭时的降权与兜底、第 5 项凭据型出口的覆盖面，以及无头 Lite（第 15、16 项）。
- `SERVER_HOST`、`SERVER_PORT` 环境变量能否通过 viper AutomaticEnv 覆盖 `config.yaml` 的 host 和 port（`internal/config/config.go:511-512`），没有逐行追到 viper 内部，也没有实测。
- auto-setup 中 Register 流程具体建了哪些空间和默认资源，没有逐行追到 `userService.Register` 的内部。
- 无头 Lite 在 `GIN_MODE=release` 下的行为已能静态确认：swagger 只在非 release 模式注册（`internal/router/router.go:151-152`；`cmd/server/main.go:44-46`），日志级别只看 `LOG_LEVEL`（`internal/logger/logger.go:337-338`）。没有实测。
