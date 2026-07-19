# FAIRY 技术路线图

FAIRY 的唯一作用是作为**真实存在的人类式聊天伴侣**：像一个有性格、有记忆、会接话的人待在桌面上，而不是 Desktop Agent，也不是 coding / 办公 / 文档处理工具。

当前版本聚焦「自然实时文本对话 + 角色视觉」。以下为后续演进，按优先级排列。

## 非目标（明确不做）

- Desktop Agent、屏幕操控、电脑操作工具调用。
- coding 助手、办公自动化、文档处理主线。
- 把 Companion Respond 伪装成已具备工具/agent 能力。
- 把模型 CoT / `reasoning_content` 直接播给用户。
- 继续押注「快模型乖乖吐 3–5 条 reply chains」作为多段表演机制。

这些能力即使未来出现独立实验，也不得默认并入聊天主线，也不得写进本路线图的交付目标。

## P0：对话 workflow → 原生 tool call 检索

**目标**：用有限状态机组织伴侣回合；在对外回复前，允许白名单**理解工具**以 provider 原生 function call 拉取记忆 / 公开资料，再输出角色台词。不引入 Desktop Agent / 行动类工具 ReAct。

**当前状态（2026-07）：已实现（tool call 取代 gather JSON）。**

- Wire FSM：`interpreting → gathering → planning → responding → completed`。
- `gathering`：兼容脉冲，**不再**自动记忆初检索；`planning`：挂载 `memory_search`（及可选 `web_search`）function tools，每轮成功 tool ≤2，然后 chains-only 回复。
- 五轴角色决策仍在 prompt 内私想，不输出 decision；最终仍是 strict JSON reply chains。
- **理解工具**（可进主对话）：`memory_search`（FTS）；可选 `web_search`（本机 OpenSERP sidecar，默认开）；后续可接向量 KNN / 受控 knowledge（P3），仍须白名单 + 步数上限。
- **行动工具**（禁止进聊天主线）：桌面操控、shell、改文件、办公自动化、任意扩展 tool。
- 中间 tool trace 不对用户可见，不进入 transcript。
- 公开资料检索：`{configRoot}/bin/openserp` 本机二进制，不用 Docker；默认开启「允许检索公开资料」，可关。

实现与验收见 `openspec/changes/replace-gather-json-with-tool-calling/`（取代 gather JSON 路径）。

## P1：火山引擎语音

**目标**：接入火山引擎语音，增强「真人在说话」的存在感。

**当前状态（2026-07）：声音复刻 HTTP TTS 已接入；ASR/VAD/打断后置。**

- 优先 TTS（文本 → 语音播放）。
- ASR、VAD、打断、口型同步可后置，且必须保持独立 Speech 边界（不把音频链路塞进 Respond 文本协议里假成功）。

## P1.5：Progressive Utterance Bus（应用层多段回复）

**目标**：在有界 tool / plan step 之间非阻塞吐出角色口吻短句，形成「先应一声 → 再查 → 再答」的多段体感；**不再依赖**模型一次输出多条 `reply_chain`。

**锁定决策：**

- 多段回复主路径 = Progressive Utterance Bus，不是「把 reply chains 调到模型听话」。
- 每轮最终 Respond 默认 **1 条 chain**（展示 + 情绪）；多段来自 step 级 `utterance` 事件。
- Utterance 与下一拍 model/tool 调用**并发**：发射不阻塞主循环；打字机 / TTS 走队列，可取消。
- 中间 utterance **仅事件流**，默认不入 transcript；最终一句入档，避免记忆污染。
- Tool trace 仍不对用户可见；utterance 禁止泄露工具名 / 检索原文 / CoT。
- 文案第一拍：规则模板 + 可选极短 `utterance` micro-lane；禁止再跑完整 Respond。

**与旧多 chain 关系：** reply chains 的「多段表演」产品承诺退役；compiler / prompt 收敛为单最终句。

