# KB-C0-02 基线评估：上游 v0.8.2 与 v0.8.3

- **任务**：iDoris-ai/WeKnora#4（KB-C0-02）；依据任务方案 v2.1 的 D7 与 §5。
- **对比对象**：`v0.8.2` = `3e8b0bfc80b845b2d4b2ed683994748741450a97`（2026-09-24）；`v0.8.3` = `3e22822c1d2397be730c458c7539d0eb8c113a24`（2026-10-14 发布）。下文简写 `3e8b0bf`、`3e22822`。
- **fork 现状**（截至 2026-10-16 UTC）：`origin/a24/develop` = `e5da6de`。它相对 v0.8.2 改了 10 个文件：`docs/agent24/` 7 个（含 #7 合入的 `CAPABILITY-MATRIX.md`），`.github/` 下 Agent24 自己的同步工作流和守卫 3 个（`git diff --name-only 3e8b0bf origin/a24/develop`）。产品代码与 v0.8.2 逐字节相同。同步现状：评估开始时 `upstream-release` 还停在 `3e8b0bf`；配好 App 凭据后，手动运行（run 38924672104）与一次定时运行（16:46 UTC）都成功，`upstream-release` 已前进到 `3e22822`（v0.8.3），同步 issue 为 #9；#3 还差 1 次定时运行。
- **方法**：只看两个 tag 的源码树差异（`git diff 3e8b0bf 3e22822`）。本地 clone 是浅克隆，看不到中间历史，所以 release 说明和 `CHANGELOG.md` 只用来定位改动，结论以源码为准。下文 `v0.8.3:路径:行` 指 v0.8.3 树里的行号。
- **规模**：1576 个文件，+142102/−16333 行。其中 Go 非测试代码 490 个文件（+33392，不含 swagger 生成物 `docs/docs.go`；含则 491 个、+35784），前端 281 个文件，迁移 32 个文件，docreader 30 个文件（含 4 个 proto 生成物）。上游自己的发布核对写明，这次核对“不是对 282 个提交重新进行完整安全审计”（`v0.8.3:docs/release-v0.8.3-audit.md:9`），并且“未运行全量 Go/前端业务回归或实际数据库升级”（同文件 `:77`）。

## 结论

**先把 v0.8.2 登记为当前产品基线，暂不升 v0.8.3。** 两种选择都不涉及新费用、新外部出口、许可证或范围变化（MinIO 换发布者只在启用 minio profile 时才相关，见必做清单第 5 条），按 D7 由工程决定，**不升级为用户决策，不在 Agent24 #994 作为决策登记**；只在 #994 补了一条 rerank 出口的事实说明（详见文末“是否升级为用户决策”）。理由见“结论与理由”。

## 1. 数据库迁移

PostgreSQL 新增 000111–000118，SQLite 新增 000031–000036；另有 3 个已发布的迁移被改写，`paradedb/00-init-db.sql` 也有改动（`git diff --name-status 3e8b0bf 3e22822 -- migrations/`）。mysql 目录没有改动。

| 迁移 | 内容 | 能否回滚 |
|---|---|---|
| PG 111 / SQLite 031 `model_catalog_config` | 新建单行表 `model_catalog_configs`，存管理员发布的模型目录 overlay | 可以：down 会删表，但已发布的 overlay 一并丢失 |
| PG 112 `embeddings_knowledge_chunk_index` | 给 `embeddings(knowledge_id)`、`(chunk_id)` 建 btree 索引；没有用 CONCURRENTLY（`v0.8.3:migrations/versioned/000112_embeddings_knowledge_chunk_index.up.sql:11`），大表建索引期间会锁表 | 可以（down 删索引） |
| PG 113 / SQLite 032 `chunk_images` | 画廊用的投影表，加 chunks 上的触发器和函数，并回填已有数据（`000113_chunk_images.up.sql` 共 177 行） | 可以（down 删表、触发器和函数）；回填耗时与图片分块数成正比 |
| PG 114 / SQLite 033 `chunk_source_locators` | `chunks` 加 `source_locators` 列 | 可以：down 删列，定位数据丢失；SQLite 的 down 用 `DROP COLUMN`，要求 3.35 或更高 |
| PG 115 `pg_search_02511` | 把 pg_search 升到 0.25.11、vector 升到 0.8.4（`000115_pg_search_02511.up.sql`），配合把镜像从 `paradedb:v0.22.6-pg17` 换成 `v0.25.11-pg17`（`v0.8.3:docker-compose.yml:544`、`:822`） | **不能**：down 是 No-op（`v0.8.3:migrations/versioned/000115_pg_search_02511.down.sql:1`），只能连同旧镜像一起从备份恢复 |
| PG 116 / SQLite 035 `agent_allowed_tools_explicit_defaults` | 改写数据：smart-reasoning 智能体的 `allowed_tools: []` 被写成显式默认工具列表，因为 `[]` 的语义改成了“不选任何内置工具” | **不能**：两边的 down 都是空操作（`v0.8.3:migrations/versioned/000116_agent_allowed_tools_explicit_defaults.down.sql:4`；`v0.8.3:migrations/sqlite/000035_agent_allowed_tools_explicit_defaults.down.sql:2`，`:1` 是注释） |
| PG 117 / SQLite 036 `messages_attachment_lookup` | 部分索引；PG 端是 GIN 索引，用 CONCURRENTLY 建（`000117_messages_attachment_lookup.up.sql:5`） | 可以（down 删索引） |
| PG 118 `mcp_oauth_timestamp_tz` | 把 MCP OAuth 表的时间列改成 timestamptz，原值按 UTC 解释 | 可以（down 按 UTC 转回）；进程时区不是 UTC 时写入的旧行会偏移，需要重新授权 |
| SQLite 034 `mcp_metadata` | 补建 `mcp_metadata` 表，`mcp_services` 加列 | 可以，但 down 依赖 `DROP COLUMN`（`v0.8.3:migrations/sqlite/000034_mcp_metadata.down.sql:2`），要求 SQLite 3.35 或更高 |
| 改写的 PG 093、094、099 | 093、094 的 CREATE 改成 IF NOT EXISTS；099 在镜像已提供 0.25.11 时跳过（`git diff 3e8b0bf 3e22822 -- migrations/versioned/000093_browser_authorization.up.sql migrations/versioned/000094_memory_consistency.up.sql migrations/versioned/000099_pg_search_0226.up.sql`） | 只影响新库或重跑；已执行过的库不会再执行 |
| `paradedb/00-init-db.sql` | 新库初始化时直接建 112 的两个索引 | — |

**小结**：fork 目前没有真实数据，升级的数据风险只落在测试实例上。但只要升级，115 和 116 就不能靠 down 回退，`FORK-MAINTENANCE.md` 要求的“一致性备份 + 恢复演练”必须在升级时做。

