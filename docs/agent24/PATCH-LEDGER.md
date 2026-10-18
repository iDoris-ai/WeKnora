# Agent24 扩展与补丁清单

**当前产品基线**：上游 `v0.8.3`（`3e22822`），2026-10-18 经同步 PR #44 吸收（合并提交 `167d24c`，任务 #43），评估见 `BASELINE-EVAL-v0.8.3.md`；v0.8.2 时期的登记见 2026-10-16。下次吸收的入口是同步 issue #9（标题会随新正式版改写）。

**必须先升级的时点**（与 `UPSTREAM-BASELINE.json` 的 `current_baseline.upgrade_before` 一致）：
1. 开始第一个产品补丁的 task 分支之前（不是写完再判断）。产品补丁指改动或新增 fork 自有命名空间（`docs/agent24/`、`scripts/agent24/`、`.github/scripts/agent24-*`、`.github/workflows/agent24-*`）之外路径的任何工作，不论是否与上游改动相交。开 PR 时用 `git diff --name-only --no-renames origin/a24/develop...<补丁分支> | grep -v -E '^(docs/agent24/|scripts/agent24/|\.github/(scripts|workflows)/agent24-)'` 复核（`--no-renames` 不可省），输出非空就停下：先完成升级并合入 a24/develop，再 rebase。这条复核在当前基线落后于上游最新正式版时适用，同步 PR（head 为 `upstream-release`）不适用。
2. 导入任何非测试资料、或让开发者以外的人访问任何实例之前。
3. 任何对外制品出货之前。
4. 需要启用 `minio`、`langfuse` 或 `full` compose profile 时（设立时的理由：v0.8.2 钉的 quay.io MinIO 镜像已不能匿名拉取；v0.8.3 的 minio 改用 pgsty/minio，启用前仍按评估文档核对；默认 `STORAGE_TYPE=local` 不受影响）。

**重新评估**（不是必须先升级的时点）：上游发布影响当前基线的安全公告时，或到 2026-11-15 时，重新评估是否提前升级（David 负责；见 `upgrade_before.reevaluate_by` 与 `review_triggers`）。

**受测栈约束**：在 `.env` 里设 `WEKNORA_VERSION=v0.8.3`（`.env.example` 默认 latest，而 Docker Hub 的 `latest` 会随上游前进），每条 `docker compose` 命令前加 `env -u WEKNORA_VERSION -u COMPOSE_FILE -u COMPOSE_PROFILES -u COMPOSE_ENV_FILES -u COMPOSE_DISABLE_ENV_FILE`（不依赖 shell 状态），先用 `docker compose config --images` 核对解析出的镜像引用、用 `docker compose config` 核对 app 的 `STORAGE_TYPE` 为 `local` 和两个端口的 `host_ip` 为 `127.0.0.1`（过滤 `STORAGE_TYPE|host_ip|published|- mode:` 须正好七行、`published` 正好两个，多出映射或缺 `host_ip` 即停；项目根目录不得有 override 文件；只看这几项，完整输出含密钥，不贴进证据），再 `docker compose pull`，5 个默认服务的镜像都与登记的 digest 比对（RepoDigests 为空也算不一致）、不一致就停、不起栈，比对通过后再 `docker compose up -d --no-build`，起栈后核对各容器的 image ID（tag 被改指时的处理见评估文档）；不用 `start_all.sh`（`--no-pull` 也不行）；从 `3e22822` 本地构建时按评估文档的命令显式传版本参数、打 `v0.8.3-local` 标签，每条 compose 命令前同样加 `env -u WEKNORA_VERSION -u COMPOSE_FILE -u COMPOSE_PROFILES -u COMPOSE_ENV_FILES -u COMPOSE_DISABLE_ENV_FILE`，用 `docker compose config --images` 和 `docker compose config` 核对，只 `docker compose pull postgres redis`、这两个比对 digest 通过后再 `docker compose up -d --no-build`，起栈后核对 image ID（完整步骤以评估文档为准），本地构建的 app、ui、docreader 改记源码 SHA、image ID、应用自报版本和构建参数；只用默认服务、`STORAGE_TYPE=local`（起栈后用 `docker exec WeKnora-app printenv STORAGE_TYPE` 留证，不是 `local` 就停栈），不启用 profile；误跑过 latest 的数据卷丢弃重建；`APP_PORT` 绑 `127.0.0.1:8080`，frontend 只需绑 `127.0.0.1`、端口任意，起栈后 `docker port` 不符、或按 compose 项目 label 过滤的 `docker ps -a` 不是正好 5 个默认容器（共享宿主机上别的容器不算）就停栈；Lite 与标准栈的结果不互相替代。详见评估文档。

升级做法：届时最新正式版不是已评估过的版本时先补差异评估；按 #9 的五步开同步 PR；按评估的「若升级的必做清单」做备份与恢复演练、Go 与前端回归、契约回归（在新 SHA 上全量重跑 PR② 探测并重核矩阵；KB-CT-02b 不能代替）、LICENSE 核对；最后更新本表与基线文件。凡会改动命名空间之外路径的 WeKnora 任务，建卡时都以“基线已升级”为前置；这条规则已于 2026-10-16 UTC 写进 Agent24#1001、#1002、#1003 正文的进入条件（WeKnora#11），任务级依赖以三个 epic 正文为准，基线数据与时点以 UPSTREAM-BASELINE.json 为准。同步通道不通时等它修好，不绕过；不通超过 7 天时上报 David 与 jason。

