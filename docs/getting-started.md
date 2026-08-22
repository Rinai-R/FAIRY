# 首次启动

Desktop 是唯一生产 runtime。SeekDB、Core、Session 和默认无权限的 WASM Host 都在 FAIRY 主进程内运行。App 不启动本地 LLM、embedding 服务、数据库子进程或其他辅助进程。

1. 按 `desktop/Taskfile.yml` 的 `package` 或 `build` 得到应用。
2. 在干净用户目录启动。进程会创建数据目录、`0600` master key 和 SeekDB schema。
3. 通过 Desktop 管理边界保存第三方聊天 provider：选择 `responses` 或 `chat_completions` 协议，填写 HTTP(S) base URL、模型名、上下文窗口、认证模式和 credential。Endpoint 必须是 base URL，不要填写 `/responses`、`/chat/completions` 或 `/embeddings` 资源路径。
4. 独立保存第三方 semantic embedding provider：填写 HTTP(S) base URL、embedding 模型和 credential。该 provider 必须返回恰好 1024 维有限向量；聊天模型配置不能代替 embedding 配置。
5. 可选保存 OpenSERP origin。严格 profile 的搜索和公开网页正文只连接这个 origin；FAIRY 不直接访问搜索结果 URL。修改 origin 后重启 Desktop 以重新绑定唯一 Web authority。未配置或不可达时，Web 能力明确不可用，本地能力继续工作。
6. 打开伴侣窗口发送一条角色对话，确认回复、取消和重启后历史仍在。
7. 打开管理工作区查看日志、Trace 与指标；关闭管理工作区不会停止 runtime。

## 严格端侧边界

- 只允许已保存的聊天 provider origin、embedding provider origin 与 OpenSERP origin 出站；三者互不授予对方的网络能力。
- Credential 只保存在本地 SecretStore；配置、状态、日志、Trace、插件和 OpenSERP 请求不得回显 credential。
- 严格 profile 不读取环境 endpoint、proxy 或 credential。不要设置 `FAIRY_DATABASE_URL`、`FAIRY_CORE_ENDPOINT`、`FAIRY_OPENSERP_URL`、`FAIRY_SEEKDB_BINARY` 或代理环境变量来配置正式 Desktop。
- SeekDB 以进程内共享库运行；缺少或校验失败的 `libseekdb` 会阻止启动，不会回退到子进程、PostgreSQL 或外部数据库。
- App 不携带本地模型 engine、weights 或 tokenizer，也不从公网下载替代物。

## 断网与局部故障

- 聊天 provider 不可用：不能生成新回复，但本地历史、数据、配置、备份、观测和管理仍可用。
- Embedding provider 不可用：需要新向量的写入或维护按原子合同失败，已有本地数据不会被零向量或其他 provider 污染；已有 text-only 路径按各领域合同继续工作。
- OpenSERP 不可用：只禁用搜索和公开网页学习，不影响本地对话历史与管理。
- 三个外部能力均不可用：FAIRY 仍可启动并打开本地管理边界，但不会伪造模型结果或自动启动本地模型。

QQ / OneBot 只在 `full` 或 `desktop-lite` 等非严格 profile 中显式启用，需要独立 OneBot 服务，因此不属于严格端侧发行。见 `docs/qq-onebot.md`。
