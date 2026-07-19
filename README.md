# FAIRY

**虚拟女友 Session Core**——可长期陪伴、有记忆、有人设的聊天伴侣引擎。

桌宠、IM 桥接、专用女友客户端等是 Surface；人格、记忆、回合与工具由本仓库的 Core 提供。  
**主形态：Docker 启动的 HTTP/SSE Session Core。** Surface 客户端另做，不内置 CLI。

## 分层

| 层 | 包 | 职责 |
|---|---|---|
| 接口层 | `fairy/api` | Hertz `/v1` HTTP/SSE |
| 编排层 | `fairy/companion`、`fairy/runtime` | 回合 FSM / Prompt / 接线 / EventHub |
| 领域层 | `memory`、`character`、`profile`、`model`、`config`、`speech`、`search`、`secret`、`visual` | Service + Store 合并；companion 经窄接口依赖 |

## 技术栈

- Core：Go（Hertz HTTP/SSE）
- 部署：Docker Compose
- 存储：SQLite / FTS5（可选 sqlite-vec + 厂商 embedding API）
- 公开检索：OpenSERP（同 Compose 网络）

## 快速启动

需要 Docker。

```bash
docker compose up -d --build
```

默认：

| 服务 | 地址 |
|---|---|
| Session Core | `http://127.0.0.1:8787` |
| OpenSERP（仅容器内） | `http://openserp:7000` |

持久化目录挂在命名卷 `fairy-data`（容器内 `/data`，即 `FAIRY_CONFIG_ROOT`）。  
可选鉴权：在 `docker-compose.yml` 里设置 `FAIRY_API_TOKEN`。

健康检查：

```bash
curl -s http://127.0.0.1:8787/v1/status
```

### HTTP API（`/v1`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v1/status` | 健康与配置摘要 |
| POST | `/v1/sessions` | 打开当前激活角色会话；可选 `{"surface":"desktop\|im_private\|im_group"}` |
| POST | `/v1/sessions/{id}/turns` | 提交回合 `{"input":"...","speechEnabled":false,"surface":"..."}`；缺省沿用 session 绑定 |
| GET | `/v1/sessions/{id}/events` | SSE harness 事件流 |
| POST | `/v1/sessions/{id}/turns/{turnId}/cancel` | 取消回合 |

`surface` 只选择渠道 prompt 段（输出侧重 / 记忆可见性提示），不换人格。缺省 `desktop`。

OpenSERP 在 Compose 内默认走 `FAIRY_OPENSERP_URL=http://openserp:7000`；也可用 `web_search` 设置里的 `base_url` 覆盖。

语义嵌入默认关闭（FTS-only）；在 semantic embedding 设置中配置 `openai_compatible_api` + model 即可。

## 本地跑 Core（可选）

需要 Go。二进制就是 HTTP 服务，没有会话 CLI。

```bash
go build -C fairy -o ../bin/fairy .
export FAIRY_CONFIG_ROOT=./data
./bin/fairy -addr 127.0.0.1:8787
```

测试：

```bash
go test -C fairy ./...
```

## 文档

- 产品方向：`docs/ROADMAP.md`
- 项目约定：`openspec/project.md`
- 领域知识库：`openspec/domains/`
