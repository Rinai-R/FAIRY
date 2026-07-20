# FAIRY SQLite 离线导入器

该工具只用于一次性离线迁移旧 schema v7 SQLite 数据。生产 Session Core 不会调用、链接或自动运行此工具。

## 前置条件

- PostgreSQL 已执行 `fairy db migrate`，目标为空或存在同一 source fingerprint 的未完成/已验证 import run。
- Qdrant 已执行 `fairy db vector migrate`，新导入时 collection 为空。
- 通过环境变量提供 `FAIRY_DATABASE_URL`、`FAIRY_QDRANT_URL`；源 secret 非空时还必须提供 `FAIRY_SECRET_MASTER_KEY`。
- master key、数据库密码和 Qdrant API key 只能通过环境变量提供，不支持命令行 flag。

## 使用

先执行只读检查：

```bash
go run . preflight \
  --intelligence /absolute/path/intelligence/fairy.sqlite3 \
  --secrets /absolute/path/model/secrets.sqlite3
```

再执行导入；相同命令可安全恢复中断或重新校验 verified run：

```bash
go run . run \
  --intelligence /absolute/path/intelligence/fairy.sqlite3 \
  --secrets /absolute/path/model/secrets.sqlite3
```

成功报告只包含 run ID、计数、状态和脱敏的下一步命令。工具不会修改环境变量、删除源文件或启动 Session Core。完成后按报告顺序运行 `fairy db status`、`fairy doctor`，最后显式启动 `fairy serve`。
