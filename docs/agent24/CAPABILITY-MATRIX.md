# WeKnora 能力矩阵（KB-CT-01 PR①）

- **受测 SHA**：`4b363d153122797c857871ce7e4bb63626efc637`（`a24/develop`，简写 `4b363d15`）。
- **与上游的关系**：上游 `v0.8.2` = `3e8b0bfc80b845b2d4b2ed683994748741450a97`；受测 SHA = v0.8.2 + 2 个 Agent24 提交（`9d4e79f`、`4b363d1`）。
  这两个提交只新增 `docs/agent24/*` 和 `.github/workflows/agent24-upstream-sync.yml`，Go 源码、配置和前端与 v0.8.2 逐字节相同（`git diff --stat 3e8b0bf 4b363d1`）。
- **文档性质**：静态源码阅读得出的**探测依据**，不是运行证据。每条 `file:line` 都已在受测 SHA 上打开文件核对；“证据文件”一列是 PR② 探测脚本将来输出的位置，目前都**待 PR② 产出**。
  运行结论以证据文件为准；证据与本表冲突时，回写本表和 iDoris-ai/Agent24#989。
- **来源**：iDoris-ai/WeKnora#5；事实台账 iDoris-ai/Agent24#989；任务 ID 见 Agent24 #1000–#1013 和 WS 各 epic。
- 文中路径都相对 WeKnora 仓库根目录。本文不含凭据，也不含任何真实资料。

## 总览

| # | 能力 | 现状（一句话） | 主要缺口 | 任务 ID |
|---|---|---|---|---|
| 1 | scoped API key / 空白名单 | 25 个 capability；路由默认拒绝；白名单为空 = 全租户 KB | 空白名单放大权限；不能表达“空范围拒绝” | KB-CT-02a、KB-C1-03、KB-C1-01 |
| 2 | signed_token 主体 | HS256、aud、exp≤24h；只取出 sub | 不参与 KB ACL，不进 audit_logs；无 jti、无撤销 | KB-C1-03、KB-CT-02a |
| 3 | hybrid-search | POST/GET `/knowledge-bases/:id/hybrid-search`，retrieve 可用 | 无 deadline、无错误信封、无 revision；会记录 query；每次都算 embedding | KB-C1-01c、KB-OBS-01、KB-SVC-POLICY-01 |
| 4 | /knowledge/{id} 与 chunks | 元数据和分块正文都能读，分页≤100 | 不能按修订读；没有 stale 语义 | KB-C1-01c、KB-CT-02a |
| 5 | /download | JWT 需 Contributor+Editor；API key 有 retrieve 即可 | 机器主体下载原件的边界比人宽 | KB-C1-01、#996 |
| 6 | /preview | Viewer 可取**原始文件**（内联） | 不是“解析后预览”；集成模式应关闭或另定 | KB-C1-01、KB-C2-02 |
| 7 | /system/info | 有版本、commit、引擎信息 | retrieve key 读不到 | KB-C1-01b-a |
| 8 | /system/capabilities | 任意 API key 可读 | 只表示路由是否注册，不表示依赖是否健康；没有 integration 字段 | KB-C1-01b-a、KB-C1-01 |
| 9 | /health | 写死返回 ok | 没有 /ready，也没有按操作的就绪探针 | KB-C1-01b-b |
| 10 | 租户隔离 | KB 按租户隔离；跨租户只能经组织共享或共享 Agent | 同租户内没有 per-user ACL | KB-C1-01、KB-C1-03、#993 |
| 11 | 撤权生效时机 | key、JWT、成员关系都按请求查库 | signed_token 撤不掉；在途流式请求不取消；RBAC 关闭时绕过 | KB-C1-03、KB-CT-03①、KB-C1-04 |
| 12 | 鉴权前路由 | 有 7 组路由注册在 Auth 之前，另有 noAuthAPI 白名单 | 集成模式无法整体关闭这些路由 | KB-C1-01 |
| 13 | 日志记正文 | 请求体、响应体各最多 10KB 写入访问日志 | 正文、query 落日志 | KB-OBS-01 |
| 14 | RBAC 可配成放行 | `WEKNORA_TENANT_ENABLE_RBAC=false` 后只记日志不拒绝，非成员按 Admin 放行 | 集成模式必须禁止这样配置 | KB-C1-01 |
| 15 | Lite 无头默认监听 | `0.0.0.0:8080`；端口 0 通不过配置校验 | 没有 loopback 默认、端口回报、ready 握手、--version | KB-LITE-01a |
| 16 | auto-setup 范围 | 仅 Wails 桌面版能用；只建默认用户和空间并签发令牌 | 无头版不能用；不建 KB、不配模型 | KB-LITE-01a、KB-C4-02a/02b |