## 2. 依赖变化及其许可证

| 位置 | 变化 | 许可证 |
|---|---|---|
| `go.mod` | `mark3labs/mcp-go` v0.52.0 → **v1.1.1**（大版本，`v0.8.3:go.mod:37`）；新增直接依赖 `golang.org/x/image` v0.38.0（`:80`，v0.8.2 的 go.mod 没有这一行，`go.sum` 里原本就有；用于 `internal/models/imageprep/prepare.go`；`go.sum` 只改了 mcp-go 的两行）；`milvus-proto` 去掉 indirect 标记（`:243`） | mcp-go：MIT，两个版本的 LICENSE blob 相同（`3d48435`）；x/image：BSD-3-Clause |
| `cli/go.mod` | `go-runewidth` 0.0.28 → 0.0.30，`modelcontextprotocol/go-sdk` 1.7.0 → 1.8.0（`v0.8.3:cli/go.mod:10-11`） | runewidth：MIT。go-sdk：MIT 向 Apache-2.0 过渡中，1.7.0 与 1.8.0 的 LICENSE blob 相同（`5791499`），没有变化 |
| `frontend/package.json` | 新增 `pdfjs-dist ^6.3.289`（`v0.8.3:frontend/package.json:37`），用于原文定位预览；lockfile 另带进 `@napi-rs/canvas` 1.0.9 主包及 11 个平台包，都是 optional | pdfjs-dist：Apache-2.0；@napi-rs/canvas：MIT。worker 随前端打包（`v0.8.3:frontend/src/components/source-preview/pdfjsLoader.ts:3`、`:16`），不走 CDN |
| `website-docs/`、`misc/intro-video/` | 只改版本号和脚本；新增 `playwright-core 1.49.1`，只用来渲染宣传视频 | 不进产品制品 |
| `third_party/anydoc-go`（Rust，链进 app） | `anydoc` =0.1.9 → =0.2.4（`v0.8.3:third_party/anydoc-go/Cargo.toml:22`）；Cargo.lock 去掉 6 个 crate（calamine 等），没有新增 crate | MIT（`:11`；上游 anydoc 两个版本的 LICENSE 相同） |
| docreader（Python） | `docreader/pyproject.toml`、`uv.lock` 都没有改（`git diff --stat 3e8b0bf 3e22822 -- docreader/pyproject.toml docreader/uv.lock` 为空）；新增启动探针 `docreader/container_entrypoint.py` | 不变 |
| 新的可选脚本 | `scripts/wemm-transformers/requirements.txt`（transformers 5.2.0 等），用于自建 WeMM embedding 服务 | 不进默认制品 |
| 容器镜像 | ParadeDB v0.22.6 → v0.25.11（见第 1 节）；**MinIO 镜像换了发布者**：`quay.io/minio/minio` → `pgsty/minio:RELEASE.2026-08-04T00-00-00Z`（`v0.8.3:docker-compose.yml:625`、`:879`；dev compose 里 `minio` 服务用未钉版本的 `pgsty/minio:latest`（`v0.8.3:docker-compose.dev.yml:85`），`langfuse-minio` 钉到 `pgsty/minio:RELEASE.2026-08-04T00-00-00Z`（`:441`）） | ParadeDB、MinIO 都是 AGPL-3.0，与之前相同；但 MinIO 改由社区（Pigsty）重新构建发布，属于供应链来源变化。原因是 MinIO 官方已关闭 Docker Hub 与 quay.io 的匿名拉取（`v0.8.3:website-docs/01-getting-started/05-troubleshooting.md:26`）：**v0.8.2 钉的 `quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z`（`3e8b0bf:docker-compose.yml:613`、`:867`）已不能匿名拉取**（2026-10-16 实测带匿名令牌仍 401，对照镜像 200）。这只影响 `minio`、`langfuse`、`full` 三个 compose profile；默认 `STORAGE_TYPE=local`（`3e8b0bf:.env.example:188`），Lite 也是 local，不受影响 |
| `LICENSE` | 第三方声明从 3262 行缩到 283 行：去掉了 paddle、playwright、numpy、PIL、MPL-2.0 组件等清单，也去掉了指向 `THIRD_PARTY_NOTICES.md` 的那句；现在只列 opencc、anydoc、wails（`v0.8.3:LICENSE:7`、`:41`、`:254`、`:258`）。`THIRD_PARTY_NOTICES.md` 和 `licenses/` 都没改 | 项目本身仍是 MIT。这不是许可证变更，但 fork 按 D6 自建 Lite 制品时，第三方声明要自己核全，不能照搬 v0.8.3 的 LICENSE |

## 3. 默认配置变化

`config/config.yaml`、`.env.lite.example` 都没有改（`git diff --stat 3e8b0bf 3e22822 -- config/config.yaml .env.lite.example` 为空）。实际生效的默认值变化如下：

| 项 | 变化 | 证据 |
|---|---|---|
| 注册模式 | 新增 `invite_register` 档。`invite_only` 现在连“凭邀请注册”也关掉。默认仍是 `self_serve` | `v0.8.3:internal/config/config.go:298`、`:879`；`v0.8.3:internal/handler/auth.go:186`；`v0.8.3:CHANGELOG.md:65` |
| `allowed_tools: []` 的语义 | 原来表示“默认工具”，现在表示“没有内置工具”；不设置（null）才是默认工具。存量数据由迁移 116/035 改写；被改写的 smart-reasoning 智能体的“全部 KB”范围不再包含只有 Wiki 的 KB | `v0.8.3:CHANGELOG.md:66` |
| 出站拨号超时 | `WEKNORA_OUTBOUND_DIAL_TIMEOUT` 默认 5s | `v0.8.3:internal/config/config.go:793-795`；`v0.8.3:docker-compose.yml:103` |
| KB 描述生成的 token 预算 | 800 → 2048，可用 `KNOWLEDGE_BASE_PROFILE_MAX_TOKENS` 调整 | `v0.8.3:internal/application/service/knowledgebase_profile.go:42`；`git diff 3e8b0bf 3e22822 -- internal/application/service/knowledgebase_profile.go` |
| 解析超时 | 新增 MinerU、PaddleOCR-VL、WeKnoraCloud 的超时环境变量，默认 1000s / 600s / 20m | `git diff 3e8b0bf 3e22822 -- .env.example docker-compose.yml` |
| 图片向量召回 | 新能力，默认关闭；要求 KB 开启 `image_vector_enabled` | `v0.8.3:internal/types/knowledgebase.go:913-914` |
| 对话附件保留 | live 会话里的附件改为保留到上传 TTL 之后，存储占用增加（留存行为变化，与方案 §5 的保留与删除要求有关；集成模式下主聊天被拒绝，影响小） | `v0.8.3:CHANGELOG.md:70`；迁移 117 的注释 |
| 镜像 | ParadeDB、MinIO 换镜像（见第 2 节）；Helm 默认把镜像钉到 appVersion | `git diff 3e8b0bf 3e22822 -- docker-compose.yml helm/values.yaml` |
| 构建 | app 镜像的 apt 源可配；docreader 可配 Playwright 制品源 | `git diff 3e8b0bf 3e22822 -- docker/Dockerfile.app docker/Dockerfile.docreader` |

