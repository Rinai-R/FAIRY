# 故障排查

## Desktop 无法启动

- 确认用户数据目录可写，且没有第二个实例占用写锁。
- 确认 `libseekdb` 已随包放入 `Contents/Frameworks`，不要在启动时下载，也不要设置 `FAIRY_SEEKDB_BINARY`。
- 日志和 Trace 在管理工作区中查看。启动失败应 fail closed，而不是回退到 PostgreSQL 或 SeekDB 子进程。

## 第三方聊天模型不可用

- 确认已经保存 provider 协议、HTTP(S) base URL、模型名、上下文窗口、认证模式和 credential；不要把 `/responses` 或 `/chat/completions` 填进 base URL。
- 严格 Desktop 不读取环境 endpoint、proxy 或 credential，也不会尝试本地模型守护进程或备用 provider。
- Provider 超时、取消、重定向越界、响应格式错误或容量超限都会明确失败；失败不应产生半截最终回复。
- 不要在日志、截图或 issue 中粘贴 API Key、Authorization header、提示词正文或工具参数。

## Semantic embedding 不可用

- 确认独立保存了第三方 embedding endpoint、模型和 credential，且 provider 返回恰好 1024 维有限向量。
- 聊天模型配置与 embedding 配置是两个 authority；即使同一供应商也不能共用隐式 credential 或用聊天输出代替向量。
- 旧 512 维设置必须重新配置。FAIRY 不下载本地 embedding 权重，不生成零向量，也不切换其他 provider。
- Provider 失败时检查管理状态中的脱敏 reason；原子写入应保持未提交，已有本地数据仍可读取。

## 搜索不可用

需要该工具的调用会返回明确错误。确认保存的 OpenSERP origin 可达且支持所需 search/fetch endpoint；修改 origin 后重启 Desktop。严格 profile 使用原生 OpenSERP adapter，不安装搜索插件，不读取 `FAIRY_OPENSERP_URL`，也不会直接抓取搜索结果 URL。桌面对话与本地历史不依赖 OpenSERP。

## 非严格 QQ 不回复

- 确认当前运行的是显式非严格 profile；严格端侧 profile 固定返回 `disabled_by_endpoint_strict`，不会读取 OneBot credential 或启动 bridge。
- 群不在 allowlist（空列表拒绝全部群）。
- 插件未授权 ingress/HTTP 或 OneBot 凭据句柄缺失。
- 插件 trap 只使该实例 degraded；本地桌面对话应仍可用。

不要在日志、截图或 issue 中粘贴 OneBot token、LLBot 授权或 master key。