## 逐项

### 1. scoped API key 与空白名单的语义
- **现状**：key 有 `full_access`、`knowledge_base_ids`、`capabilities` 三个字段（`internal/types/tenant_api_key.go:25-32`），共 25 个 capability（`:78-164`）。
  `/api/v1` 上的 API-key gate 对没有声明策略的路由一律拒绝（`internal/middleware/api_key_gate.go:130-134`）。
  **白名单为空时放行全租户 KB**：`AllowsKnowledgeBase` 遇到空列表直接返回 true（`internal/types/tenant_api_key.go:328-330`），`AllowsKnowledgeBases` 同样如此（`:345-347`）。
  另外，既不要求 full access、又没列 capability 的策略（`apiKeyAny()`）对任何有效 key 都放行（`api_key_gate.go:149-151`；`internal/router/rbac.go:226-228`）。
- **缺口**：“空 = 全部”和契约要求的“空交集即拒绝、默认 deny-all”正好相反。key 范围也没有和用户或权威授权取交集。
- **任务**：KB-CT-02a（#1011 §2 范围语义）、KB-C1-03、KB-C1-01。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/01-scoped-key.json`（待 PR② 产出）

### 2. signed_token 主体（是否影响 ACL 和审计）
- **现状**：租户 `APIPrincipalConfig.Mode=signed_token` 时，从 `X-External-User-Token` 取出 JWT 校验（`internal/middleware/auth.go:595-607`）。
  校验函数在 `auth.go:613-659`：要求 HS256、`aud=weknora`、带 exp，剩余有效期≤24h（`:26`、`:644`）；校验 nbf；token 的 tenant_id 必须等于 key 所属租户（`:650`）；最后只返回 `sub`（`:653-658`）。
  得到的主体是 `api_external_user:<tenant>:<sub>`（`:604-607`），而 key 的权限范围仍原样从 key 复制（`auth.go:550-558`）。
- **ACL**：KB 访问只看 key 范围和租户（`internal/middleware/kb_access.go:202`；`internal/application/access/knowledgebase.go:104-118`），主体不参与。
- **审计**：`audit_logs` 只有 `actor_user_id`（`internal/types/audit_log.go:220`）。API key 请求的 actor 是租户用户或合成的 system 用户（`auth.go:513-524`），外部主体不进表。
- **主体真正影响的地方**：会话归属（`internal/types/principal.go:195-199`；`internal/application/service/session.go:42-44`）、记忆作用域（`internal/application/service/memory/scope.go:33-41`）、用量日志行里的 `principal=`（`internal/models/api/usage_log.go:39-44`）。
- **缺口**：主体不受权威绑定；拿到租户 HMAC 就能伪造任意 sub；没有 jti 消费和撤销；没有 principal 级审计。
- **任务**：KB-C1-03（依赖 #992）、KB-CT-02a §1 信任边界。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/02-signed-token.json`（待 PR② 产出）

