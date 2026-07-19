# FAIRY

Session Core：人类式聊天伴侣的会话 / agent 提供器（Go CLI）。桌宠与 IM 等 Surface 另行接入。

## 技术栈

- Core：Go + Cobra CLI
- 存储：SQLite / FTS5（可选 sqlite-vec + 厂商 embedding API）
- 公开检索：外部 OpenSERP（Docker Compose）

## 开发运行

需要 Go。可选 Docker（OpenSERP）。

```bash
# 拉起 OpenSERP（可选）
docker compose up -d

# 构建 CLI
go build -C fairy -o ../bin/fairy .

# 主形态：Hertz Core API（Surface 接入点）
./bin/fairy serve --addr 127.0.0.1:8787
# 可选鉴权：--token xxx 或 FAIRY_API_TOKEN

# 调试用 CLI（非主形态）
./bin/fairy status
./bin/fairy session open
./bin/fairy turn "你好"
```

### HTTP API（`/v1`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v1/status` | 健康与配置摘要 |
| POST | `/v1/sessions` | 打开当前激活角色会话 |
| POST | `/v1/sessions/{id}/turns` | 提交回合 `{"input":"...","speechEnabled":false}` |
| GET | `/v1/sessions/{id}/events` | SSE harness 事件流 |
| POST | `/v1/sessions/{id}/turns/{turnId}/cancel` | 取消回合 |

配置根默认：`$FAIRY_CONFIG_ROOT`，否则 macOS  
`~/Library/Application Support/dev.rinai.fairy/harness/v1`。

OpenSERP 默认 `http://127.0.0.1:7000`，可用 `FAIRY_OPENSERP_URL` 或 `web_search` 设置里的 `base_url` 覆盖。

语义嵌入默认关闭（FTS-only）；在 semantic embedding 设置中配置 `openai_compatible_api` + model 即可。

测试：

```bash
go test -C fairy ./...
```

## 文档

- 产品方向：`docs/ROADMAP.md`
- 项目约定：`openspec/project.md`
- 领域知识库：`openspec/domains/`
