# WeKnora fork：分支角色与上游维护

本仓库是 `iDoris-ai/WeKnora`，上游为 `Tencent/WeKnora`。本次只建立仓库、文档与同步基础设施，不启动知识产品实现或恢复 Agent24 暂停路线。

## 三个长期分支

| 分支 | 角色 | 写入原则 |
|---|---|---|
| `upstream-release` | 与上游最新非 draft、非 prerelease 的正式 Release 所指 commit 一致 | 纯镜像；不添加我们的文档、补丁或 merge commit |
| `upstream-main` | 与上游开发分支 `main` 的 commit 一致 | 纯镜像；不添加产品改动 |
| `a24/develop` | 从正式 Release 起步的二次开发集成分支；本仓库默认分支 | 保存 Agent24 文档、适配和 feature；通过正常 merge 吸收正式版 |

初始正式版为 `v0.8.2`；完整初始 SHA 见 `UPSTREAM-BASELINE.json` 的 `initial_snapshot`。当前产品基线与必须先升级的时点见同一文件的 `current_baseline`（`upgrade_before.triggers`；重新评估的日期与触发另见 `reevaluate_by`、`review_triggers`）和 PATCH-LEDGER 开头；镜像的实时状态以 `git ls-remote` 为准。研究方案里的旧 main SHA 保留为当时证据，不能混称产品发布基线。

## 同步规则

`.github/workflows/agent24-upstream-sync.yml` 每六小时检查，也可手动运行：

```bash
gh workflow run agent24-upstream-sync.yml -R iDoris-ai/WeKnora --ref a24/develop
```

1. 查询上游正式 Release 和 main；先确认两条镜像均可快进，再 atomic push，使镜像指向上游 commit，不创建额外提交。
2. 产品分支尚未包含正式版时，开或更新一个由工作流创建的 `[upstream-sync]` issue，写明正式版 SHA 与评审步骤；**工作流不开同步 PR**。评审人审完后自己开 `upstream-release → a24/develop` 同步 PR；该 PR 打开期间不推进 `upstream-release`，合并后工作流关闭 issue。开发版仅跟踪，不自动进入产品。
3. 同步 PR 必须完成适配/权限/取消/引用/迁移/恢复检查及评审后正常 merge。工作流没有自动批准或合并权限行为。同步 PR 由评审人在审完上游对 `.github/` 的改动后才开，开的那一刻适用检查即以评审人身份运行；未执行或未通过不算完成。
4. 上游正式版 tag 名不是 `v` 开头的版本号形式（`^v[0-9]+(\.[0-9]+)*([-.][0-9A-Za-z.]+)?$`，例如 `v0.8.3`、`v1.0.0-rc.1`）时失败停止：tag 名会写进评审人照抄执行的命令，不能带 shell 元字符。非快进、改写 tag、冲突、权限不足或上游 API 错误均失败停止，查看 Actions 日志处理；不 reset、强推、rebase 已共享历史或把上游文件整树覆盖到产品分支。六小时是检查周期，不是实时同步保证；失败期间旧版本仍保留。

镜像随上游移动；同步 PR 打开期间它的 head 不会被同步工作流推进，但评审期间若没有打开的同步 PR，`upstream-release` 可能前进，所以 issue 的步骤要求先暂停同步工作流并核对镜像 SHA。评审/验收必须针对当前精确 head，不能复用旧批准。长期改动不得放在镜像分支。产品开发用短期 task 分支，PR base 为 `a24/develop`；上游镜像不是 task PR 的 base，除同步 PR 外也不要作为任何 PR 的 head（本仓库里以镜像为 head 的 PR 打开期间，同步工作流不推进该镜像）。

## 同步凭据

内置 `GITHUB_TOKEN` 无法获得 `workflows` 权限，推送改动了 `.github/workflows/` 的上游提交会被拒（2026-10-13 起的失败即此原因）。工作流因此**只在推镜像时**改用组织 App 的短期令牌；查询 PR 与开、改、关同步 issue 仍用内置 `GITHUB_TOKEN`（job 权限 `contents: read`、`pull-requests: read`、`issues: write`）。工作流不再开 PR，仓库设置 "Allow GitHub Actions to create and approve pull requests" 可以关闭，建议关闭。

- App：iDoris-ai 组织下的私有 GitHub App「iDoris WeKnora Sync」，只安装到本仓库。仓库权限仅 Contents RW、Workflows RW、Metadata R；不订阅 webhook，不申请组织或账号权限。
- 凭据只放在 Environment `upstream-sync`，其部署分支策略只允许 `a24/develop`：Environment variable `WEKNORA_SYNC_APP_CLIENT_ID` 存 App 的 Client ID，Environment secret `WEKNORA_SYNC_APP_PRIVATE_KEY` 存 App 私钥（PEM 全文）。不要放成仓库级或组织级 secret：镜像推送与评审后运行的同步 PR 检查都会运行上游工作流，它们能读到本仓库可见的全部仓库级与组织级 secret，但跑在其它 ref 上，进不了这个 Environment。
- 缺任一项时工作流第一步即失败，不回退到 `GITHUB_TOKEN`。每次运行用 `actions/create-github-app-token` 换取限定本仓库、Contents 与 Workflows 写权限的 1 小时令牌，任务结束自动吊销。
- 轮换：在 App 设置页先生成新私钥、更新 Environment secret、手动运行一次工作流确认成功，再删除旧私钥。撤销：删除全部私钥或卸载 App。

