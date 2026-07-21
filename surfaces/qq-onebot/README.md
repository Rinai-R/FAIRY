# FAIRY ZeroBot QQ 群 Surface

这是一个依赖 ZeroBot v1.8.2 的独立 QQ 群 Surface，适用于已由 LLOneBot 登录并托管 QQ 登录态的实例。ZeroBot 负责 OneBot 11 正向 WebSocket、事件/消息段解析和 action 调用；FAIRY 只负责 allowlist、触发策略、队列与 Core turn 编排，不负责扫码、登录或好友/群管理。

当前交付以独立进程运行。`serve` 只表示运行 QQ Surface 进程，不会启动额外 HTTP server；进程只向 FAIRY Core 发起 HTTP/SSE 请求。

## 配置

所有配置都来自精确环境变量；token 不接受 flag、默认值或自动 trim：

```sh
export FAIRY_CORE_ENDPOINT=http://127.0.0.1:8787
export FAIRY_CORE_TOKEN='从 Core 配置取得的 token'
export FAIRY_ONEBOT_ENDPOINT=ws://127.0.0.1:3001
export FAIRY_ONEBOT_TOKEN='LLOneBot 正向 WS token'
export FAIRY_ONEBOT_SELF_ID='机器人 QQ 号'
export FAIRY_ONEBOT_GROUP_ALLOWLIST='群号1,群号2'
```

远程 Core 必须使用 `https://`，远程 OneBot 必须使用 `wss://`；本机回环地址可以使用 HTTP/WS。allowlist 必须非空。

## LLOneBot 操作顺序

1. 在 LLOneBot 中完成 QQ 扫码和登录，启用 OneBot 11 正向 WebSocket，并设置 token 与监听地址。
2. 确认 FAIRY Core 已启动、`FAIRY_CORE_ENDPOINT` 可访问，并使用 Core token。
3. 先运行 `fairy-qq-onebot doctor`，确认 Core 与 OneBot 握手都通过。
4. 将目标群加入 allowlist 后运行 `fairy-qq-onebot serve`。
5. Surface 默认只响应 @机器人或回复机器人消息；同群按 FIFO 处理，不同群可以并行。Surface 不会输出默认道歉文本，也不会把 QQ 群内容写入个人记忆。

ZeroBot 独占 OneBot 连接与重连；FAIRY 不叠加第二套 WebSocket client/reconnect loop。停止进程会取消群队列和进行中的 Core turn，并通过进程退出终止 ZeroBot driver。action 失败不会重试，避免重复发送。