## 4. API 变化

**新注册的路由**（`git diff 3e8b0bf 3e22822 -- 'internal/**/*.go' ':!*_test.go'` 里所有新增的路由注册；没有删除任何路由）：

| 路由 | 权限 | API key 能否调用 | 证据 |
|---|---|---|---|
| `GET`/`POST /knowledge-bases/:id/images`、`GET /knowledge-bases/:id/gallery-config` | Viewer + KB 读权限 | retrieve 即可 | `v0.8.3:internal/router/routes_knowledge.go:89-101` |
| `GET /image-attrs/schema` | Viewer | retrieve 即可 | `v0.8.3:internal/router/routes_knowledge.go:281` |
| `POST /wiki-search`（跨 KB 搜索 Wiki，返回标题、摘要和匹配片段，`v0.8.3:internal/handler/wiki_page.go:1115-1131`） | Viewer | retrieve 即可；KB 白名单为空时放行全部（`v0.8.3:internal/handler/wiki_page.go:1092`） | `v0.8.3:internal/router/routes_knowledge.go:372` |
| `GET /agents/tools` | Viewer | 沿用智能体路由组的策略 | `v0.8.3:internal/router/routes_agent.go:38` |
| `POST /models/:id/copy`（复制模型配置，连同已存凭证） | Admin | — | `v0.8.3:internal/router/routes_infra.go:35` |
| `POST /mcp-services/:id/oauth/assertion`（无浏览器时用 JWT Bearer assertion 换 MCP OAuth 令牌） | Viewer | — | `v0.8.3:internal/router/routes_infra.go:189` |
| `/system/admin/model-catalog`（GET/PUT，以及 `POST /preview`）、`GET /system/admin/users`、`POST /system/admin/users/status` | SystemAdmin JWT | 被 API-key gate 默认拒绝 | `v0.8.3:internal/router/routes_auth_tenant.go:293-302` |

**已有接口的字段变化**：
- hybrid-search 请求新增可选的 `rerank` 对象（`v0.8.3:internal/types/search.go:279`；`v0.8.3:internal/types/rerank.go:11-15`）。不传时行为与 v0.8.2 相同；传了且没有显式写 `"enabled": false` 时就会调用租户配置的 rerank 模型，响应里多一个 `meta`（`v0.8.3:internal/handler/knowledgebase.go:345-399`）。
- `SearchResult` 新增 `source_locators`、`matched_images` 两个字段，都是 omitempty（`v0.8.3:internal/types/search.go:199`、`:233`）。
- app 与 docreader 之间的 gRPC 契约新增 `SourceBlock` 和 `source_blocks = 6`（`v0.8.3:docreader/proto/docreader.proto:48`、`:63`、`:73`），是 `source_locators` 的来源。线路上向后兼容；两个镜像版本不配套时只是拿不到原文定位。
- `/knowledge-search` 请求新增 `match_count`、两个阈值、两个检索开关和 `rerank`；响应总是带 `meta`（请求选项在 `v0.8.3:internal/handler/session/qa.go:917`，`meta` 在 `:951`；`git diff 3e8b0bf 3e22822 -- internal/handler/session/qa.go`）。
- `PUT /knowledge-bases/:id` 可以修改 `vlm_config`，只有 owner 空间或 admin 共享可以改（`git diff 3e8b0bf 3e22822 -- internal/handler/knowledgebase.go`）。
- 对话接口允许只传图片或文件、不带文字。
- 上传接口对格式错误的 `.json` 文件直接返回 400（`git diff 3e8b0bf 3e22822 -- internal/handler/knowledge.go` 中 `CreateKnowledgeFromFile` 的新 hunk）。
- `/system/capabilities` 新增键 `settings.sandbox.remote`（`v0.8.3:internal/handler/deployment_capabilities.go:27`、`:120`）。
- MCP 端点调用允许列表外的工具时，改为返回 JSON-RPC `-32602`（`v0.8.3:CHANGELOG.md:71`）。
- Swagger 里多出来的 `/artifacts`、`/sessions/{id}/rewind` 等路径，是补写了已有接口的文档，不是新接口（`v0.8.3:docs/release-v0.8.3-audit.md` “第二轮整体复核”一节）。

## 5. 安全相关修复（逐项按源码核对过的）

- **访问日志不再把整个请求体读进内存**：v0.8.2 对 POST/PUT/PATCH 且 Content-Type 为 json、form 或 `text/` 的请求（包括鉴权前的路由，跳过 `/assets/`、wiki stats 和本机浏览器流量）执行无上限的 `io.ReadAll`（`3e8b0bf:internal/middleware/logger.go:96-101`、`:105`、`:168-182`）；v0.8.3 只读 10KB+1 字节，其余部分原样交给 handler（`v0.8.3:internal/middleware/logger.go:122`）。脱敏正则只多了 `assertion` 一项（`:59`）。
- **LLM 调试日志**：目录和文件权限改为 0700/0600（`v0.8.3:internal/logger/llm_logger.go:40`、`:105`）；文件名里来自客户端 `X-Request-ID` 的部分会被清洗，`../` 不再能改写落盘目录（`:116-122`）。这项功能默认关闭，只有设置了对应环境变量才启用。
- **令牌和账号**：签发令牌时如果写库失败，登录直接报错，不再返回一对下一个请求就失效的令牌（`v0.8.3:internal/application/service/user.go:1166-1169`）；查库出错时如实报错，不再谎称“token is revoked”（`:1319`）；被停用的账号，已签发的令牌逐请求失效（`:1337`）；系统管理员停用用户时，在同一事务里撤销该用户的全部令牌（`v0.8.3:internal/handler/system.go:1595`；实际撤销在 `v0.8.3:internal/application/repository/user.go:269-270`）。
- **授权收紧**：数据分析只作用于有授权的共享 KB（#4136）；共享智能体操作本机浏览器时，按调用者而不是 owner 配对（`git diff 3e8b0bf 3e22822 -- internal/agent/tools/browserskill.go`）。
- **授权放宽**：同租户的 Admin 可以管理其他成员的 IM 渠道会话（`git diff 3e8b0bf 3e22822 -- internal/application/service/session.go` 里的 `loadSessionForManage`）。
- **限量与稳健性**：Feishu、Notion、IMA、Yuque、GitLab 的分页和响应体加了上限（#3985、#3887 等）；远程图片下载受文件大小上限约束（#3906）；RSS 超大响应直接拒绝（#3658）；WeCom 自定义 wss 端点经过 SSRF 检查（#4070）；agent 子 goroutine 里的 panic 不再导致进程退出（#3846）；MCP 资源分页加了上限（#4007）。这一行只按 CHANGELOG 定位、抽查过源码，没有逐条核对。
- **数据完整性**：新 ParadeDB 镜像带的 pgvector 0.8.1 → 0.8.4 修复了并行 HNSW 构建的缓冲区溢出和 vacuum 期间的索引损坏；pg_search 从 0.24.0 起有基于 WAL 的崩溃恢复（`v0.8.3:website-docs/01-getting-started/06-paradedb-upgrade.md:5`）。这是“导入真实资料前必须升级”的直接理由。
- **没有修的**：能力矩阵第 13 项里“租户 HMAC（`hmac_secret`）明文进访问日志”仍然存在：正则只匹配整个字段名，列表里没有 `hmac_secret`（`v0.8.3:internal/middleware/logger.go:54-59`）。