**为什么工作流不开同步 PR。** 同步 PR 与 base 同在本仓库，只要它处于打开状态，合并提交里的上游 `pull_request` / `pull_request_review*` 工作流就可能运行，并拿到写权限的 `GITHUB_TOKEN`（本仓库默认 write）和本仓库可见的 secret（例如组织级 `CLA_TOKEN`）。用 App 令牌开 PR 或推进它的 head 会直接触发；即使用内置 `GITHUB_TOKEN` 开（它自己产生的 `opened` 不触发），之后任何人或其它 App 打标签、指派、请求评审、编辑、评审或评论也都会触发，上游现有的 `cli-e2e.yml` 就是"打标签触发"。静态检查上游工作流挡不住所有写法，所以由流程保证：评审 `.github/` 之前不存在同步 PR。issue 事件只运行默认分支 `a24/develop` 上已评审的工作流。

评审人按 issue 的五步执行：先 `gh workflow disable` 暂停同步工作流并确认没有未完成的运行（含排队中的）；用 `git ls-remote` 确认 `upstream-release` 仍指向 issue 写的 SHA；用该 SHA 评审上游对 `.github/` 的改动，重点看会被 `pull_request` 类事件运行的工作流（job 未声明 `permissions` 时继承工作流级 `permissions`，两级都未声明才取本仓库默认的写权限；显式申请写权限、引用 secret、调用新的外部 action 都要逐个确认），确认无害前不开 PR；开同步 PR 并核对 head；最后 `gh workflow enable` 恢复。查询 PR 与推送之间还有几秒的窗口：若恰好有人此时开了以镜像为 head 的同仓 PR，推送会触发它的检查。能开同仓 PR 的人本身已有写权限，这不构成越权。

**推镜像前的守卫。** 用 App 令牌推送会产生 push 事件，运行被推送提交里的工作流定义。推送前由 `.github/scripts/agent24-mirror-trigger-guard.rb` 检查本次要推送的目标提交里的全部工作流（`.yml`/`.yaml`，不区分大小写），下列任一情况都失败停止、不推送：

- `push` 触发会匹配到镜像分支名：无分支过滤、只有 `paths` 过滤、`branches` 为空列表或其模式可能匹配，或 `branches-ignore` 没有确定排除它。模式按 GitHub 过滤语法解析 `*`、`**`、`?`、`+`、`[...]`（方括号只认字母数字与范围）。`**/` 可以匹配零层目录（cheat sheet 里 `**/README.md` 匹配 `README.md`）；其它与 `**` 相邻的 `/`（结尾的 `/**`、`***/`、`/**/**` 等）和大小写是否敏感，文档都没写。分支过滤是拿短名还是 `refs/heads/<name>` 全名去匹配，文档也没写。这些在 `branches` 里按较宽的解释（`/` 可省略、不区分大小写、短名和全名任一匹配都算，即会匹配），在 `branches-ignore` 里按较窄的解释（不算排除）；叠加量词、其它方括号内容、`\` 等无法识别的写法，在 `branches` 里按匹配、在 `branches-ignore` 里按不匹配处理。`branches` 里的 `!` 否定一律忽略（只会多拦）；`branches-ignore` 里只要出现 `!` 开头的模式，就按会触发处理；
- 出现 `create` 触发；
- 文件名或内容不是合法 UTF-8、无法解析、出现 YAML 合并键 `<<`（按反转义后的键值判断，`"<<"`、`"\x3c\x3c"` 等任何写法都算；Psych 会合并而 GitHub 不支持，读法不同就拦）、含多个 YAML 文档、同一映射里有重复键（含 `on` 与 `"on"`、`on` 与 `true` 这类解析后相同的键）或非普通标量的键、任何带标签的节点（如 `!!binary`）、工作流文件不是普通文件（目录、子模块、符号链接）、`.github` 或 `.github/workflows` 不是普通目录，或结构无法识别。

今天上游 main 与 v0.8.3 都能通过。触发时先确认上游改动的意图，再决定是否手动推送或调整守卫。修改守卫后运行 `bash .github/scripts/agent24-mirror-trigger-guard-test.sh`，全部用例须为 PASS。同步工作流每次运行都会先在 runner 上跑这个测试脚本（Self-test 步骤），任何用例失败就停止，不推镜像。`schedule`、`workflow_run`、`pull_request_target` 等事件使用默认分支 `a24/develop` 上已评审的定义，不在检查范围内。

## 防止覆盖我们的 feature

正常 merge 保留双方历史，但不能证明行为无回归。每项修改登记到 `PATCH-LEDGER.md`，写目的、涉及路径、上游扩展点和测试。冲突逐项人工解决；不批量选择 ours/theirs。数据库升级前做一致性备份和恢复演练；不能只退回二进制就称可回滚。

UI 在 fork 的 Vue 前端开发；Agent24 只放宿主壳、受限 IPC、provider、profile 和 Sidecar 托管。对外契约、来源修订与权限字段经双方确认再实现。个人 Lite 与组织标准栈共用代码和接口，不另造永久分叉。保留上游许可证，新增代码/制品另核依赖义务。

## 首个提交与范围

- `WEKNORA-INTEGRATION-PROPOSAL.md`：当前总体方案及分发、切换、Sidecar/界面嵌入补充。
- `FORK-MAINTENANCE.md` / `UPSTREAM-BASELINE.json`：分支职责、更新、初始证据与当前基线。
- `BASELINE-EVAL-v0.8.3.md`：v0.8.2 与 v0.8.3 的差异评估和基线决定。
- `PATCH-LEDGER.md`：补丁/扩展登记起点。
- `agent24-upstream-sync.yml`：定时镜像快进，并在有新正式版时开或更新同步 issue；不是产品 feature。

提交作者为 `jhfnetboy <jhfnetboy@gmail.com>`；提交与 PR 描述使用中文。上游同步 PR 不绕过任何后续设置的 required CI / 外部评审门。
