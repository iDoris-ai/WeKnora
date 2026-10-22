# Agent24 Knowledge v1 schema 副本（就绪与 search 端点的测试用）

- 来源：iDoris-ai/Agent24 仓库 `protocol/knowledge/v1/` 下的 `ready.schema.json`、`search.schema.json`、`error.schema.json`、`common.schema.json`，提交 9121a7b2（契约 CONTRACT-READONLY-v1 r10）。
- sha256：
  - `ready.schema.json`  3670772188f2a0c84f6e7234006977bbf029764ad44e19f11c38791e524dbfdd
  - `search.schema.json` 6f714593bfaaeb1e3b3a5063b1610eaa19da74b3e37f28b1dd2b25c257fba16f
  - `error.schema.json`  02dea455203cf503f61e0995f62605fffbcd832af627a5fb4a210820cd81649e
  - `common.schema.json` 97de36a9af3a1315bdacda1243c9f8dd84a1f15993f9c757698b73c2bc345f52
- 不得手改：`agent24_knowledge_ready_schema_test.go` 会校验上面四个 sha256；JSON 文件不能加注释，所以来源写在这里。
- 同步方法：Agent24 契约或 schema 改动后，从 Agent24 对应提交原样复制这些文件，
  再更新本文件与测试里的 sha256 和提交号。
