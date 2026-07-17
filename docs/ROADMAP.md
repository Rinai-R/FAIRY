# FAIRY 技术路线图

FAIRY 的唯一作用是作为**真实存在的人类式聊天伴侣**：像一个有性格、有记忆、会接话的人待在桌面上，而不是 Desktop Agent，也不是 coding / 办公 / 文档处理工具。

当前版本聚焦「自然实时文本对话 + 角色视觉」。以下为后续演进，按优先级排列。

## 非目标（明确不做）

- Desktop Agent、屏幕操控、电脑操作工具调用。
- coding 助手、办公自动化、文档处理主线。
- 把 Companion Respond 伪装成已具备工具/agent 能力。

这些能力即使未来出现独立实验，也不得默认并入聊天主线，也不得写进本路线图的交付目标。

## P0：对话 workflow → 角色决策 / 认知 gather loop

**目标**：用有限状态机组织伴侣回合；在对外回复前，允许白名单**理解工具**有限 gather（记忆检索等），再输出角色台词。不引入 Desktop Agent / 行动类工具 ReAct。

**当前状态（2026-07）：已实现（含 gather MVP）。**

- Wire FSM：`interpreting → gathering → planning → responding → completed`。
- `gathering`：初始记忆 Retrieve；`planning`：模型可至多一次请求 `memory.search` 再检索，然后强制 chains-only 回复。
- 五轴角色决策仍在 prompt 内私想，不输出 decision；最终仍是 strict JSON reply chains。
- **理解工具**（可进主对话）：`memory.search`（FTS）；可选 `web.search`（本机 OpenSERP sidecar，默认关）；后续可接向量 KNN / 受控 knowledge（P3），仍须白名单 + 步数上限。
- **行动工具**（禁止进聊天主线）：桌面操控、shell、改文件、办公自动化、通用 tool calling。
- 中间 gather / tool trace 不对用户可见，不进入 transcript。
- 近况查询：安装包或 `{configRoot}/bin/openserp` 提供二进制；不用 Docker；用户只需开关「允许查近况」。

实现与验收见 `openspec/changes/add-character-decision-agent-loop/` 与 `openspec/changes/add-companion-cognition-gather-loop/`。

## P1：火山引擎语音

**目标**：接入火山引擎语音，增强「真人在说话」的存在感。

- 优先 TTS（文本 → 语音播放）。
- ASR、VAD、打断、口型同步可后置，且必须保持独立 Speech 边界（不把音频链路塞进 Respond 文本协议里假成功）。

## P2：实时主动互动

**目标**：用户在活动、且 FAIRY 前台活跃时，按间隔触发互动策略（例如突然说一句引起互动）。

- **默认感知：** 可读轻量上下文（如前台 App 名、前台活跃、时间节奏、近期对话）。
- **可配置开放程度：** 用户可调低（几乎不感知）或按档位提高；更高档位必须另开 change 定义边界。
- **默认不得：** 屏幕全文读取、文档内容处理、电脑操控、办公自动化。
- 互动话术必须仍像该角色本人，而不是系统通知。

## P3：记忆 / 知识库 / skills / MCP（可选升级）

**目标**：提供可选接入，而不是替换默认本地栈。

- **默认：** 继续 SQLite + FTS5 trigram + 本地轻量路径（与当前实现一致）。
- **可选外接：** mem0、独立向量数据库、skills、MCP 等。
- 未配置可选后端时，不得伪造成功或静默降级后假装已是向量检索。
- 优先级低于 P0–P2；不阻塞语音与主动互动。

### P3a：语义向量检索（sqlite-vec，优先本地升级路径）

**目标**：从 trigram 关键词匹配升级为语义向量检索，仍落在 SQLite 生态内，作为默认栈的增强，而不是强制外置向量库。

- 引入 `sqlite-vec`（Mozilla 赞助，纯 C、零依赖 SQLite 向量扩展）。
  - 生产路径为 Go：通过 cgo / 静态链接加载扩展（`sqlite3_auto_extension` 或等价注册），**不以** Rust `cargo add sqlite-vec` 为生产绑定。
  - `vec0` 虚拟表存储向量，与现有 FTS5 trigram 并行。
- Embedding 生成：
  - **第一阶段**：复用用户已配置的模型 API 做 embedding（零额外本地模型）。
  - **第二阶段**：引入本地轻量 ONNX 模型（如 `bge-small-zh`，约 96MB）做离线能力；Go 侧选用合适的 ONNX Runtime 绑定（不把旧 Rust `ort` crate 当作生产依赖）。
- 检索策略：FTS5 粗筛 → 向量 KNN 精排，或双路并行再融合；缺 embedding / 扩展加载失败必须显式错误，不得假装仍在做语义检索。

## P4：IM 通讯探索（飞书 / Telegram / QQ）

**目标**：探索让角色出现在外部 IM 中与他人聊天的可能性。

- 身份、鉴权、多会话隔离、滥用与隐私边界风险高。
- 先研究与小范围原型，不进入近期主线，不挡 P0–P2。
- 未完成边界前，不得把外部 IM 流量直接灌进现有 Companion session 并假装安全。

## 当前主线完善（持续）

- 修复缺陷、提升对话质量与稳定性。
- 完善角色配置、会话管理与桌宠体验。
- 上述工作与 P0 可并行，但不改变「人类式聊天」定位。
