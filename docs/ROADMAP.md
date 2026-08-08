# FAIRY RoadMap

当前阶段先把同一陪伴人格在私人桌面和公共社交场景中的核心体验跑通。

## 已落地

- [x] Session Core：显式 Agent Loop、可取消 Turn、白名单工具、节奏化回复、持久会话与分层记忆
- [x] 数据底座：PostgreSQL 17、GORM additive migration、pg_trgm + pgvector 单库混合召回与 OpenSERP 可选公开检索
- [x] Desktop Surface：Wails v3 桌宠、快捷输入、历史消息、coarse observation 与受控的模型按需主屏捕获
- [x] Web 控制台：角色、称呼、模型、表情包、智能、用量与可观测性管理
- [x] 公共社交陪伴：QQ 群聊接入，由 Core 判断是否自然参与
- [x] 表情包输出：人工语义元数据、Agent 工具、有序投递、Surface 回执与 Desktop 历史语义展示

## 下一阶段

- [ ] 收口真实运行质量：模型回复自然度、长会话稳定性、QQ operator 环境 smoke 与失败诊断
- [ ] 提升表情包语义质量；当前依赖人工描述与标签，不使用未经设计的 VLM/OCR 自动识别
- [ ] 丰富角色渲染；当前本地单图状态可用，Live2D 不阻塞聊天主线
- [ ] 评估飞书、Telegram 等其他 IM Surface
- [ ] skills、MCP、mem0 与多模型路由只作为显式可选能力另行设计，不引入隐藏 fallback

## 非目标

- Desktop Agent、coding 助手、办公自动化或任意电脑操作
- 默认持续录屏、无边界环境采集或把截图写入长期记忆
- 自动切换协议/模型、吞掉错误或用 fallback 伪造成功

欢迎直接提 issue 或改这份列表。
