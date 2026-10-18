# Agent24 Knowledge v1 schema 副本（A24-KB-02 测试用）

- 来源：iDoris-ai/Agent24 仓库 `protocol/knowledge/v1/ready.schema.json`、`common.schema.json`，提交 9623505e（PR iDoris-ai/Agent24#1053–#1065，契约 CONTRACT-READONLY-v1 r8）。
- sha256：
  - `ready.schema.json`  3670772188f2a0c84f6e7234006977bbf029764ad44e19f11c38791e524dbfdd
  - `common.schema.json` 4d0f6669c4d7e2ad75e513991a86dbcb90e751c681076ba97a929d826008a278
- 不得手改：`agent24_knowledge_ready_schema_test.go` 会校验上面两个 sha256；JSON 文件不能加注释，所以来源写在这里。
- 同步方法：Agent24 契约或 schema 改动后，从 Agent24 对应提交原样复制这两份文件，
  再更新本文件与测试里的 sha256 和提交号。
