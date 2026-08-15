# 故障排查

## Desktop 无法启动

- 确认用户数据目录可写，且没有第二个实例占用写锁。
- 确认 SeekDB artifact 已随包校验；不要在启动时下载。
- 日志和 Trace 在管理工作区中查看。启动失败应 fail closed，而不是回退到 PostgreSQL。

## 搜索不可用

需要该工具的调用会返回明确错误。检查插件实例是否 ready、是否授予 `http.request`、以及 OpenSERP 兼容提供方是否可达。桌面对话不依赖搜索插件。

## QQ 不回复

- 群不在 allowlist（空列表拒绝全部群）。
- 插件未授权 ingress/HTTP 或 OneBot 凭据句柄缺失。
- 插件 trap 只使该实例 degraded；本地桌面对话应仍可用。

不要在日志、截图或 issue 中粘贴 OneBot token、LLBot 授权或 master key。