## 6. 对能力矩阵结论的影响

能力矩阵（`docs/agent24/CAPABILITY-MATRIX.md`，#7 已合入；受测 SHA `4b363d15` = v0.8.2 的产品代码）引用的核心文件在 v0.8.3 **一个字节都没改**：`internal/middleware/{auth,api_key_gate,rbac,kb_access}.go`、`internal/types/tenant_api_key.go`、`internal/application/access/knowledgebase.go`、`internal/router/files.go`、`internal/handler/{chunk,tenant,knowledge_download}.go`、`internal/filetransport/response.go`、`internal/logger/logger.go`、`internal/application/service/{tenant_member,kbshare}.go`、`internal/types/{audit_log,principal}.go`、`internal/router/static.go`、`cmd/server/listen.go`、`config/config.yaml`、`.env.lite.example`、`.github/workflows/release-lite.yml`。验证方法：`git diff --shortstat 3e8b0bf 3e22822 -- <上述路径>`，每一项输出都为空。

| # | 能力 | v0.8.3 是否改变结论 | 说明与证据 |
|---|---|---|---|
| 1 | scoped key / 空白名单 | 不变 | 判定代码没改；新增的 `/wiki-search`、`/images` 沿用“白名单空 = 全部”（`v0.8.3:internal/handler/wiki_page.go:1092`；`routes_knowledge.go:89-101`）。集成模式的允许列表要逐一决定是否放行第 4 节表中前 4 行的新路由（允许列表默认拒绝） |
| 2 | signed_token / direct | 不变 | `auth.go`、`tenant.go`、审计相关类型都没改。新增的 MCP OAuth assertion（`routes_infra.go:189`）给按主体归属的 MCP 令牌多了一条不经浏览器的获取路径，但不改变“主体不受权威绑定”这一结论 |
| 3 | hybrid-search | 结论不变，细节有变 | 仍然没有调用方 deadline；非 AppError 仍然返回 500 并带回 `err.Error()`（`v0.8.3:internal/handler/knowledgebase.go:388`）；`ContentRevision` 仍是 `json:"-"`（`v0.8.3:internal/types/search.go:244`）；query 全文仍会写进日志（`knowledgebase.go:355`；`knowledgebase_search.go:167`）；仍会自己算 query embedding。**新增**：主 KB 没有 embedding 模型时改用范围内另一个 KB 的模型（`knowledgebase_search.go:242`；v0.8.2 只用主 KB，`3e8b0bf:internal/application/service/knowledgebase_search.go:206-209`）；可选 rerank，开启后会调用模型；**单个 store 部分失败时返回部分结果、错误为 nil，只记一条 WARN**（`v0.8.3:internal/application/service/knowledgebase_search_fanout.go:172-184`），契约的 partial 语义要覆盖这种情况；多了 `source_locators`。矩阵引用的行号会漂移 |
| 4 | /knowledge 与 chunks | 不变 | `handler/chunk.go` 没改；Chunk 多了 `source_locators`（有助于 KB-C1-01c 定位），但仍然不能按修订读取，也没有 stale 语义 |
| 5 | /download、batch-download、/files | 不变 | 下载 handler 函数体未改，行号漂移：`DownloadKnowledgeFile` 从 `3e8b0bf:internal/handler/knowledge.go:1603` 移到 `v0.8.3:internal/handler/knowledge.go:1987`，函数体逐行相同；`/download` 路由从 `routes_knowledge.go:120` 移到 `:138`，batch-download 仍在 `:78`；`knowledge_download.go`、`router/files.go`、`filetransport/response.go` 没改 |
| 6 | /preview | 不变 | `handler/knowledge.go` 的改动集中在画廊（`git diff 3e8b0bf 3e22822 -- internal/handler/knowledge.go` 显示，最后一个 hunk 从 v0.8.2 的 1104 行开始，只新增画廊函数），preview 函数体没改，行号漂移（`PreviewKnowledgeFile` 1653 → 2037，路由 `:121` → `:139`） |
| 7 | /system/info | 不变 | `system.go` 只新增了 ListUsers/SetUserStatus；路由策略没改 |
| 8 | /system/capabilities | 文字要改 | 键从 13 个变成 14 个（新增 `settings.sandbox.remote`，`v0.8.3:internal/handler/deployment_capabilities.go:27`）；“只表示路由已注册”这一结论不变 |
| 9 | /health | 不变 | `router.go` 只改了 CORS 头（:130）和多注册了一组路由（:263）；`/health` 与 Auth 挂载点（:208）没动 |
| 10 | 租户隔离 | 不变（有一处放宽） | 判定代码没改；同租户 Admin 可以管理他人的 IM 渠道会话（见第 5 节）；CHANGELOG 写了 MCP 端点范围纳入共享 KB（#3848），没有逐行核对 |
| 11 | 撤权时机 | 不变（多了一个手段） | signed_token 仍然没有 jti；新增“停用账号”这一撤销手段，而且令牌校验会检查账号状态（`user.go:1337`） |
| 12 | 鉴权前路由与注册 | 不变（多了一个选项） | 路由结构没变；默认仍是 `self_serve`（`config.go:879`）；新增的 `invite_register` 可以作为 KB-C1-01 关闭公开注册的手段之一 |
| 13 | 日志记正文 | 不变 | 正文仍然最多记 10KB（`logger.go:20`、`:270`、`:275`）；`hmac_secret` 仍会明文落日志；只是请求体不再整体缓冲；Langfuse 只多了 `vector_score` 字段 |
| 14 | RBAC 可配成放行 | 不变 | `rbac.go`、`auth.go` 都没改 |
| 15 | 无头 Lite 默认监听 | 不变 | `config.yaml`、`listen.go` 没改；`JWT_SECRET` 为空时仍在启动时随机生成（`v0.8.3:internal/application/service/user.go:81-94`）。持久化 JWT secret 的改动只在 Wails 桌面版（`git diff 3e8b0bf 3e22822 -- cmd/desktop/signing_key.go`）；Lite 本机技能也只在桌面版（`v0.8.3:internal/application/service/tenant_skill_host.go:22-28`） |
| 16 | auto-setup | 不变 | `handler/auth.go` 只有 Register 这一个 hunk |

