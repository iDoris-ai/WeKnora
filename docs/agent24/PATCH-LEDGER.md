# Agent24 扩展与补丁清单

**当前产品基线**：上游 `v0.8.2`（`3e8b0bf`），2026-10-16 按 #4 登记，评估见 `BASELINE-EVAL-v0.8.3.md`。上游 `v0.8.3` 已进 `upstream-release` 镜像但暂不吸收，入口是同步 issue #9。**升级触发条件**（也写在 `UPSTREAM-BASELINE.json` 的 `next_review_trigger`）：第一个改动 v0.8.3 也改过的文件（路由、`middleware/logger.go`、hybrid-search 的 handler/service、`types/search.go` 等）的产品补丁 PR 开出之前，以及任何对外制品出货之前，先合入同步 PR；同步通道不通时等它修好，不绕过。

| 编号 | 范围与目的 | 路径 | 上游升级处置 / 验证 |
|---|---|---|---|
| A24-BOOT-01 | 产品规划、分支维护和初始基线 | `docs/agent24/` | 保留独立命名空间；上游更新后核对文档证据与实施状态 |
| A24-BOOT-02 | 两条纯镜像定时快进；有新正式版时开或更新同步 issue，由评审人审完 `.github/` 后自己开同步 PR；推镜像用组织 App 短期令牌，推送前守卫拦截会被镜像推送激活的上游工作流 | `.github/workflows/agent24-upstream-sync.yml`、`.github/scripts/agent24-mirror-trigger-guard.rb`、`.github/scripts/agent24-mirror-trigger-guard-test.sh`；外部依赖：组织 App「iDoris WeKnora Sync」、Environment `upstream-sync`（见 FORK-MAINTENANCE.md「同步凭据」） | 检查权限、镜像 SHA 和幂等；不自动批准/合并，不发布产品。上游 `.github/` 冲突时保留这三个文件；改守卫后跑 `bash .github/scripts/agent24-mirror-trigger-guard-test.sh`，全部 PASS，并用守卫检查当前上游 main 与最新正式版；工作流每次运行也会先在 runner 上跑这个测试，失败就停止 |
| A24-DESIGN-01 | 唯一主对话与知识服务职责、KB-C0–C4 验收设计 | `docs/agent24/ADR-001-SINGLE-CONVERSATION.md`、总体方案 | 用户已确认产品方向，工程接口待评审；上游升级检查无第二聊天、授权与原知识能力回归；本次仅文档 |
| A24-DESIGN-02 | 默认一站式知识库，隐藏普通用户的技术配置成本 | `docs/agent24/TODO-B-ONE-STOP-KNOWLEDGE.md` | 对照 #1 的初始化/隔离/费用验收；先固定配置，调优后续；本次仅文档 |

当前没有 RAG、解析、数据库、Agent 或 UI 核心修改，产品代码与 v0.8.2 逐字节相同。新增 feature 必须补登记：目的、影响路径、测试、能否回馈上游，以及下一次升级如何保留/调整。
