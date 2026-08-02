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

仓库提供可选的 `docker-compose.qq.yml`，组合官方 LLOneBot 镜像、自动配置 sidecar 和独立 QQ Surface。首次使用先从示例创建本地 env，并填写全部必需空值：

```bash
cp .env.example .env
docker compose -f docker-compose.yml -f docker-compose.qq.yml up -d --build
```

其中 `LLONEBOT_AUTH_TOKEN` 是 PMHQ/LuckyLillia 授权，`FAIRY_ONEBOT_TOKEN` 是 FAIRY 与 LLOneBot HTTP API/事件回调共同使用的 OneBot access token，两者不能复用。`FAIRY_API_TOKEN` 是 QQ Surface 访问 Core 的 token。

启动后打开 [FAIRY 控制台](http://127.0.0.1:8787/console/) 的“接入”页，填写允许 FAIRY 参与的 QQ 群号并保存；空列表会拒绝全部群。随后打开 [LLOneBot WebUI](http://127.0.0.1:3080) 完成 QQ 扫码登录。登录前，LLOneBot 配置 sidecar 会等待 `/root/llonebot/data/config_<uin>.json`，不会伪造 QQ 已连接；登录后它会自动启用容器内 OneBot HTTP API 和指向 QQ Surface 的 HTTP POST，无需在 WebUI 手工填写 endpoint。控制台保存的群范围对 QQ Surface 下一条群事件生效，无需重启。

```bash
docker compose -f docker-compose.yml -f docker-compose.qq.yml logs -f llonebot llonebot-config qq-onebot
docker compose -f docker-compose.yml -f docker-compose.qq.yml down
```

QQ 登录态和 LLOneBot 数据保存在 `llonebot-qq-login`、`llonebot-data` volume。普通 `down` 不删除它们；不要使用 `down -v`，除非明确要清除登录态。宿主机只通过回环地址访问 Core `8787` 和 LLOneBot WebUI `3080`，OneBot `3000` 与事件 listener `3002` 不发布到宿主机。

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
go -C fairy run . config apply speech --file - < speech.json
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
go build -C surfaces/qq-onebot .
node --test deploy/llonebot/configure.test.mjs
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

项目结构、产品边界和当前领域事实见 `openspec/project.md` 与 `openspec/domains/`。