**影响**：16 项结论没有一项因 v0.8.3 失效，也没有一个缺口被上游补上。如果升级，能力矩阵要换受测 SHA，并按必做清单第 4 条的机械规则重核（不只限于上表“结论不变，细节有变”和“文字要改”两类）；PR② 的探测要在新 SHA 上跑。

## 7. `.github/` 的改动与守卫

- 改动只有 3 个文件（`git diff --name-status 3e8b0bf 3e22822 -- .github/`）：
  - `ISSUE_TEMPLATE/config.yml`：只换了一个文档链接。
  - `workflows/docker-image.yml`：Cube 镜像加了 arm64 平台。触发条件仍是 push 到 `main` 或 `v*` tag（`v0.8.3:.github/workflows/docker-image.yml:2-7`），会引用 DockerHub 的 secret；镜像分支不在触发范围内。
  - 新增 `workflows/helm.yml`：push 只针对 `main`（`v0.8.3:.github/workflows/helm.yml:5-6`）；另有 `pull_request`（`:10`），路径过滤为 `helm/**` 和 `.github/workflows/helm.yml` 本身（`:11-13`）；工作流级权限 `contents: read`（`:15-16`）；没有引用 GitHub secret（`secrets.dbPassword` 之类是 helm `--set` 的参数字符串）；用到一个新的外部 action `azure/setup-helm@v4`，按 tag 引用，没有钉 SHA（`:26`）。
- **守卫**：用 a24/develop 上的 `.github/scripts/agent24-mirror-trigger-guard.rb` 检查 `3e22822 upstream-release`，输出 `mirror trigger guard: ok`，退出码 0；对 `3e8b0bf` 同样通过。这与 `FORK-MAINTENANCE.md` 里“上游 main 与 v0.8.3 都能通过”的说法一致。
- **评审人开同步 PR 时要注意**：v0.8.3 改了 `helm/**`，所以 `helm.yml` 会在同步 PR 上运行（只读权限、不用 secret，可以接受；`azure/setup-helm@v4` 要按 FORK-MAINTENANCE 逐个确认）。其余 `pull_request` 类工作流与 v0.8.2 相同，没有新增写权限或 secret 引用。
- **与 fork 补丁的冲突**：上游改动的文件和 fork 的 10 个文件（文首 `e5da6de` 的清单）没有交集（`comm -12` 两份文件清单，结果为空），升级合并在文件层面不会冲突。

## 结论与理由

**先把 v0.8.2 登记为当前产品基线；升 v0.8.3 作为一次普通的上游同步，等条件满足后再做。**

1. **同步通道还在观察期，升级的前置步骤都没做。** 截至 2026-10-16 16:46 UTC，#3 修复后手动运行 1 次、定时运行 1 次成功，还差 1 次定时运行；按 #4 步骤 3，升级还要走同步 PR 的五步评审和回归，这些都还没做。留在 v0.8.2 则按步骤 2 可以直接登记，不依赖 #3。
2. **D7 要求探测用固定 SHA。** 能力矩阵已按固定 SHA `4b363d15` 合入（#7）；PR② 的探测还没开始，同样要在固定 SHA 上做。现在换 SHA，矩阵就得重核行号，而且按方案 §3，升级 PR 要额外占一个名额，排期顺延。需要说明：按下面的时点，升级会发生在第一个产品补丁之前，所以换 SHA、重跑探测这些返工只是**延后，并没有省掉**；换来的是 PR② 能按固定 SHA 落地，以及届时可以直接跳到更新的正式版。
3. **升级换不来当前需要的东西。**（这一条和下一条说的是“不必急着升”，不是“不升”：按下面的时点，升级终将发生。） 第 6 节的 16 项结论全部不变；KB-C1 要补的缺口（允许列表、权威主体、日志政策、deadline/错误分类、修订、Lite 托管）在 v0.8.3 里一个都没变。v0.8.3 带来的好处（rerank 选项、source_locators、`invite_register`、令牌修复）都不是 KB-CT-01/02 和首个只读切片的前提。
4. **升级的成本和不可逆性是实打实的。** 迁移 115（扩展升级）和 116/035（改写数据）都不能回退；ParadeDB 大版本升级，MinIO 换了发布者；改动规模 1576 个文件，而上游自己既没跑全量回归、也没演练数据库升级（`docs/release-v0.8.3-audit.md:77`）。我们要先补上 Go、前端和契约回归，契约回归此时还没有可跑的 schema 测试（KB-CT-02b 未做）。

**反方理由（如实记录）**：现在 fork 没有产品补丁、没有真实数据，与上游改动的文件也没有交集，这是升级成本最低的时候。之后 KB-C1-01（路由）、KB-OBS-01（`logger.go`）、KB-C1-01c（hybrid-search 的 handler、service 和 `types/search.go`）落下的补丁，恰好都在 v0.8.3 改动过的文件里，再吸收上游时会有冲突要手工解决。

**所以登记时一并写明必须先升级的时点**（`UPSTREAM-BASELINE.json` 的 `current_baseline.upgrade_before` 与 PATCH-LEDGER 开头写法一致）：
1. 开始第一个产品补丁的 task 分支之前（不是写完再判断，免得在 v0.8.2 上白写）。产品补丁指改动或新增 fork 自有命名空间（`docs/agent24/`、`scripts/agent24/`、`.github/scripts/agent24-*`、`.github/workflows/agent24-*`，例如 PR② 的探测脚本放在 `scripts/agent24/probe/`）之外路径的任何工作，**不论是否与上游改动相交**。开工前按计划改动的路径清单判断；开 PR 时用 `git diff --name-only --no-renames origin/a24/develop...<补丁分支> | grep -v -E '^(docs/agent24/|scripts/agent24/|\.github/(scripts|workflows)/agent24-)'` 复核，**输出非空就停下**：先按下面的做法完成升级并合入 a24/develop，再 rebase 补丁分支。`--no-renames` 必须带：默认的重命名检测会让从命名空间外移进来的文件只显示新路径，漏掉被删的产品路径。这条复核只在 `current_baseline` 仍是 v0.8.2 时适用；同步 PR（head 为 `upstream-release`）本身就是升级，不适用。目前靠人工执行。`comm -12` 与上游改动文件取交集只作辅助参考，不作判据。
2. 导入任何非测试资料、或让开发者以外的人访问任何实例之前：v0.8.2 在鉴权前把 json、form、`text/` 类 POST/PUT/PATCH 请求的请求体整体无上限读入内存（`3e8b0bf:internal/middleware/logger.go:105`，Logger 挂在 Auth 之前），而无头 Lite 默认监听 `0.0.0.0`（能力矩阵第 15 项）。
3. 任何对外制品出货之前。
4. 需要启用 `minio`、`langfuse` 或 `full` compose profile 时（见第 2 节 MinIO 一行）。不用不入库的本地 override 绕开，免得受测环境与登记不一致。

