# Agent24 CONTRACT-OPS-v2 schema 与夹具副本（A24-KB-43 的测试用）

- 来源：iDoris-ai/Agent24 提交 f7291afd（ab/weknora，含 OPS-v2 终稿 55ed0540 与 READONLY schema r13）：
  - `protocol/knowledge/ops-v2/candidate.schema.json`
  - `protocol/knowledge/fixtures/ops-v2/schema-cases.json`
  - `protocol/knowledge/fixtures/ops-v2/entry-hash.samples.json`
- sha256：
  - `candidate.schema.json`     3ef5bc360ec8358d690a6bb34d86581fc1f3f32fb25f11b10a1055203748510a
  - `schema-cases.json`         cc2f9acf9715fd64524cc32e20c27e738e1d81ed9f4cea940251a1379801db29
  - `entry-hash.samples.json`   c8069d60b6ee2e5789aa2cfa7a4dfb1c08228cfef16ca78bbdf8fedc2cbf5b2e
- 不得手改：`agent24_knowledge_candidates_test.go` 校验这三个 sha256。同步方法同 `../agent24_knowledge/README.md`。
