# FAIRY ZeroBot QQ 群 Surface

这是一个拥有独立 `go.mod`、依赖 ZeroBot v1.8.2 的 QQ 群 Surface，适用于已由 LLOneBot 登录并托管 QQ 登录态的本机实例。ZeroBot 负责 OneBot 11 HTTP webhook listener、事件/消息段解析、`OnlyGroup` 触发和 HTTP action；FAIRY 负责群 allowlist、有界消息窗口、Core participation/session/turn 和 final beat 回发，不负责扫码、登录或好友/群管理。

当前交付以独立进程运行。`serve` 由 ZeroBot HTTP driver 启动本机事件回调 listener；FAIRY 不实现第二套 HTTP handler/server。进程同时向 LLOneBot HTTP API 和 FAIRY Core HTTP/SSE 发起请求。

同一二进制还提供短生命周期 `healthcheck` 子命令。它复用 `serve` 的环境配置，依次验证已认证 Core status、OneBot `get_login_info` 和本地 webhook TCP listener；不会启动第二套 server、发送 QQ 消息或输出登录账号。任一边界失败时只返回稳定的组件错误类别。

## 配置

启动凭据与 endpoint 来自精确环境变量；token 不接受 flag、默认值或自动 trim：

```sh
export FAIRY_CORE_ENDPOINT=http://127.0.0.1:8787
export FAIRY_CORE_TOKEN='从 Core 配置取得的 token'
export FAIRY_ONEBOT_WEBHOOK_ENDPOINT=http://127.0.0.1:3002
export FAIRY_ONEBOT_API_ENDPOINT=http://127.0.0.1:3001
export FAIRY_ONEBOT_TOKEN='LLOneBot access token'
```

远程 Core 必须使用 `https://`；webhook 和 OneBot API 都必须是无 path/query/userinfo 的本机回环 `http://` 地址。Core 和 OneBot token 必须非空。OneBot token 同时用于 webhook `X-Signature` HMAC-SHA1 校验和 action Bearer 鉴权。群 allowlist 只从 Core 管理配置读取，不使用环境变量；空列表或读取失败时拒绝全部群事件。

仓库根目录的 `docker-compose.qq.yml` 会额外设置 `FAIRY_ONEBOT_CONTAINER_NETWORK=true`，允许受控的 Compose service hostname 和 `0.0.0.0` listener。该 opt-in 仅用于容器网络；普通宿主机运行不得设置，默认仍拒绝远程明文 HTTP、公网 IP、任意域名和带 path/query/userinfo 的 endpoint。

## 推荐 Docker 部署

在仓库根目录填写 `.env.example` 中的必需值，然后启动基础服务与 QQ overlay：

```bash
docker compose -f docker-compose.yml -f docker-compose.qq.yml up -d --build
```

先打开 `http://127.0.0.1:8787/console/` 的“接入”页保存允许参与的 QQ 群，再打开 `http://127.0.0.1:3080`，用 `LLBOT_WEBUI_TOKEN` 登录并完成 QQ 扫码。配置 sidecar 会先原子写入 direct 模式所需的 `auth_token.txt` 和 `webui_token.txt`，等待 LLBot 生成 `/app/llbot/data/config_<uin>.json` 后，再自动启用容器内 `http://llbot:3000` action API 和指向 `http://qq-onebot:3002` 的 HTTP POST。`LLBOT_AUTH_TOKEN`、`LLBOT_WEBUI_TOKEN`、`FAIRY_ONEBOT_TOKEN` 和 `FAIRY_API_TOKEN` 各自独立；详细来源、volume、镜像信任边界和日志命令见仓库根 `README.md`。

## LLOneBot 操作顺序

1. 确认 FAIRY Core 已启动、`FAIRY_CORE_ENDPOINT` 可访问，并在控制台“接入”页保存允许参与的群号。
2. 在 LLBot WebUI 中完成 QQ 扫码和登录。Compose 配置 sidecar 会启用 OneBot 11 HTTP API，并把 HTTP 事件上报地址和 access token 原子写成 `FAIRY_ONEBOT_WEBHOOK_ENDPOINT` 与 `FAIRY_ONEBOT_TOKEN` 对应的值。
3. 等待 `docker compose -f docker-compose.yml -f docker-compose.qq.yml ps` 将 `qq-onebot` 标记为 `healthy`，或在容器内执行 `/usr/local/bin/fairy-qq-onebot healthcheck`。`running` 仅代表进程存在；`healthy` 代表 Core、OneBot action 和 webhook listener 就绪；两者都不替代真实测试群中的消息/回复 smoke。
4. 非 Compose 部署在本目录构建并运行：

   ```bash
   go build .
   ./fairy-qq-onebot serve
   ```

5. Surface 通过 ZeroBot `OnlyGroup` 接收群事件，并在每条事件进入 Core Session 前读取 Core 当前 allowlist。保存后的下一条事件使用新列表，无需重启；空列表或 Core 配置不可用时 fail closed。授权群的每群窗口滚动保留最新 20 条，新消息立即驱动 participation；同群最多一个 participation/turn 在途，运行中到达的新消息会使旧 decision 失效并用最新 snapshot 重判。
6. Core 对 snapshot 返回严格的 `reply`、`wait` 或 `silent`。`reply` 指定窗口内目标消息，Surface 提交带发送者标签和唯一 `[reply-target]` 标记的有序上下文；`wait` 使用 Core 选择的 1–300 秒，期间新消息会提前唤醒；`silent` 不创建 timer、turn 或 OneBot action。@/回复只是强信号，不保证回复，普通消息也可以因自然相关而回复。

Core 或 action 失败会记录错误，不输出默认道歉或 mock 文本。回复频度、近期存在感和消息价值由 Core 根据真实 transcript 语义权衡；QQ Surface 不实现关键词、随机概率或评分公式。群聊 Prompt 不读取私人 profile，`public_memory_search` 只查询 PostgreSQL verified knowledge；私人 Surface 仍使用完整 `memory_search`。

ZeroBot 独占 webhook listener、签名校验与 HTTP action caller；FAIRY 不叠加 HTTP bridge、doctor transport、队列、去重或重试。进程 context 取消后 `serve` 返回，ZeroBot driver 随进程退出。

## 验证

```bash
go test ./... -race -count=1
go vet ./...
go build .
go run . healthcheck
```

最后一条命令需要真实环境变量和正在运行的 Core、OneBot action、webhook listener。自动测试中的 HTTP fixture 只证明协议与安全边界；operator 环境的 `healthcheck` 只证明依赖 readiness；只有真实 allowlist 群事件和真实 QQ 回复都被观察到时，群消息 smoke 才能记为 PASS。
