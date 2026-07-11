# FAIRY

FAIRY 是一个以角色存在感为核心的桌面情感陪伴应用：透明桌宠负责“在场”，折叠聊天卡负责短而自然的交流，Rust Harness 负责角色推断、网络搜索、个人记忆与知识检索。

它不是 Galgame、剧情生成器或 coding agent，也不接入外部 agent 适配器。

## 当前能力

- Tauri 2 + React/Vite + Rust workspace，当前主要验证 macOS。
- 可拖动透明桌宠、置顶、托盘恢复和受保护的点击穿透。
- 可拔插角色简介与用户偏好称呼。
- OpenAI-compatible Responses / Chat Completions，协议由用户显式选择。
- 普通对话默认一句短回复；详细请求仍只产生一条助手消息。
- 独立 `SpeechText`：清理全句中的独立填充词，只包含第一条纯净、可朗读语义句，为后续 TTS 保留稳定边界。
- 单一 Brave Search 工具、最多两次调用、最多五个可折叠来源；没有 Provider fallback。
- SQLite schema v2 将个人记忆与知识分层：网页有据的事实可直接验证，无来源事实先进入候选区，只有用户明确确认后才参与 FTS5 trigram 检索；后台提取任务始终可审计。
- KV-cache 友好的 `respond` / `compact` / `extract` 独立稳定 lane。

尚未实现真实 TTS、ASR、Live2D、口型同步、主动对话和云同步；相关调用不得伪造成功。

## 开发

环境版本以根目录 `package.json` 和 Rust toolchain 为准。

```bash
rtk pnpm install --frozen-lockfile
rtk pnpm test
rtk pnpm build
rtk cargo fmt --all -- --check
rtk cargo clippy --workspace --all-targets --all-features -- -D warnings
rtk cargo test --workspace --all-targets
rtk pnpm tauri dev
```

## 配置与数据边界

- 模型 Bearer Key 只进入系统 Keychain service `com.rinai.fairy.model`。
- Brave API Key 只进入独立 Keychain service `com.rinai.fairy.search`。
- 脱敏连接配置、角色和用户称呼保存在应用配置目录。
- SQLite 智能层保存在应用配置目录的 `intelligence/fairy.sqlite3`。
- 设置页分别展示最多 20 条候选知识与已验证知识；确认和移除都是显式操作，移除保留 tombstone、来源与审计记录。
- 角色简介、称呼、用户消息、检索上下文与必要对话历史会发送给用户配置的模型服务。
- 搜索 query 会发送给 Brave；返回的标题、摘要和 URL 按不可信引用数据处理。
- API Key、Authorization header、完整外部响应和模型 hidden reasoning 不得进入配置、日志或智能层记录。

## 项目文档

- [项目上下文](openspec/project.md)
- [当前变更提案](openspec/changes/add-companion-intelligence-and-speech-contract/proposal.md)
- [实现设计](openspec/changes/add-companion-intelligence-and-speech-contract/design.md)
- [知识确认与语音规范化增量](openspec/changes/manage-confirmed-knowledge-and-speech-normalization/proposal.md)

`web/public/characters/atri/atri-official.png` 不属于 FAIRY 项目许可，仅用于个人非商业原型；使用限制见同目录 `NOTICE.md`。
