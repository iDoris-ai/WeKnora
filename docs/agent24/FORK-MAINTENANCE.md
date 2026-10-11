# WeKnora fork：分支角色与上游维护

本仓库是 `iDoris-ai/WeKnora`，上游为 `Tencent/WeKnora`。本次只建立仓库、文档与同步基础设施，不启动知识产品实现或恢复 Agent24 暂停路线。

## 三个长期分支

| 分支 | 角色 | 写入原则 |
|---|---|---|
| `upstream-release` | 与上游最新非 draft、非 prerelease 的正式 Release 所指 commit 一致 | 纯镜像；不添加我们的文档、补丁或 merge commit |
| `upstream-main` | 与上游开发分支 `main` 的 commit 一致 | 纯镜像；不添加产品改动 |
| `a24/develop` | 从正式 Release 起步的二次开发集成分支；本仓库默认分支 | 保存 Agent24 文档、适配和 feature；通过正常 merge 吸收正式版 |

初始正式版为 `v0.8.2`；完整初始 SHA 见 `UPSTREAM-BASELINE.json`。该文件是初始化快照，不是实时状态。研究方案里的旧 main SHA 保留为当时证据，不能混称产品发布基线。

## 同步规则

`.github/workflows/agent24-upstream-sync.yml` 每六小时检查，也可手动运行：

```bash
gh workflow run agent24-upstream-sync.yml -R iDoris-ai/WeKnora --ref a24/develop
```

1. 查询上游正式 Release 和 main；先确认两条镜像均可快进，再 atomic push，使镜像指向上游 commit，不创建额外提交。
2. 产品分支尚未包含正式版时，创建 `upstream-release → a24/develop` 同步 PR；已有 PR 则复用。开发版仅跟踪，不自动进入产品。
3. 同步 PR 必须完成适配/权限/取消/引用/迁移/恢复检查及评审后正常 merge。工作流没有自动批准或合并权限行为。GitHub Actions 创建的 PR 不保证触发 PR CI，应显式运行适用检查；未执行不算通过。
4. 非快进、改写 tag、冲突、权限不足或上游 API 错误均失败停止，查看 Actions 日志处理；不 reset、强推、rebase 已共享历史或把上游文件整树覆盖到产品分支。六小时是检查周期，不是实时同步保证；失败期间旧版本仍保留。

镜像随上游移动，同步 PR 的 head 也可能移动；评审/验收必须针对当前精确 head，不能复用旧批准。长期改动不得放在镜像分支。产品开发用短期 task 分支，PR base 为 `a24/develop`；上游镜像不是 task PR 的 base。

## 防止覆盖我们的 feature

正常 merge 保留双方历史，但不能证明行为无回归。每项修改登记到 `PATCH-LEDGER.md`，写目的、涉及路径、上游扩展点和测试。冲突逐项人工解决；不批量选择 ours/theirs。数据库升级前做一致性备份和恢复演练；不能只退回二进制就称可回滚。

UI 在 fork 的 Vue 前端开发；Agent24 只放宿主壳、受限 IPC、provider、profile 和 Sidecar 托管。对外契约、来源修订与权限字段经双方确认再实现。个人 Lite 与组织标准栈共用代码和接口，不另造永久分叉。保留上游许可证，新增代码/制品另核依赖义务。

## 首个提交与范围

- `WEKNORA-INTEGRATION-PROPOSAL.md`：当前总体方案及分发、切换、Sidecar/界面嵌入补充。
- `FORK-MAINTENANCE.md` / `UPSTREAM-BASELINE.json`：分支职责、更新与初始证据。
- `PATCH-LEDGER.md`：补丁/扩展登记起点。
- `agent24-upstream-sync.yml`：定时镜像快进和同步 PR 提议；不是产品 feature。

提交作者为 `jhfnetboy <jhfnetboy@gmail.com>`；提交与 PR 描述使用中文。上游同步 PR 不绕过任何后续设置的 required CI / 外部评审门。
