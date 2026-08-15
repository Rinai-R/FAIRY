# QQ / OneBot 迁移说明

独立 `fairy-qq-onebot` Surface、Dockerfile 和 Compose overlay 已删除。QQ 接入现在是 Desktop 托管的 `fairy.plugin.qq-onebot` 插件。

## 参考实现

协议解析、reply reference、发送回执与 allowlist 规范化位于：

- `fairy/plugin/qqonebot/`
- 插件清单：`fairy/plugin/qqonebot/manifest.json`
- Host 事件队列与 Edge 轮询：`fairy/runtime/wasm/eventqueue.go`、`fairy/app/edge/qq_bridge.go`

插件不得导入 Core 内部包或 `fairy/transport/session`。官方实现以 Go testhost handler 运行，通过 Host `http.request` / `event.emit` / `action.complete` 访问网络和回执。

## 安装与配置

1. 在 Desktop 管理工作区的「插件」任务中安装并启用 `fairy.plugin.qq-onebot`。
2. 授权 `http.request`（OneBot API 的 loopback host/port）、`http.ingress`、`event.emit` 和 `action.complete`。默认无权限。
3. 在实例配置中保存群 allowlist 和 OneBot API URL。空 allowlist 拒绝全部群。
4. OneBot access token 只通过不回显的凭据句柄 `onebot` 写入；管理工作区不会展示明文。
5. 将 OneBot 实现的 HTTP POST 指向插件 ingress（仅 loopback）。不要启动独立 FAIRY QQ 进程或 Core Compose 服务。

旧 Compose 环境变量（`FAIRY_CORE_ENDPOINT`、`FAIRY_CORE_TOKEN`、`FAIRY_ONEBOT_*`、`FAIRY_ONEBOT_CONTAINER_NETWORK`）不再读取。endpoint 与凭据必须在插件安装时重新确认。
