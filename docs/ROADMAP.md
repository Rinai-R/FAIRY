# FAIRY 技术路线图

当前版本聚焦「自然实时文本对话 + 角色视觉」主线。以下为后续演进方向，按优先级排列。

## P0：主线完善（当前）

- 修复现有缺陷、提升对话质量和稳定性。
- 完善角色配置体验和会话管理。

## P1：记忆系统升级 — 语义检索

**目标**：从 trigram 关键词匹配升级为语义向量检索。

- 引入 `sqlite-vec`（Mozilla 赞助，纯 C、零依赖 SQLite 向量扩展）。
  - 通过官方 Rust binding 静态链接（`cargo add sqlite-vec` + `sqlite3_auto_extension`）。
  - `vec0` 虚拟表存储向量，与现有 FTS5 trigram 并行。
- Embedding 生成：
  - **第一阶段**：复用用户已配置的模型 API 做 embedding（零额外模型）。
  - **第二阶段**：引入本地轻量 ONNX 模型（如 `bge-small-zh`，约 96MB）做离线 fallback，框架选用 `ort` crate。
- 检索策略：FTS5 粗筛 → 向量 KNN 精排，或双路并行取交集。

## P2：语音链路

- 麦克风采集 + VAD（语音活动检测）+ ASR（语音识别）。
- 完整 TTS（文本转语音）+ 口型同步。
- 语音打断机制。

## P3：角色渲染升级

- Live2D 运行时支持（保持与当前 `state_images` 单图状态运行时的边界隔离）。
- 更多角色视觉包和动画状态。

## P4：Desktop Agent

- 支持 work 模式，可以接入自定义 mcp、skills、知识库等进行轻量办公。