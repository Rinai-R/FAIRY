# 项目 Agent 规则

本项目使用 [Fission-AI/OpenSpec](https://github.com/Fission-AI/OpenSpec) 官方 OPSX 工作流管理规格驱动变更。

## OpenSpec 工作流

`.agents/skills/openspec-*` 是工作流的权威指令。处理 OpenSpec 操作时，必须读取并遵循对应 Skill，不要在本文件中另建阶段门或重复实现工作流。

默认 `core` profile 包含以下操作：

| 操作 | Skill | 用途 |
|---|---|---|
| 探索 | `openspec-explore` | 在不创建 artifact、不实现代码的前提下调查问题和澄清方向 |
| 提案 | `openspec-propose` | 一次创建 proposal、增量 specs、design 和 tasks |
| 应用 | `openspec-apply-change` | 按 tasks 实现变更并更新任务状态 |
| 更新 | `openspec-update-change` | 根据新信息修订已有 change artifact |
| 同步 | `openspec-sync-specs` | 将 change 中的增量规格同步到主规格 |
| 归档 | `openspec-archive-change` | 验证完成状态并归档 change |

工作原则：

- 需求尚不明确时先使用 `openspec-explore`；目标清楚时可直接使用 `openspec-propose`。
- 实现前阅读 proposal、specs、design 和 tasks，确认待实现内容与 artifact 一致。
- 实现中发现范围、行为或设计需要变化时，使用 `openspec-update-change` 更新 artifact，保持规格与代码同步。
- 完成实现后按 Skill 指令验证，再同步规格并归档。
- OpenSpec 采用可迭代 artifact 工作流，不添加固定阶段、复杂度分级或额外确认门禁。
- 以 `openspec/config.yaml` 的 schema 和项目上下文为准；需要了解 CLI 状态时运行 `openspec status` 或 `openspec list`。

## 文档语言

除非用户明确要求英文，AI 新建或更新的项目文档使用简体中文。OpenSpec 固定术语、命令、代码符号、文件路径以及 Requirement、Scenario、SHALL、MUST、WHEN、THEN 等规范关键词可以保留英文。

## Git 策略

默认不得创建 git commit。只有用户明确要求提交、commit 或指定 commit message 时，才允许执行提交。

可以执行只读或验证操作，例如：

- `git status`
- `git diff`
- `git diff --check`
- 项目的测试、构建、lint 和类型检查命令

不得擅自执行 `git push`、`git reset --hard`、`git rebase` 等会改变历史或远端状态的操作。

## 项目级 Skills

`.agents/skills/` 同时包含 OpenSpec 工作流 Skills 和项目级专业 Skills。OpenSpec 更新只应管理 `openspec-*` 目录，不得覆盖或删除其他 Skill。

专业 Skill 的来源与锁定版本记录在 `.agents/skills.lock.json`。升级专业 Skill 时应按仓库逐项更新并验证，不要手工混合多个上游版本。