**重新评估**（不是必须先升级的时点，结论可以是继续留守）：上游发布影响 v0.8.2 的安全公告时，或到 2026-11-15（距本次评估满 30 天）时，重新评估是否提前升级。由 David 负责关注上游的 Release 与 Security Advisories。对应 `UPSTREAM-BASELINE.json` 的 `upgrade_before.reevaluate_by` 与 `review_triggers`；目前没有自动提醒，人工提醒见 Agent24#999 的任务项。

**留守期间的受测栈约束**（PR② 的探测与任何产品验证都要遵守）：
- **应用镜像必须钉版本。** v0.8.2 的 compose 用 `wechatopenai/weknora-{ui,app,sandbox,docreader}:${WEKNORA_VERSION:-latest}`（`3e8b0bf:docker-compose.yml:3`、`:37`、`:396`、`:408`），而且 `.env.example` 本身就写着 `WEKNORA_VERSION=latest`（`3e8b0bf:.env.example:34`），照常 `cp .env.example .env` 也会拉 latest。Docker Hub 上 app、ui、docreader 三个镜像的 `latest` 都已等于 v0.8.3（2026-10-16 实测）。所以必须在 `.env` 里改成 `WEKNORA_VERSION=v0.8.2`，在 compose 项目根目录（`3e8b0bf` 的 checkout；a24/develop 的 `docker-compose.yml`、`.env.example` 与它相同也可以）先 `unset WEKNORA_VERSION`（shell 里导出的变量优先于 `.env`），再用 `docker compose config --images` 确认 compose 实际解析出的正好是下面列的 5 个引用，不符就停；然后 `docker compose pull` 拉取 5 个默认服务的镜像，再逐个比对：`docker image inspect --format '{{json .RepoDigests}}' <镜像:tag>` 的结果里必须含 `<仓库>@<下面登记的 digest>`，为空也算不一致（5 个镜像依次是 `wechatopenai/weknora-ui:v0.8.2`（frontend 服务）、`wechatopenai/weknora-app:v0.8.2`、`wechatopenai/weknora-docreader:v0.8.2`、`paradedb/paradedb:v0.22.6-pg17`、`redis:7.0-alpine`，仓库名照写，redis 是短名 `redis`），**不一致就停，不起栈**；比对通过后再用 `docker compose up -d --no-build` 起栈（v0.8.2 的 compose 没有设 `pull_policy`，默认只拉本机缺的镜像，不会改拉别的版本），起栈后用 `docker inspect --format '{{.Image}}' <容器>`（容器名是 `WeKnora-{frontend,app,docreader,postgres,redis}`，`3e8b0bf:docker-compose.yml:14`、`:44`、`:414`、`:533`、`:554`）确认各容器的 image ID 等于刚比对过的镜像的 `{{.Id}}`，不一致就停栈，数据卷按下面“丢弃重建”一条处理。某个 tag 被改指、比对不一致时，可以 `docker pull <仓库>@<登记 digest>`，再 `docker tag <仓库>@<登记 digest> <仓库>:<tag>` 打回原 tag；打回之后不要再对这个服务 `docker compose pull`（会重新解析 tag），只 pull 其余服务，照常比对 5 项后再 `up -d --no-build`。先比对再起栈，是因为误拉 latest 时 app 一启动就会把库迁移到 111–118（见下面“丢弃重建”一条）；证据记录这 5 个 digest 与应用自报的版本。
- **不用 `scripts/start_all.sh`，`--no-pull` 也不行。** 它默认 `up --pull always`（`3e8b0bf:scripts/start_all.sh:387`）；`--no-pull` 走的是 `up --build -d`（`:380-384`），而 frontend、app、docreader 都带 `build:`（`3e8b0bf:docker-compose.yml:8`、`:38`、`:409`），会用当前 checkout 重建并覆盖本地的 `v0.8.2` tag，本地构建的镜像 digest 不会等于登记值，比对注定失败；`--no-pull` 还会设 `START_OLLAMA=true`（`:666-668`），在宿主机拉起 Ollama，超出“只用默认服务”。
- **从 `3e8b0bf` 本地构建是另一条路**，只在拉不到官方 app、ui、docreader 镜像时用。在 `3e8b0bf` 的干净 git 工作树根目录（例如 `git worktree add <目录> 3e8b0bf`）执行：
  ```bash
  eval "$(./scripts/get_version.sh env)"   # VERSION 取自 VERSION 文件（0.8.2），COMMIT_ID 取 git rev-parse --short HEAD
  T=v0.8.2-local
  docker build --build-arg VERSION_ARG="$VERSION" --build-arg COMMIT_ID_ARG="$COMMIT_ID" \
    --build-arg BUILD_TIME_ARG="$BUILD_TIME" --build-arg GO_VERSION_ARG="$GO_VERSION" --build-arg WITH_ANYDOC=1 \
    -f docker/Dockerfile.app -t wechatopenai/weknora-app:$T .
  docker build -f docker/Dockerfile.docreader -t wechatopenai/weknora-docreader:$T .
  docker build --build-arg VITE_FRONTEND_COMMIT="$COMMIT_ID" -f frontend/Dockerfile -t wechatopenai/weknora-ui:$T frontend/
  ```
  然后在 `.env` 里设 `WEKNORA_VERSION=v0.8.2-local`，同样先 `unset WEKNORA_VERSION`，用 `docker compose config --images` 确认解析出的是三个 `:v0.8.2-local` 加 postgres、redis 那两个引用，不符就停；再 `docker compose pull postgres redis`（本地构建的三个镜像不在 Docker Hub 上，不对它们 pull）并比对这两个的 digest，通过后再 `docker compose up -d --no-build`（打回过 tag 的服务同样不再 pull），起栈后同样核对各容器的 image ID，本地构建的三个以构建出的镜像的 `{{.Id}}` 为准。版本参数必须显式传：`.dockerignore` 排除了 `.git/`（`3e8b0bf:.dockerignore:2`），镜像里的 `make build-prod` 取不到 `git describe`，只能用构建参数（`3e8b0bf:Makefile:275-283`、`3e8b0bf:docker/Dockerfile.app:52-61`），而 compose 只给 app 传 `APK_MIRROR_ARG` 和 `WITH_ANYDOC`（`3e8b0bf:docker-compose.yml:38-43`），所以用 `docker compose build` 时自报版本会是 `unknown`。也不用 `make docker-build-*`，它固定打 `:latest`（`3e8b0bf:Makefile:81`、`:151`、`:158`）；上面的命令就是这几个 make 目标（`:137-158`）去掉 `--platform`、改了 tag。宿主机没装 Go 时 `GO_VERSION` 为 `unknown`，不影响判断。留证：postgres、redis 仍按下面登记的 digest 比对；本地构建的 app、ui、docreader 的 digest 不会等于登记值，改为记录源码 SHA（`git rev-parse HEAD` 等于 `3e8b0bfc…`，且 `git status --porcelain` 为空）、三个镜像的 image ID（`docker image inspect --format '{{.Id}}'`）、应用自报版本（带 Viewer 级凭据请求 `GET /api/v1/system/info`，响应包在 `data` 里：`data.version` 应为 `0.8.2`，`data.commit_id` 以 `3e8b0bf` 开头；`3e8b0bf:internal/handler/system.go:281-283`、`:359-361`、`:375-379`、`3e8b0bf:internal/router/routes_auth_tenant.go:252-255`；用 API key 时须是 full_access 或带 `manage_vector_stores` 能力的 key，retrieve 档的 key 会被拒）和实际传入的构建参数；证据标明“本地构建”。