### 3. hybrid-search
- **现状**：POST/GET `/api/v1/knowledge-bases/:id/hybrid-search`，路由要求 Viewer 和 KB 读权限，API key 需 retrieve 或 full access（`internal/router/routes_knowledge.go:200`、`:232-233`）。
  handler 在 `internal/handler/knowledgebase.go:321-381`，用的是请求上下文（`:359`），所以客户端断开会传导下去。
  服务层在调用方没给 query_embedding 时会算一次 query embedding（`internal/application/service/knowledgebase_search.go:206-213`）。
- **缺口**：
  - 没有 deadline 和预算参数；服务层的非 AppError 一律包成 500（`knowledgebase.go:366-372`）。
  - `SearchResult.ContentRevision` 不序列化，标的是 `json:"-"`（`internal/types/search.go:220`）。
  - 访问日志会记录 query（`knowledgebase.go:346-347`）。
  - 相关的 `/knowledge-search` 用 `logger.CloneContext` 脱开了请求 ctx（`internal/handler/session/qa.go:829`；`internal/logger/logger.go:511-512`），错误一律返回 500（`qa.go:904`），也会记录 query（`qa.go:897`）。
- **任务**：KB-C1-01c（PR1：deadline 与错误信封；PR3：修订映射）、KB-OBS-01、KB-SVC-POLICY-01、KB-CT-02a §3。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/03-hybrid-search.json`（待 PR② 产出）

### 4. /knowledge/{id} 与 chunks 读取
- **现状**：
  - `GET /knowledge/:id`、`/:id/spans`、`/:id/stages` 要求 Viewer 和父 KB 读权限（`internal/router/routes_knowledge.go:105-107`），handler 在 `internal/handler/knowledge.go:600`。
  - `GET /chunks/:knowledge_id`、`/chunks/by-id/:id`、`/chunks/:knowledge_id/:id/revisions` 走 retrieve（`routes_knowledge.go:31-38`）。分页默认 10、最大 100（`internal/handler/chunk.go:117-125`），默认只返回 text 类型分块（`chunk.go:128`）。
  - Chunk 带 `content_revision` 字段（`internal/types/chunk.go:132`），Knowledge 带 `file_hash` 和 `parse_status`（`internal/types/knowledge.go:189`、`:218`）。
- **缺口**：读取不能指定修订，也不会校验“检索时的修订”；没有 stale 返回；源修订、索引构建、chunk 修订三者之间没有映射；chunk 读取的错误同样一律 500（`chunk.go:74-75`、`:139-141`）。
- **任务**：KB-C1-01c（PR2：正文读取与 stale；PR3：修订映射）、KB-CT-02a §4–§5。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/04-knowledge-chunk-read.json`（待 PR② 产出）

### 5. /download
- **现状**：`GET /knowledge/:id/download`，JWT 需要 Contributor，并对父 KB 有写（Editor）权限（`internal/router/routes_knowledge.go:120`）；handler 里再查一次 Editor（`internal/handler/knowledge.go:1615-1622`）。返回原件，带 `no-store`（`:1634-1636`）。
  API key 只要有 retrieve 就能下载，因为角色守卫对 API key 直接放行（路由注释 `routes_knowledge.go:114-119`；`internal/middleware/rbac.go:75-78`）；KB 白名单仍然生效（`kb_access.go:202`）。
- **缺口**：机器主体下载原件的门槛（retrieve）低于人（Contributor+Editor）。谁能下载原件还没定（#996）。集成模式需要在允许列表里明确开或关。
- **任务**：KB-C1-01、#996、KB-C2-03（下载策略）。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/05-download.json`（待 PR② 产出）

### 6. /preview
- **现状**：`GET /knowledge/:id/preview` 要求 Viewer 和读权限，retrieve key 也可用（`routes_knowledge.go:121`）。handler 返回的是**原始文件**，以内联方式下发，没有 Download 头（`internal/handler/knowledge.go:1653-1679`）。
- **缺口**：preview 实际上等于“Viewer 可取原件”，绕过了 /download 更严的边界。集成模式应当关闭，或者限定为解析后的内容。
- **任务**：KB-C1-01、KB-C2-02（嵌入页首版只读）、#996。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/06-preview.json`（待 PR② 产出）

