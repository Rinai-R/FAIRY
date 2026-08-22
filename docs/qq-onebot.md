# QQ / OneBot 迁移说明

QQ / OneBot 需要额外协议服务、网络入口和 credential，因此不属于 `endpoint-strict` 零依赖发行边界。严格 profile 不读取、不修改已保存的 QQ 实例，也不启动 bridge、poller、listener 或 OneBot outbound；管理状态固定为 `disabled_by_endpoint_strict`。

以下说明只适用于用户显式选择的 `full` 或 `desktop-lite` 等非严格扩展。独立 `fairy-qq-onebot` Surface、Dockerfile 和 Compose overlay 已删除；非严格 QQ 接入由 Desktop 的插件 Host 管理。

## 参考实现

协议解析、reply reference、发送回执与 allowlist 规范化位于：

- `fairy/plugin/qqonebot/`
- 插件清单：`fairy/plugin/qqonebot/manifest.json`
- Host 事件队列与 Edge 轮询：`fairy/runtime/wasm/eventqueue.go`、`fairy/app/edge/qq_bridge.go`

插件不得导入 Core 内部包或 `fairy/transport/session`。仓库中的 Go testhost 只用于开发和合同测试，不是严格正式包中的可执行插件；非严格发行若要安装该插件，必须提供通过 release inventory 校验的真实 `.fairy-plugin` / `module.wasm`。

## 安装与配置

1. 明确选择非严格 profile；严格 profile 不提供启用入口。
2. 在 Desktop 管理工作区的「插件」任务中安装并启用经过校验的 `fairy.plugin.qq-onebot` 包。
3. 授权 `http.request`（OneBot API 的 loopback host/port）、`http.ingress`、`event.emit` 和 `action.complete`。默认无权限。
4. 在实例配置中保存群 allowlist 和 OneBot API URL。空 allowlist 拒绝全部群。
5. OneBot access token 只通过不回显的凭据句柄 `onebot` 写入；管理工作区不会展示明文。
6. 将 OneBot 实现的 HTTP POST 指向插件 ingress（仅 loopback）。不要启动独立 FAIRY QQ 进程或 Core Compose 服务。

旧 Compose 环境变量（`FAIRY_CORE_ENDPOINT`、`FAIRY_CORE_TOKEN`、`FAIRY_ONEBOT_*`、`FAIRY_ONEBOT_CONTAINER_NETWORK`）不再读取。endpoint 与凭据必须在插件安装时重新确认。
