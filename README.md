# FAIRY

FAIRY 是同一个持续存在的陪伴人格：在桌面上是用户独享的私人伴侣，在社交平台上是自然克制的公共群友。它支持自然对话、长期记忆、角色配置，以及本地管理工作区中的观测与插件。

权威数据在本机 SeekDB 中。Desktop 拥有 Core 与插件 Host 的生命周期，不需要独立 Core 进程、PostgreSQL、Qdrant、Docker Compose、Node、Python 或本地模型服务。

严格端侧发行的“零依赖”边界是：除用户明确保存的第三方聊天模型、第三方 1024 维 semantic embedding 服务和 OpenSERP 外，FAIRY 不要求其他运行时服务。App 不携带、不下载、也不启动本地 LLM、embedding engine、模型权重或 tokenizer；第三方能力不可用时不会偷偷切换 provider 或本地 fallback。

## 首次启动

构建并打开 Desktop（未签名开发包即可）：

```bash
task -t desktop/Taskfile.yml package
open desktop/bin/FAIRY.app
```

首次启动会在用户数据目录创建 SeekDB、master key（`0600`）和空的插件授权。默认没有任何插件网络权限。先在 Desktop 管理边界分别保存第三方聊天 provider 与第三方 embedding provider 的 endpoint、模型和 credential；聊天与 embedding 可以来自同一供应商，但配置、凭据和模型 identity 彼此独立。角色历史、本地数据、配置、观测和重启恢复都在同一个本地进程内完成。

开发时也可以：

```bash
task -t desktop/Taskfile.yml build
./desktop/bin/FAIRY
```

CLI `go -C fairy run . serve` 仍可用于本机 loopback 调试，但生产陪伴路径是 Desktop。

## Web 与 QQ

严格端侧 profile 的搜索与公开网页正文只经过用户保存的单一 OpenSERP origin。OpenSERP 是宿主原生的可选 Web authority，不是随包搜索插件；不可用时只影响搜索和公开网页学习，不影响本地历史、数据、配置和管理。

QQ / OneBot 需要额外协议服务和网络入口，因此只属于显式启用的非严格扩展，不进入零依赖发行边界。参考实现位于 `fairy/plugin/qqonebot/`，迁移说明见 `docs/qq-onebot.md`。

第三方模型、OpenSERP、插件权限与故障隔离见 `docs/getting-started.md`、`docs/plugins.md` 和 `docs/troubleshooting.md`。
实际组装 App 的干净机器与运行边界验收见 `docs/release-validation.md`；Developer ID 签名/公证只用于可选公开发行。

## 备份

使用管理工作区的备份任务复制 SeekDB 数据目录。master key 必须单独保存。见 `docs/backup.md`。

## CLI

CLI 是 `/v1` 的 typed HTTP/SSE client，主要用于本机调试，不直接修改配置 JSON 或数据库：

```bash
go -C fairy run . --help
go -C fairy run . status
go -C fairy run . doctor
```

连接配置优先级为 flag > env > 默认值：

- `--endpoint` / `FAIRY_ENDPOINT`，默认 `http://127.0.0.1:8787`
- `--timeout` / `FAIRY_CLI_TIMEOUT`，默认 `15s`
- `--output json|table` / `FAIRY_CLI_OUTPUT`，默认 `json`
- `FAIRY_API_TOKEN`：仅当显式启动 `serve` 时需要；Desktop 进程内 Session 不使用 Core bearer

## 验证

```bash
gofmt -l fairy desktop
go test -C fairy ./... -count=1
(cd fairy && go test ./... -race -count=1)
(cd fairy && go vet ./...)
FAIRY_SEEKDB_LIBRARY="$HOME/Library/Caches/FAIRY/seekdb-embed/build_release/src/include/libseekdb.dylib" \
go test -C fairy ./... -tags=integration -count=1
FAIRY_REQUIRE_LEGACY_FREE=1 go test -C fairy ./legacy_runtime_dependencies_test.go ./architecture_test.go ./package_architecture_test.go -count=1
git diff --check
```

插件开发、故障排查与备份文档：

- `docs/getting-started.md`
- `docs/plugins.md`
- `docs/backup.md`
- `docs/troubleshooting.md`

项目结构和产品边界见 `openspec/project.md`，当前行为合同位于 `openspec/specs/`。
