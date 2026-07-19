# FAIRY

FAIRY 是一个 Go Session Core：通过 HTTP/SSE 提供角色对话、记忆、模型、语音与设置能力，同时内置 React 管理控制台和面向 AI Coding 测试/调试的白名单 CLI。

## 本地启动

```bash
go -C fairy run . serve
```

默认监听 `127.0.0.1:8787`，控制台位于 `http://127.0.0.1:8787/console/`。无参数执行 `go -C fairy run .` 在当前版本仍等价于 `serve`，新脚本应使用显式子命令。

也可通过 Docker Compose 启动：

```bash
docker compose up --build
```

## AI Coding CLI

CLI 是 `/v1` 的 typed HTTP/SSE client，不直接修改配置 JSON 或 SQLite：

```bash
go -C fairy run . --help
go -C fairy run . status
go -C fairy run . doctor
go -C fairy run . session open --surface desktop
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
- `FAIRY_API_TOKEN`：唯一 Bearer token 输入，不提供 token flag，也不写配置文件

`turn send`、`events follow` 和 `logs --follow` 的流式生命周期由命令 context 控制，不会在默认 15 秒后自动断开。stream 断开时不会自动重连。

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
pnpm --filter @fairy/web test
pnpm --filter @fairy/web build
docker compose build fairy
git diff --check
```

项目结构、产品边界和当前领域事实见 `openspec/project.md` 与 `openspec/domains/`。
