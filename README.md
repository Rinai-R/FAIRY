# FAIRY

FAIRY 是同一个持续存在的陪伴人格：在桌面上是用户独享的私人伴侣，在社交平台上是自然克制的公共群友。它支持自然对话、长期记忆、角色配置、语音、统一设置管理以及开发调试。

目前提供 macOS 桌面客户端和 QQ 群聊接入。两种场景共用同一角色与对话内核，由 Core 根据交互关系决定陪伴姿态，并严格区分私人和公共的记忆与隐私边界。

## 本地启动

```bash
go -C fairy run . serve
```

默认监听 `127.0.0.1:8787`，控制台位于 `http://127.0.0.1:8787/console/`。无参数执行 `go -C fairy run .` 在当前版本仍等价于 `serve`，新脚本应使用显式子命令。

也可通过 Docker Compose 启动：

```bash
export FAIRY_POSTGRES_PASSWORD='<strong-random-password>'
export FAIRY_SECRET_MASTER_KEY='<base64-exactly-32-bytes>'
export FAIRY_API_TOKEN='<strong-random-token>'
docker compose up --build
```

## QQ 群聊 Docker 接入

仓库提供可选的 `docker-compose.qq.yml`，组合 LLBot direct、自动配置 sidecar 和独立 QQ Surface。Compose 采用 [LLOneBot 官方安装脚本](https://github.com/LLOneBot/LuckyLilliaBot/blob/a0337794382612cbedd0aea9955ce749925f776c/script/install-llbot-docker.sh)的 direct 单容器拓扑，并以完整 manifest digest 固定 `linyuchen/llbot:8.1.5`。`linyuchen` 是镜像发布者的个人 Docker Hub namespace，不是 LLOneBot 组织 namespace；公开 manifest、amd64/arm64 child digest 和固定上游 revision 记录在 `deploy/llonebot/image-contract.json`。首次使用先从示例创建本地 env，并填写全部必需空值：

```bash
cp .env.example .env
docker compose -f docker-compose.yml -f docker-compose.qq.yml up -d --build
```

其中 `LLBOT_AUTH_TOKEN` 是从 [LuckyLillia Auth](https://auth.luckylillia.com) 获取的 LLBot direct 授权，不能本地随机生成；`LLBOT_WEBUI_TOKEN` 是独立的 LLBot WebUI 密码，只使用英文字母和数字；`FAIRY_ONEBOT_TOKEN` 是 FAIRY 与 LLBot HTTP API/事件回调共同使用的 OneBot access token；`FAIRY_API_TOKEN` 是 QQ Surface 访问 Core 的 token。四种凭据不得复用。若旧授权曾交给不再信任的镜像或环境，必须在 LuckyLillia Auth 页面撤销并重新签发；清空本地 `.env` 不能撤销远端授权。

启动后打开 [FAIRY 控制台](http://127.0.0.1:8787/console/) 的“接入”页，填写允许 FAIRY 参与的 QQ 群号并保存；空列表会拒绝全部群。随后打开 [LLBot WebUI](http://127.0.0.1:3080)，使用 `.env` 中的 `LLBOT_WEBUI_TOKEN` 登录并完成 QQ 扫码。配置 sidecar 会先以 mode `0600` 原子初始化 `/app/llbot/data/auth_token.txt` 和 `/app/llbot/data/webui_token.txt`，再等待 LLBot 生成 `/app/llbot/data/config_<uin>.json`；它不会伪造 QQ 已连接。登录后 sidecar 自动启用容器内 `http://llbot:3000` action API 和指向 `http://qq-onebot:3002` 的 HTTP POST，无需在 WebUI 手工填写 endpoint。控制台保存的群范围对 QQ Surface 下一条群事件生效，无需重启。

QQ Surface 的 `running` 只表示进程存在；Compose 只有在已认证 Core status、OneBot 登录 action 和容器内 webhook listener 都可用时才显示 `healthy`。可用下面两条命令查看状态并主动复查；`healthcheck` 成功时不输出账号或 token，失败时只输出 `core_unavailable`、`onebot_unavailable` 或 `webhook_unavailable`。`healthy` 证明依赖链就绪，但不等于已经观察到真实群消息和回复；真实群消息 smoke 仍需在 allowlist 测试群中单独记录。

```bash
docker compose -f docker-compose.yml -f docker-compose.qq.yml ps
docker compose -f docker-compose.yml -f docker-compose.qq.yml exec qq-onebot /usr/local/bin/fairy-qq-onebot healthcheck
docker compose -f docker-compose.yml -f docker-compose.qq.yml exec -T qq-onebot /usr/local/bin/fairy-qq-onebot smoke --message-id '<真实入站 message_id>' --wait 60s
docker compose -f docker-compose.yml -f docker-compose.qq.yml logs -f llbot llbot-config qq-onebot
docker compose -f docker-compose.yml -f docker-compose.qq.yml down
```

`smoke` 不发送消息：它精确查询该入站 ID 的持久 Trace/Turn 和成功 `Surface 回执`，再用回执中的出站 ID 调用 OneBot `get_msg`。只有同一链路四段证据完整才输出低敏 `PASS`；没有新鲜真实消息时应记录 INCOMPLETE，不能用自动 fixture、readiness 或 Core completed 冒充。

QQ 登录态、LLBot 配置和两个授权文件统一保存在 `llbot-data` volume。普通 `down` 不删除该卷；不要使用 `down -v`，除非明确要清除登录态和配置。配置 sidecar 是唯一通过环境变量接收 LLBot auth/WebUI token 的服务，LLBot 只读取卷内文件；QQ overlay 不使用 `privileged`，任何服务都不挂载 Docker socket。宿主机只通过回环地址访问 Core `8787` 和 LLBot WebUI `3080`，OneBot `3000` 与事件 listener `3002` 均不发布到宿主机。

从旧有头部署迁移时，先执行普通 `down`，不要附加 `-v`；把 `.env` 中的旧授权变量改名为 `LLBOT_AUTH_TOKEN` 后再启动新 overlay。旧登录卷不会被新部署读取，也不会被自动删除；direct 首次启动通常需要重新扫码。回滚时恢复旧 overlay 和旧变量，并重新挂载保留的旧登录卷。确认不再回滚后，才由 operator 手工清理孤立卷。

升级 LLBot 时不得把 image 改成 `latest` 或只写 tag。先核对官方安装脚本、候选版本标签、公开 manifest 和上游默认配置，再同步修改 `deploy/llonebot/image-contract.json` 与 `docker-compose.qq.yml`，最后执行：

```bash
node deploy/llonebot/verify-image.mjs
node --test deploy/llonebot/image-contract.test.mjs deploy/llonebot/configure.test.mjs
```

第一条命令会实时验证 LLBot registry tag 的 manifest/digest、官方安装脚本的 direct 拓扑、两个授权文件和 sidecar 依赖的唯一 `http`/`http-post` 结构；网络或上游不可用时会明确失败，不会 fallback 到浮动 tag。它不需要真实授权或 QQ 登录，也不能替代 operator 的真实登录与测试群 smoke。真实扫码和群消息结果必须单独记录为 PASS、INCOMPLETE 或 N/A，不能用受控 HTTP fixture 代替。

## CLI

CLI 是 `/v1` 的 typed HTTP/SSE client，不直接修改配置 JSON 或数据库：

```bash
go -C fairy run . --help
go -C fairy run . status
go -C fairy run . doctor
go -C fairy run . session open --endpoint-kind desktop --endpoint-key local-debug --audience single --initiation direct --presentation embodied
go -C fairy run . turn send --conversation <id> --input "你好"
go -C fairy run . events follow --conversation <id>
go -C fairy run . logs --follow --level warn
go -C fairy run . metrics
```

设置管理复用 Web 控制台的同一组 Admin API：

```bash
go -C fairy run . config get model
go -C fairy run . config apply model --file model.json
go -C fairy run . profile apply --file profile.json
go -C fairy run . character create --file character.json
go -C fairy run . character activate --character <id> --revision <revision>
```

连接配置优先级为 flag > env > 默认值：

- `--endpoint` / `FAIRY_ENDPOINT`，默认 `http://127.0.0.1:8787`
- `--timeout` / `FAIRY_CLI_TIMEOUT`，默认 `15s`
- `--output json|table` / `FAIRY_CLI_OUTPUT`，默认 `json`
- `FAIRY_API_TOKEN`：必需的 Bearer token（含 loopback）；不提供 token flag，也不写配置文件。未设置时 `serve` / `NewServer` 失败。
- `FAIRY_RUNTIME_PROFILE`：`full`（默认）或 `desktop-lite`；两者都要求 PostgreSQL current schema 与 master key，不再配置独立向量服务

`turn send`、`events follow` 和 `logs --follow` 的流式生命周期由命令 context 控制，不会在默认 15 秒后自动断开。stream 断开时不会自动重连。

数据库与向量字段使用显式运维命令：

```bash
go -C fairy run . db migrate
go -C fairy run . db status
go -C fairy run . db vector rebuild
```

PostgreSQL 初始化使用 GORM models 对空库或已有 schema 执行非破坏性的 additive migration：常规 CHECK、unique 与 B-tree index 随 model 迁移，`pg_trgm`、`pgvector`、外键、partial、GIN 与 HNSW 等 PostgreSQL 特例显式创建，全部成功后才提交 current schema revision。个人记忆与已验证知识的 512 维向量直接保存在权威业务行中；`db vector rebuild` 只重算并回写 PostgreSQL。迁移不自动删除、重命名或重写已有结构，也不读取或导入 SQLite 数据。

## 备份与恢复

备份脚本要求 PATH 中的 PostgreSQL client 与服务端 major version 一致：

```bash
FAIRY_DATABASE_URL='postgres://...' scripts/backup-postgres.sh /absolute/path/fairy.dump
FAIRY_DATABASE_URL='postgres://.../empty_database' scripts/restore-postgres.sh /absolute/path/fairy.dump
go -C fairy run . db status
```

数据库备份只包含加密后的 secret ciphertext。`FAIRY_SECRET_MASTER_KEY` 必须通过独立的 secret-management/backup 流程保存，禁止写入 dump 或脚本参数。

## 日志与指标

控制台「日志」栏目和 CLI 使用同一组受鉴权 API：

- `GET /v1/logs`：查询当前进程最近 2000 条有界、脱敏结构化日志
- `GET /v1/logs/stream`：ready + backlog + live SSE
- `GET /v1/metrics`：进程、HTTP、日志、runtime subscriber/job 与真实 token/cache usage

日志只存在于进程内，不持久化。Authorization、apiKey、access token、password 等字段和常见 inline credential 会在进入公开 ring 前统一脱敏；Prompt、对话全文、tool arguments 和完整 provider response 不属于公开日志字段。

## 验证

```bash
go test -C fairy ./... -count=1
(cd fairy && go test ./... -race -count=1)
(cd fairy && go vet ./...)
go test -C surfaces/qq-onebot ./... -race -count=1
go vet -C surfaces/qq-onebot ./...
go build -C surfaces/qq-onebot -o ../../bin/fairy-qq-onebot .
node --test deploy/llonebot/image-contract.test.mjs deploy/llonebot/configure.test.mjs
node deploy/llonebot/verify-image.mjs
docker compose -f docker-compose.yml -f docker-compose.qq.yml config --quiet
docker compose -f docker-compose.integration.yml up -d --wait
FAIRY_TEST_DATABASE_URL='postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable' \
go test -C fairy ./... -tags integration -count=1
docker compose -f docker-compose.integration.yml down
pnpm --filter @fairy/web test
pnpm --filter @fairy/web build
docker compose up -d --build --wait
git diff --check
```

项目结构和产品边界见 `openspec/project.md`，当前行为合同位于 `openspec/specs/`，工程知识参考位于 `openspec/domains/`。