- **只用默认服务**（`frontend`、`app`、`docreader`、`postgres`、`redis`），不启用任何 profile；显式写出 `STORAGE_TYPE=local`。三个应用镜像的 v0.8.2 digest（2026-10-16 查得）：app `sha256:bc8a534799fcb54045100daf4bc5db86270b38ae45c5f761c410c50b4dcd6cdc`、ui `sha256:83cfd9272e1219b5b5729f89d9c3f8d15a16e1b8cc17cacf638153cc0417290f`、docreader `sha256:3c36e7e738ba515315523876d8041f9bb02809145a4e3b331e2550e4bff98b7a`。postgres 与 redis 用的是可变 tag（`paradedb/paradedb:v0.22.6-pg17`、`redis:7.0-alpine`，`3e8b0bf:docker-compose.yml:532`、`:553`），同样要比对：postgres `sha256:58bf87d2a6f1e72f56590b0f0ae1467e82c3f99ca203dde646672c8ac13148b2`、redis `sha256:c9d92d840fd011c908f040592857c724ae6d877f2aba5c40ad963276507386b2`（2026-10-17 用 `docker image inspect --format '{{json .RepoDigests}}' <镜像>` 只读查本机已拉取的镜像得到，没有联网核对 Docker Hub 上这两个 tag 的当前指向；不一致时先查明 tag 是否被改指，再重新登记；要继续用已登记的版本，按上面的办法打回原 tag。五个登记值都是多架构 index digest，各平台通用）。
- **误跑过 latest 的数据卷一律丢弃重建。** v0.8.3 会把库迁移到 111–118，新表、新列和 116 改写的数据都不可回退（115 只在新 ParadeDB 镜像上才升级扩展，在 v0.8.2 的旧镜像上是空操作），而 v0.8.2 启动时迁移失败只记警告、照常启动（`3e8b0bf:internal/container/container.go:898-904`），会在不匹配的 schema 上悄悄运行。证据里同时记录 `schema_migrations` 的版本，v0.8.2 应为 110。结论不覆盖 MinIO/S3 系后端；例如 local 后端签名链接有效期与 MinIO 不同，不能外推。
- **Lite 与标准栈不能互相替代。** Lite 走 SQLite 迁移与另一套默认配置，用它跑的结果不能算作标准栈证据，反之亦然。
- **实例只绑定本机或隔离网络**（见时点 2）。默认 compose 把 `${APP_PORT:-8080}:8080`、`${FRONTEND_PORT:-80}:80` 映射到所有网卡（`3e8b0bf:docker-compose.yml:46`、`:16`），所以要设 `APP_PORT=127.0.0.1:8080`；frontend 只需绑 `127.0.0.1`，端口任意（例如 `FRONTEND_PORT=127.0.0.1:18080`，本机 80 端口可能被占用；探测只访问 app，frontend 的端口不影响任何结论），并在证据里记录 `docker port` 的输出。

为了让这些时点真正生效，门槛按规则执行，不只限于点名的任务：凡是 Target repo 为 WeKnora、会改动命名空间之外路径的任务，建卡时都要写入“基线已升级（#9 已吸收）”这一依赖。KB-CT-02b 在 WeKnora 侧的 Go 测试放在 `scripts/agent24/` 下，不放 `internal/`，所以不算产品补丁。**登记位置**：这条规则最初只写在 Agent24#1001（WS2 epic）的一条评论里；2026-10-16 UTC 已写进三个 epic 正文的进入条件（WeKnora#11）：#1001（KB-C1-01、KB-C1-01b-a、KB-C1-01b-b、KB-C1-01c、KB-C1-03、KB-OBS-01、KB-SVC-POLICY-01 等）、#1002（WS3：KB-C2-01、KB-C2-02）、#1003（WS4：KB-LITE-01a，以及按规则判断的 KB-LITE-01b；#1003 的进入条件已写明 KB-C0-02 只表示基线已登记，不表示已升级）。时点 2 已挂到 #1001 退出验收的“真实数据验收”，时点 3 已挂到 #1003 的出货验收。任务级依赖以三个 epic 正文为准，基线数据与时点以 `UPSTREAM-BASELINE.json` 为准。受测栈约束同样写进 WeKnora#5（PR②）的验收。同步通道（#3）不通时等它修好，不绕过；不通超过 7 天时上报 David 与 jason。KB-C1-01c 与 KB-CT-02a 的修订、定位设计应以 v0.8.3 的 `source_locators` 与 `SourceBlock` 为准，避免重做上游已有的东西。

做法：届时最新正式版不是 v0.8.3 时，先补一份 v0.8.2 到该版本的差异评估；按同步 issue 的五步开同步 PR；再按下面的“若升级的必做清单”执行。

## 是否升级为用户决策（D7 / §5）

按 D7，只有涉及**新费用、外部出口、许可证或范围**变化时才升级为用户决策，并在 Agent24 #994 登记。

