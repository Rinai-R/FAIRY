# FAIRY ZeroBot QQ 群 Surface

这是一个拥有独立 `go.mod`、依赖 ZeroBot v1.8.2 的 QQ 群 Surface，适用于已由 LLOneBot 登录并托管 QQ 登录态的本机实例。ZeroBot 负责 OneBot 11 HTTP webhook listener、事件/消息段解析、`OnlyGroup` 触发和 HTTP action；FAIRY 负责群 allowlist、有界消息窗口、Core participation/session/turn 和 final beat 回发，不负责扫码、登录或好友/群管理。

当前交付以独立进程运行。`serve` 由 ZeroBot HTTP driver 启动本机事件回调 listener；FAIRY 不实现第二套 HTTP handler/server。进程同时向 LLOneBot HTTP API 和 FAIRY Core HTTP/SSE 发起请求。

## 配置

所有配置都来自精确环境变量；token 不接受 flag、默认值或自动 trim：

```sh
export FAIRY_CORE_ENDPOINT=http://127.0.0.1:8787
export FAIRY_CORE_TOKEN='从 Core 配置取得的 token'
export FAIRY_ONEBOT_WEBHOOK_ENDPOINT=http://127.0.0.1:3002
export FAIRY_ONEBOT_API_ENDPOINT=http://127.0.0.1:3001
export FAIRY_ONEBOT_TOKEN='LLOneBot access token'
export FAIRY_ONEBOT_GROUP_ALLOWLIST='群号1,群号2'
```

远程 Core 必须使用 `https://`；webhook 和 OneBot API 都必须是无 path/query/userinfo 的本机回环 `http://` 地址。allowlist 和 token 必须非空。OneBot token 同时用于 webhook `X-Signature` HMAC-SHA1 校验和 action Bearer 鉴权。

## LLOneBot 操作顺序

1. 在 LLOneBot 中完成 QQ 扫码和登录，启用 OneBot 11 HTTP API，并把 HTTP 事件上报地址设为 `FAIRY_ONEBOT_WEBHOOK_ENDPOINT`；access token 与 `FAIRY_ONEBOT_TOKEN` 一致。
2. 确认 FAIRY Core 已启动、`FAIRY_CORE_ENDPOINT` 可访问，并使用 Core token。
3. 在本目录构建并运行：

   ```bash
   go build .
   ./fairy-qq-onebot serve
   ```

4. Surface 通过 ZeroBot `OnlyGroup` 观察 allowlist 群内所有非空文本。每群滚动保留最新 20 条，新消息立即驱动 participation；同群最多一个 participation/turn 在途，运行中到达的新消息会使旧 decision 失效并用最新 snapshot 重判。
5. Core 对 snapshot 返回严格的 `reply`、`wait` 或 `silent`。`reply` 指定窗口内目标消息，Surface 提交带发送者标签和唯一 `[reply-target]` 标记的有序上下文；`wait` 使用 Core 选择的 1–300 秒，期间新消息会提前唤醒；`silent` 不创建 timer、turn 或 OneBot action。@/回复只是强信号，不保证回复，普通消息也可以因自然相关而回复。

Core 或 action 失败会记录错误，不输出默认道歉或 mock 文本。回复频度、近期存在感和消息价值由 Core 根据真实 transcript 语义权衡；QQ Surface 不实现关键词、随机概率或评分公式。群聊 Prompt 不读取私人 profile，`public_memory_search` 只查询 PostgreSQL verified knowledge；私人 Surface 仍使用完整 `memory_search`。

ZeroBot 独占 webhook listener、签名校验与 HTTP action caller；FAIRY 不叠加 HTTP bridge、doctor transport、队列、去重或重试。进程 context 取消后 `serve` 返回，ZeroBot driver 随进程退出。

## 验证

```bash
go test ./... -race -count=1
go vet ./...
go build .
```
