# Agent24 扩展与补丁清单

**当前产品基线**：上游 `v0.8.2`（`3e8b0bf`），2026-10-16 按 #4 登记，评估见 `BASELINE-EVAL-v0.8.3.md`。截至 2026-10-16 评估过 `v0.8.3`，暂不吸收，下次吸收的入口是同步 issue #9（标题会随新正式版改写）。

**必须先升级的时点**（与 `UPSTREAM-BASELINE.json` 的 `current_baseline.upgrade_before` 一致）：
1. 开始第一个产品补丁的 task 分支之前（不是写完再判断）。产品补丁指改动或新增 fork 自有命名空间（`docs/agent24/`、`scripts/agent24/`、`.github/scripts/agent24-*`、`.github/workflows/agent24-*`）之外路径的任何工作，不论是否与上游改动相交。开 PR 时用 `git diff --name-only origin/a24/develop...<补丁分支> | grep -v -E '^(docs/agent24/|scripts/agent24/|\.github/(scripts|workflows)/agent24-)'` 复核，输出非空就停下：先完成升级并合入 a24/develop，再 rebase。
2. 导入任何非测试资料、或让开发者以外的人访问任何实例之前。
3. 任何对外制品出货之前。
4. 需要启用 `minio`、`langfuse` 或 `full` compose profile 时（v0.8.2 钉的 quay.io MinIO 镜像已不能匿名拉取；默认 `STORAGE_TYPE=local` 不受影响）。
5. 上游发布影响 v0.8.2 的安全公告时，或到 2026-11-15 时，重新评估（David 负责）。

**留守期间的受测栈约束**：在 `.env` 里设 `WEKNORA_VERSION=v0.8.2`（`.env.example` 默认 latest，而 Docker Hub 的 `latest` 已等于 v0.8.3），起栈后与登记的 digest 比对、不一致就停；不用 `start_all.sh` 的默认拉取；只用默认服务、`STORAGE_TYPE=local`，不启用 profile；误跑过 latest 的数据卷丢弃重建；`APP_PORT`、`FRONTEND_PORT` 绑定 `127.0.0.1`；Lite 与标准栈的结果不互相替代。详见评估文档。

升级做法：届时最新正式版不是 v0.8.3 时先补差异评估；按 #9 的五步开同步 PR；按评估的「若升级的必做清单」做备份与恢复演练、Go 与前端回归、契约回归（KB-CT-02b 完成前以新 SHA 上重跑 PR② 探测并重核矩阵代替）、LICENSE 核对；最后更新本表与基线文件。凡会改动命名空间之外路径的 WeKnora 任务，建卡时都以“基线已升级”为前置，已登记到 Agent24#1001。同步通道不通时等它修好，不绕过；不通超过 7 天时上报 David 与 jason。

| 编号 | 范围与目的 | 路径 | 上游升级处置 / 验证 |
|---|---|---|---|
| A24-BOOT-01 | 产品规划、分支维护和初始基线 | `docs/agent24/` | 保留独立命名空间；上游更新后核对文档证据与实施状态 |
| A24-BOOT-02 | 两条纯镜像定时快进；有新正式版时开或更新同步 issue，由评审人审完 `.github/` 后自己开同步 PR；推镜像用组织 App 短期令牌，推送前守卫拦截会被镜像推送激活的上游工作流 | `.github/workflows/agent24-upstream-sync.yml`、`.github/scripts/agent24-mirror-trigger-guard.rb`、`.github/scripts/agent24-mirror-trigger-guard-test.sh`；外部依赖：组织 App「iDoris WeKnora Sync」、Environment `upstream-sync`（见 FORK-MAINTENANCE.md「同步凭据」） | 检查权限、镜像 SHA 和幂等；不自动批准/合并，不发布产品。上游 `.github/` 冲突时保留这三个文件；改守卫后跑 `bash .github/scripts/agent24-mirror-trigger-guard-test.sh`，全部 PASS，并用守卫检查当前上游 main 与最新正式版；工作流每次运行也会先在 runner 上跑这个测试，失败就停止 |
| A24-DESIGN-01 | 唯一主对话与知识服务职责、KB-C0–C4 验收设计 | `docs/agent24/ADR-001-SINGLE-CONVERSATION.md`、总体方案 | 用户已确认产品方向，工程接口待评审；上游升级检查无第二聊天、授权与原知识能力回归；本次仅文档 |
| A24-DESIGN-02 | 默认一站式知识库，隐藏普通用户的技术配置成本 | `docs/agent24/TODO-B-ONE-STOP-KNOWLEDGE.md` | 对照 #1 的初始化/隔离/费用验收；先固定配置，调优后续；本次仅文档 |

当前没有 RAG、解析、数据库、Agent 或 UI 核心修改，产品代码与 v0.8.2 逐字节相同（截至 2026-10-16）。新增 feature 必须补登记：目的、影响路径、测试、能否回馈上游，以及下一次升级如何保留/调整。
