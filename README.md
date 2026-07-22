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
docker compose up --build
```

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
- `FAIRY_RUNTIME_PROFILE`：`full`（默认，强制 Qdrant）或 `desktop-lite`（Qdrant 可选，检索可降级 FTS）

`turn send`、`events follow` 和 `logs --follow` 的流式生命周期由命令 context 控制，不会在默认 15 秒后自动断开。stream 断开时不会自动重连。

数据库与向量索引使用显式运维命令：

```bash
go -C fairy run . db migrate
go -C fairy run . db status
go -C fairy run . db vector migrate
go -C fairy run . db vector rebuild
go -C fairy run . db vector reconcile          # 默认 dry-run
go -C fairy run . db vector reconcile --apply  # 只删除确认 orphan
```

PostgreSQL 初始化只支持全新空 schema，不升级旧 PostgreSQL schema，也不读取或导入 SQLite 数据。

## 备份与恢复

备份脚本要求 PATH 中的 PostgreSQL client 与服务端 major version 一致：

```bash
FAIRY_DATABASE_URL='postgres://...' scripts/backup-postgres.sh /absolute/path/fairy.dump
FAIRY_DATABASE_URL='postgres://.../empty_database' scripts/restore-postgres.sh /absolute/path/fairy.dump
go -C fairy run . db status
```

数据库备份只包含加密后的 secret ciphertext。`FAIRY_SECRET_MASTER_KEY` 必须通过独立的 secret-management/backup 流程保存，禁止写入 dump 或脚本参数。

## 日志与指标

控制台第 8 个「日志」栏目和 CLI 使用同一组受鉴权 API：

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
FAIRY_TEST_DATABASE_URL=... FAIRY_TEST_QDRANT_GRPC_URL=... go test -C fairy ./... -tags integration -count=1
pnpm --filter @fairy/web test
pnpm --filter @fairy/web build
docker compose up -d --build --wait
git diff --check
```

项目结构、产品边界和当前领域事实见 `openspec/project.md` 与 `openspec/domains/`。
