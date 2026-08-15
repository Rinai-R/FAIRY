# 首次启动

Desktop 是唯一生产 runtime。它在本机启动 SeekDB、Core、Session 和默认无权限的 WASM Host。

1. 按 `surfaces/desktop/Taskfile.yml` 的 `package` 或 `build` 得到应用。
2. 在干净用户目录启动。进程会创建数据目录、`0600` master key 和 SeekDB schema。
3. 打开伴侣窗口发送一条角色对话，确认回复、取消和重启后历史仍在。
4. 打开管理工作区查看日志、Trace 与指标；关闭管理工作区不会停止 runtime。
5. 需要 QQ 或搜索时再安装对应插件，见 `docs/plugins.md`。

不要设置 `FAIRY_DATABASE_URL`、`FAIRY_CORE_ENDPOINT` 或 `FAIRY_OPENSERP_URL`。SeekDB 与 PostgreSQL 互斥，缺少 SeekDB 时启动失败。