实现见 `openspec/changes/add-progressive-utterance-bus/`。

## P2：实时主动互动

**目标**：用户在活动、且 FAIRY 前台活跃时，按间隔触发互动策略（例如突然说一句引起互动）。

- **默认感知：** 可读轻量上下文（如前台 App 名、前台活跃、时间节奏、近期对话）。
- **可配置开放程度：** 用户可调低（几乎不感知）或按档位提高；更高档位必须另开 change 定义边界。
- **默认不得：** 屏幕全文读取、文档内容处理、电脑操控、办公自动化。
- 互动话术必须仍像该角色本人，而不是系统通知。
- **必须复用 P1.5 utterance 总线**，不得再开第三套说话通道。

## P3：记忆 / 知识库 / skills / MCP（可选升级）

**目标**：提供可选接入，而不是替换默认本地栈。

- **默认：** 继续 SQLite + FTS5 trigram + 本地轻量路径（与当前实现一致）；语义能力由 P3a 增强。
- **可选外接：** mem0、独立向量数据库、skills、MCP 等。
- 未配置可选后端时，不得伪造成功或静默降级后假装已是向量检索。
- 优先级低于 P0–P1.5；不阻塞语音与多段回复主线。

### P3a：语义向量检索（sqlite-vec + 本地轻量 embedding）

**目标**：从 trigram 关键词匹配升级为语义向量检索，仍落在 SQLite 生态内，作为默认栈的增强，而不是强制外置向量库。

- 引入 `sqlite-vec`（Mozilla 赞助，纯 C、零依赖 SQLite 向量扩展）。
  - 生产路径为 Go：当前通过 `modernc.org/sqlite` + `modernc.org/sqlite/vec` 在同一 `sqlite` driver 上注册 `vec0`；不引入 Rust 生产绑定，也不做 silently no-op。
  - `vec0` 虚拟表存储向量，与现有 FTS5 trigram 并行。
- Embedding 生成：
  - **默认优先：本地轻量 ONNX 模型**（如 `bge-small-zh` 或同等体量）做离线能力；Go 侧选用合适的 ONNX Runtime 绑定。
  - **可选：复用用户已配置的模型 API** 做 embedding，仅作加速 / 回填，不是第一阶段必经。
  - 首次下载 / 模型缺失 / 扩展加载失败必须显式状态；当前已有只读模型状态检查：缺少 `intelligence/embeddings/bge-small-zh-v1.5/model.onnx` 会报告 `missing/unavailable`；检索时无向量则只走 FTS，并标记 `semantic=unavailable`，不得假装语义检索。
- 写入：memory create/supersede、knowledge 确认时先生成 `pending` embedding job；当前已有显式 `ProcessEmbeddingJobs` 注入式 worker 可把 ready embedder 输出写入 vec0，并且语义状态可只读报告 pending/running/failed jobs、embedded items 和 vector rows；但尚未接入 ONNX 或后台自启动。
- 检索策略：当前已有显式 `RetrieveWithSemantic` 可在注入 ready query embedder 后执行 FTS ∪ KNN 融合（固定公式）；默认 `memory_search` 仍保持 FTS-only，待 composition root 注入真实本地 embedder 后启用语义检索，tool limit 保持现有量级。

实现见 `openspec/changes/add-sqlite-vec-local-embeddings/`。

## P4：IM 通讯探索（飞书 / Telegram / QQ）

**目标**：探索让角色出现在外部 IM 中与他人聊天的可能性。

- 身份、鉴权、多会话隔离、滥用与隐私边界风险高。
- 先研究与小范围原型，不进入近期主线，不挡 P0–P1.5。
- 未完成边界前，不得把外部 IM 流量直接灌进现有 Companion session 并假装安全。

## 当前主线完善（持续）

- 修复缺陷、提升对话质量与稳定性。
- 完善角色配置、会话管理与桌宠体验。
- 上述工作与 P0 / P1.5 可并行，但不改变「人类式聊天」定位。
