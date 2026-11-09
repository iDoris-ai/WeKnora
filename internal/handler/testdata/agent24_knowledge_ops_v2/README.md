# Agent24 CONTRACT-OPS-v2 schema 与夹具副本（A24-KB-43 的测试用）

- 来源：iDoris-ai/Agent24 提交 aa98363a（ab/weknora，#1706 合入：OPS-v2 r2，加了 get_request/get_response 与 7 条用例；含 READONLY schema r13）：
  - `protocol/knowledge/ops-v2/candidate.schema.json`
  - `protocol/knowledge/fixtures/ops-v2/schema-cases.json`
  - `protocol/knowledge/fixtures/ops-v2/entry-hash.samples.json`
- sha256：
  - `candidate.schema.json`     54ad995b7550fedf130cfe84fcf889e15b57f975a26998f68230ad36c3539e9e
  - `schema-cases.json`         8ec64a631b6550a3dc1a123a7ea92bcab5f0f581c4f9df09d266b682f96e81f4
  - `entry-hash.samples.json`   c8069d60b6ee2e5789aa2cfa7a4dfb1c08228cfef16ca78bbdf8fedc2cbf5b2e
- 不得手改：`agent24_knowledge_candidates_test.go` 校验这三个 sha256。同步方法同 `../agent24_knowledge/README.md`。