### 7. /system/info
- **现状**：`GET /api/v1/system/info` 要求 Viewer（`internal/router/routes_auth_tenant.go:255`），返回 Version、Edition、CommitID、BuildTime、引擎和 DB 迁移版本（`internal/handler/system.go:319-380`）。
  它所在的路由组对 API key 默认要求 manage_vector_stores 或 full access（`routes_auth_tenant.go:252`）。
- **缺口**：只有 retrieve 的 key 读不到版本；也没有协议版本字段。
- **任务**：KB-C1-01b-a。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/07-system-info.json`（待 PR② 产出）

### 8. /system/capabilities
- **现状**：任意有效 API key 和 Viewer 都能读（`routes_auth_tenant.go:254`）。13 个键（`internal/handler/deployment_capabilities.go:13-27`），全部来自启动时的路由注册快照（`:58-63`、`:116-118`）；只有 sandbox.docker 是实时值（`:138-154`）。
- **缺口**：capabilities 表示“路由已注册”，不表示依赖健康或业务就绪；没有 integration、knowledge 协议能力字段；集成模式下也不会如实反映已禁用的路由。
- **任务**：KB-C1-01b-a、KB-C1-01（如实声明）、KB-CT-02a §9。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/08-system-capabilities.json`（待 PR② 产出）

### 9. /health
- **现状**：注册在 Auth 之前，写死返回 `{"status":"ok"}`（`internal/router/router.go:145-147`），并在 noAuthAPI 白名单里（`internal/middleware/auth.go:37`）。
- **缺口**：不检查 DB、存储、向量库、模型；没有 /ready；没有按操作（search/read/ingest）的就绪探针。
- **任务**：KB-C1-01b-b、KB-CT-02a §8。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/09-health.json`（待 PR② 产出）

### 10. 租户隔离
- **现状**：
  - 普通 key 不能用 X-Tenant-ID 切换租户（`internal/middleware/auth.go:416-429`）；JWT 切换要经过 `IsTenantAccessible` 检查（`:314-321`）。
  - KB 判定：同租户直接授予 Admin 级访问（`internal/application/access/knowledgebase.go:117-118`）；跨租户只能经组织共享（`:120-126`）或共享 Agent（`:127-154`）。
- **缺口**：同租户内没有 per-user ACL（任何成员、任何同租户 key 都按 KB 白名单看到全部）；集成模式还没有禁止消费既存的共享授权。
- **任务**：KB-C1-01（禁止消费既存共享）、KB-C1-03、#993。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/10-tenant-isolation.json`（待 PR② 产出）

### 11. 撤权生效时机
- **现状**：
  - API key 每次请求都按 hash 查库，并检查 `revoked_at` 和 `expires_at`（`internal/application/service/tenant_api_key.go:91-100`；`internal/application/repository/tenant_api_key.go:27-31`），没有缓存。
  - JWT 每次请求查 token 记录的 IsRevoked（`internal/application/service/user.go:1252-1253`）。
  - 成员关系每次请求查询（`internal/middleware/auth.go:761-767`）。
  - 所以下一个请求就会生效。
- **缺口**：
  - signed_token 没有 jti 和撤销，在 exp 到期前（≤24h）一直有效；只有轮换租户 HMAC 才能让它失效。
  - 已经开始的流式或长请求不会因撤权而中止。
  - RBAC 关闭时，成员被移除后仍按 Admin 放行（见 14）。这是从源码推断的，未经运行核实。