- **留在 v0.8.2（本结论）**：代码、依赖、配置都不变，四项都不涉及。**不升级为用户决策，不在 #994 登记**，按 #4 步骤 2 直接登记即可。
- **如果改选升到 v0.8.3**，逐项判断如下，结论同样是**不必升级为用户决策**（MinIO 来源只在启用 minio profile 时才需要请用户确认，见必做清单第 5 条）：
  - 费用：没有默认开启的新付费调用。KB 描述的 max_tokens 从 800 提到 2048 只是上限，属于既有功能；图片向量召回、rerank 都要显式开启。
  - 出口：Go 代码里新出现的外部主机只有模型和连接器厂商目录的默认地址（Pinecone、Cohere、MiniMax 等），只在用户配置了该厂商时才会用到；前端 pdf.js 的 worker 随包分发；文档链接要用户点击才访问。**边界情况**：hybrid-search 新增的 `rerank` 选项，让持有 retrieve key 的调用方可以触发调用租户已配置的 rerank 服务，把 query 和候选正文发给它。`/knowledge-search` 在 v0.8.2 已经有这条路径。它属于 #994 正在讨论的“允许哪些模型服务、费用上限”范围，不是新的决策点；事实说明已补进 #994（2026-10-16 UTC），升级时由 KB-C1-01 的允许列表明确禁用或放行 `rerank` 字段。
  - 许可证：项目仍是 MIT；新增依赖分别是 Apache-2.0、MIT、BSD-3；ParadeDB 和 MinIO 仍是 AGPL-3.0。`LICENSE` 里第三方声明的缩减属于合规义务（自建 Lite 制品时自己核全声明），不是许可证变化。
  - 范围：升级不改变 Knowledge 的产品范围。新功能（画廊、多模态、跨库 Wiki、MCP assertion）在集成模式下由允许列表默认拒绝。

## 若升级的必做清单（供以后的同步 PR 用）

1. 先确认 #3 的同步工作流成功；按 FORK-MAINTENANCE 的五步暂停同步、核对镜像 SHA、审 `.github/`（重点是 `helm.yml` 和 `azure/setup-helm@v4`），然后再开同步 PR。
2. 测试库先做一致性备份，再演练升级和恢复（ParadeDB v0.25.11 加迁移 111–118；Lite 用 SQLite 031–036）；按上游的 `website-docs/01-getting-started/06-paradedb-upgrade.md` 操作，**不要**对新镜像执行旧的 0.22.6 升级命令。
3. 升级前先在当前基线上跑一遍 Go（`go test ./...`）和前端测试，记下失败清单；升级后再跑，对照区分回归与原有失败。契约回归：KB-CT-02b 完成后跑 schema 与夹具测试；在此之前以第 4 条的“新 SHA 上重跑 PR② 探测并重核能力矩阵”代替。
4. 把能力矩阵换成新 SHA，按机械规则重核：矩阵引用的产品路径（简写按全路径还原）里，凡是 `git diff --name-only --no-renames <旧 SHA> <新 SHA> -- <路径>` 有输出的，全部重核行号与结论，不按“看起来相关”挑选。PR-Daemon 对 #12 的统计：v0.8.2 → v0.8.3 矩阵引用约 62 个路径，28 个有输出；其中 `handler/knowledge.go` 的下载与 preview 函数体没改，但行号整体漂移（第 6 节第 5、6 行），`types/chunk.go`、`types/knowledge.go` 也有漂移。在新 SHA 上重跑 PR② 的探测；允许列表补上第 4 节列出的新路由和 `rerank` 字段。
5. MinIO 镜像换了发布者（`pgsty/minio`），属于供应链来源变化，不在 D7 的四项（费用、出口、许可证、范围）之内。由工程决定：不启用 `minio` profile（默认 `STORAGE_TYPE=local`）时不需要处理；真要启用时（时点 4），再请用户（David）确认沿用 `pgsty/minio` 还是在 fork 的 compose 里覆盖，结果写进 PATCH-LEDGER。
6. rerank 出口的事实说明已在 2026-10-16 UTC 补进 Agent24 #994（v0.8.2 的 `/knowledge-search` 已有这条路径，v0.8.3 又加到 hybrid-search）；升级时由 KB-C1-01 的允许列表对 `rerank` 字段表态。
7. fork 按 D6 自建 Lite 制品时，app 与 docreader 配套构建（`SourceBlock` 契约）。
8. 核对 `LICENSE`：同步会把 fork 的 LICENSE 换成上游缩短后的版本，并丢掉指向 `THIRD_PARTY_NOTICES.md` 的那一句；合并时恢复这条指引，或在 PATCH-LEDGER 写明接受上游写法。
9. KB-LITE-01b 若已在 v0.8.2 上测过体积、内存和启动时间，升级后重新测量。
10. 更新 `UPSTREAM-BASELINE.json` 的 `current_baseline`（`initial_snapshot` 保持不动），同时更新 `PATCH-LEDGER.md`。

## 未核实 / 不确定

- 本地 clone 是浅克隆，看不到 v0.8.2 到 v0.8.3 之间的提交历史。上表全部来自两个 tag 的源码树差异，release 说明里的 PR 号只作索引。#3848（MCP 端点范围纳入共享 KB）和部分连接器限量修复只按 CHANGELOG 定位，没有逐行核对。
- 新增路由沿用的 API-key 策略，只看了路由注册和 `wiki-search` 的 handler；`/images` 返回的图片 URL 再经哪个出口取字节（`/files` 还是 KB 范围的图片代理），没有追到底。
- 依赖许可证是用 GitHub API 只读查询 LICENSE 得到的（仓库级 SPDX 和指定 tag 的 LICENSE blob），没有对 Go、npm、Cargo 依赖做传递闭包级别的许可证扫描。
- 没有运行任何回归测试，也没有做数据库升级演练；迁移是否可逆只是读 SQL 得出的判断。
- “升级成本最低的窗口就是现在”与“先留守”之间的取舍属于工程判断，两边理由都写在上面了。

## 决定记录

2026-10-16 按本评估登记 v0.8.2 为当前产品基线。按方案 §5 这属于工程决定。另做了一次盲选交叉确认作参考（不是 Grok/Codex 交叉确认）：当日 Codex 与 Grok 都不可用，改由 Opus 5.5 与 Sonnet 5.5 两个替代评审（同一厂商，独立性弱于原流程）在看不到本文结论的情况下各自读事实部分，都选“留在 v0.8.2”，都判断不改变目标、费用、出口、许可证或范围；得知 MinIO 与 `latest` 镜像的事实后复核，选择不变。两者都建议把升级时点写成硬条件，已写入 `UPSTREAM-BASELINE.json` 的 `current_baseline.upgrade_before` 和 PATCH-LEDGER 开头。另一条补充：v0.8.2 上未修的访问日志无上限读取、令牌写库失败等问题，只影响当前的测试实例；导入真实资料、开放给开发者以外的人访问、对外制品出货之前都必须先升级（见上面的时点 2、3）。