| 编号 | 范围与目的 | 路径 | 上游升级处置 / 验证 |
|---|---|---|---|
| A24-BOOT-01 | 产品规划、分支维护和初始基线 | `docs/agent24/` | 保留独立命名空间；上游更新后核对文档证据与实施状态 |
| A24-BOOT-02 | 两条纯镜像定时快进；有新正式版时开或更新同步 issue，由评审人审完 `.github/` 后自己开同步 PR；推镜像用组织 App 短期令牌，推送前守卫拦截会被镜像推送激活的上游工作流 | `.github/workflows/agent24-upstream-sync.yml`、`.github/scripts/agent24-mirror-trigger-guard.rb`、`.github/scripts/agent24-mirror-trigger-guard-test.sh`；外部依赖：组织 App「iDoris WeKnora Sync」、Environment `upstream-sync`（见 FORK-MAINTENANCE.md「同步凭据」） | 检查权限、镜像 SHA 和幂等；不自动批准/合并，不发布产品。上游 `.github/` 冲突时保留这三个文件；改守卫后跑 `bash .github/scripts/agent24-mirror-trigger-guard-test.sh`，全部 PASS，并用守卫检查当前上游 main 与最新正式版；工作流每次运行也会先在 runner 上跑这个测试，失败就停止 |
| A24-DESIGN-01 | 唯一主对话与知识服务职责、KB-C0–C4 验收设计 | `docs/agent24/ADR-001-SINGLE-CONVERSATION.md`、总体方案 | 用户已确认产品方向，工程接口待评审；上游升级检查无第二聊天、授权与原知识能力回归；本次仅文档 |
| A24-DESIGN-02 | 默认一站式知识库，隐藏普通用户的技术配置成本 | `docs/agent24/TODO-B-ONE-STOP-KNOWLEDGE.md` | 对照 #1 的初始化/隔离/费用验收；先固定配置，调优后续；本次仅文档 |

当前没有 RAG、解析、数据库、Agent 或 UI 核心修改，产品代码与 v0.8.3 逐字节相同（截至 2026-10-18；v0.8.2 时期见 2026-10-16 的登记）。新增 feature 必须补登记：目的、影响路径、测试、能否回馈上游，以及下一次升级如何保留/调整。

## 2026-10-18 基线升级到 v0.8.3

1. **吸收方式与结果**：任务 iDoris-ai/WeKnora#43（KB-C0-03）。同步 PR iDoris-ai/WeKnora#44 以普通 merge 吸收上游正式版 `v0.8.3`（`3e22822c1d2397be730c458c7539d0eb8c113a24`），合并提交 `167d24c24b4d8497cc6b31db02507ae52f0aa116`。产品代码等同 v0.8.3（命名空间外与 `3e22822` 逐字节相同），产品补丁截至 2026-10-18 仍为 none。合并前核对：本地合并结果上 Go（vet/test/desktop/build，以及 `-tags anydoc` 的 docparser vet/test 与 server build）与前端（test/type-check/build）全部通过；ParadeDB `v0.25.11` 加迁移 111–118 的备份恢复演练通过（升级后迁移 118 非 dirty；备份在 `v0.22.6` 独立卷恢复，计数一致）。`schema_migrations` 版本从 110（v0.8.2）变为 118。默认 5 服务镜像 digest 已登记到 `UPSTREAM-BASELINE.json` 的 `current_baseline.image_digests`（as_of 2026-10-18）。
2. **LICENSE**：上游把 `LICENSE` 从 3262 行缩到 283 行，并删掉了指向 `THIRD_PARTY_NOTICES.md` 的那句。fork 接受上游原样，不改（改了就成为要长期重放的产品补丁）。自建 Lite 制品的发布清单须附 `THIRD_PARTY_NOTICES.md` 与 `licenses/`，并按实际构建依赖核全声明。
3. **anydoc 例外**：anydoc 的 `cargo audit` 因 zip 8.6.0（RUSTSEC-2026-0367/0368/0369）失败。该问题在 v0.8.2 已存在；anydoc 0.2.4 只在内存里用 `ZipArchive::new(Cursor)` 加 `by_name` 读取，不 extract、不设权限。David 同意 #44 按例外合并。跟进 iDoris-ai/WeKnora#45（首选 cherry-pick 上游 `345855b`，把 zip 升到 9）；#45 若落地，会成为第一个产品补丁，届时在上表登记。
4. **修正评估文档的前提**：`BASELINE-EVAL-v0.8.3.md`「若升级的必做清单」第 3 条以 KB-CT-02b 为契约回归的前提，这一前提不成立。KB-CT-02b（Agent24#1012）只交付 TS/Rust 对手写 JSON 夹具的校验，不对 WeKnora 服务端运行，证明不了上游行为。所以升级后要在 v0.8.3 受测栈上全量重跑 PR② 的 16 项探测（随后续 PR 提交）。
5. **后续**：在 v0.8.3 受测栈上重跑探测并提交证据；能力矩阵换 SHA 重核（28 处内容变化的引用）。