- **任务**：KB-C1-03（撤销、jti）、KB-CT-03①（撤权状态机、generation）、KB-C1-04。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/11-revocation.json`（待 PR② 产出）

### 12. 注册在鉴权之前的路由
- **现状**：全局 Auth 在 `router.go:208` 才挂上。在它之前注册的有：
  - `/health`（`:145`）；
  - 非 release 模式下的 `/swagger/*any`（`:151-158`；`.env.lite.example:5` 默认 `GIN_MODE=debug`）；
  - Lite 前端静态文件（`:170-172`）；
  - IM 回调（`:175`）、embed 公开路由（`:178-187`）、`/mcp/:endpoint_id`（`:191`）、资源 grant（`:195`）、sandbox terminal 和 desktop WS（`:201-202`）、local-browser 三条（`:203-205`）。
  - 此外 noAuthAPI 白名单（`internal/middleware/auth.go:36-63`）还放行 register、login、auto-setup、refresh、OIDC、presigned 等。
- **缺口**：这些路由不经过 API-key gate，也没有集成模式总开关，无法按允许列表关闭。
- **任务**：KB-C1-01。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/12-pre-auth-routes.json`（待 PR② 产出）

### 13. 日志是否记录正文
- **现状**：Logger 中间件对 POST/PUT/PATCH 读取请求体（`internal/middleware/logger.go:179-182`），JSON 和文本类响应体也都捕获；两者各截断到 10KB（`:20`、`:221-233`），以 `request_body`、`response_body` 写入（`:252-258`）。
  只有 local-browser 流量跳过（`:176-177`），SSE 响应跳过（`:223-224`）。
  脱敏只覆盖 password、token、key 这类字段名（`:45-56`）。也就是说，chunk 正文（GET 响应）和 search query（POST 请求体）都会落日志。
  另外 handler 还单独记录 query（`internal/handler/knowledgebase.go:346`、`internal/handler/session/qa.go:897`）。
- **缺口**：服务端默认就记录正文，没有关闭开关。
- **任务**：KB-OBS-01（哨兵秘密负例）。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/13-log-body.txt`（待 PR② 产出，只含哨兵串）

### 14. RBAC 能否配成放行
- **现状**：默认强制执行（`internal/config/config.go:880-885`），但 `WEKNORA_TENANT_ENABLE_RBAC` 设为非 true 的值、或 yaml 写 `enable_rbac: false`，都会关闭（`config.go:876-879`、`:211-220`）。
  关闭后：
  - `RequireRole` 只记日志、照常放行（`internal/middleware/rbac.go:89-95`）；
  - 没有成员关系的 JWT 用户直接按 Admin 放行（`internal/middleware/auth.go:817-828`）。
- **缺口**：集成模式必须拒绝这项配置，或在启动时失败。
- **任务**：KB-C1-01（RBAC 不可放行）。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/14-rbac-off.json`（待 PR② 产出）

### 15. Lite 无头模式的默认监听地址
- **现状**：
  - 无头 Lite 就是 `cmd/server` 加上 `EDITION=lite`（`Makefile:77`、`:288-304`）。监听地址取 `cfg.Server.Host:Port`（`cmd/server/main.go:76-77`），默认 `0.0.0.0:8080`（`config/config.yaml:3-4`）。打包脚本会把 `config/` 原样带上（`scripts/package-lite.sh:77-78`）。
  - 端口必须在 1–65535，`0` 通不过配置校验（`internal/config/config.go:686`）。
  - 启动时只打印配置的 addr，不是实际绑定的地址（`cmd/server/main.go:132`）；没有 `--version`，也没有机器可读的 ready 行。
  - `JWT_SECRET` 为空或是占位值时，每次启动随机生成（`internal/application/service/user.go:81-94`），重启后旧 token 全部失效。
  - 对照：Wails 桌面版默认 `127.0.0.1:0`（`cmd/desktop/main.go:447-456`）。
- **缺口**：没有 loopback 默认、`:0` 端口回报、受控握手、密钥持久化。
- **任务**：KB-LITE-01a、KB-LITE-01b。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/15-lite-listen.json`（待 PR② 产出）

### 16. auto-setup 的范围
- **现状**：
  - `POST /api/v1/auth/auto-setup`（`internal/router/routes_auth_tenant.go:217`）在 noAuthAPI 白名单里（`internal/middleware/auth.go:40`）。
  - handler 要求 `Edition=lite`，并且请求头 `X-WeKnora-Desktop-Token` 要和进程内 token 恒时比较相等（`internal/handler/auth.go:907-917`）。这个 token 只有 Wails 桌面版设置（`cmd/desktop/main.go:217`；setter 在 `internal/handler/auth.go:30`），所以无头 `cmd/server` 调用一律 401。
  - 成功时用固定的默认邮箱常量和随机口令注册用户（空间由 Register 流程建立），然后签发 access/refresh token（`auth.go:919-976`）。
- **缺口**：不建默认 KB，不配置模型和配置档；无头托管没有可用的引导入口；引导凭据和日常凭据没有分开。
- **任务**：KB-LITE-01a（握手通道）、KB-C4-02a（可信 bootstrap）、KB-C4-02b（幂等建库）。
- **证据文件**：`docs/agent24/probe-evidence/4b363d15/16-auto-setup.json`（待 PR② 产出）

## 与 iDoris-ai/Agent24#989 的差异（待回写）

1. **signed_token 行号**：#989 写的是 `auth.go:551-658`。实际 signed_token 分支在 `auth.go:595-607`，校验函数在 `:613-659`；551-558 是 key 范围的赋值。
2. **“signed_token 不影响……”说得太绝对**：ACL 和 audit_logs 确实不受影响，但主体会影响会话归属、记忆作用域和用量日志行（见第 2 项）。
3. **“:0 不回报端口”**：在无头 `cmd/server` 上，端口 0 根本通不过配置校验（`config.go:686`）；`:0` 只出现在桌面版（`cmd/desktop/main.go:456`）。
4. **日志**：#989 只提到请求体（`logger.go:180/253`）。响应体同样会记录（`logger.go:221-233`、`:258`），所以 chunk 正文读取也会落日志；`/knowledge-search` 也记录 query（`qa.go:897`）。
5. **/system/info 列在“可复用”**：retrieve key 读不到（`routes_auth_tenant.go:252`、`:255`），需要 manage_vector_stores 或 full access。
6. **/download、/preview 列在“可复用”**：没写出两点。一是 API key 只要 retrieve 就能下载原件（`routes_knowledge.go:114-120`）；二是 preview 返回的是原件，Viewer 就能拿到（`knowledge.go:1653-1679`）。
7. **“RBAC 可配置为放行 rbac.go:89”**：漏了更严重的一条。RBAC 关闭时，非成员会被授予 Admin（`auth.go:817-828`）。
8. **“撤权下一请求生效”**：对 API key、JWT、成员关系成立；对 signed_token 不成立（没有撤销，exp≤24h）。在途请求也不取消。
9. **鉴权前路由**：#989 只引用 `router.go:174-205`。还应加上 `/health`（`:145`）、非 release 模式的 swagger（`:151`）、Lite 静态文件（`:170`）和 noAuthAPI 白名单（`auth.go:36-63`）。
10. **“v0.8.2 + 2 文档提交”**：这两个提交还新增了 `.github/workflows/agent24-upstream-sync.yml`，不全是文档；Go 源码确实未变。

## 未核实

- 本表全部都是静态结论，运行行为待 PR② 证据。尤其是第 11 项“RBAC 关闭时被移除的成员仍可访问”，这是推断。
- `SERVER_HOST`、`SERVER_PORT` 环境变量能否通过 viper AutomaticEnv 覆盖 `config.yaml` 的 host 和 port（`internal/config/config.go:511-512`），没有逐行追到 viper 内部，也没有实测。
- auto-setup 中 Register 流程具体建了哪些空间和默认资源，没有逐行追到 `userService.Register` 的内部。
- 无头 Lite 在 `GIN_MODE=release` 下的实际行为（swagger 是否关闭、日志级别），没有实测。
