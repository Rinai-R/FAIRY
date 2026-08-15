# 备份与恢复

权威数据是用户目录中的 SeekDB 文件。管理工作区「备份」任务把该数据目录复制到配置根下的 `backups/<timestamp>/`。

- 备份包含加密后的 secret ciphertext，不包含 master key 明文。
- master key 文件必须通过独立流程保存；丢失后密文不可恢复。
- 恢复时停止 Desktop，替换 SeekDB 数据目录为备份副本，再启动同一版本或兼容版本的应用。
- 不要把 PostgreSQL dump 当作权威源，也不要在恢复后重新引入 Compose/pgvector/Qdrant。

插件实例配置、授权和升级 journal 随 SeekDB 一起备份。插件 `.fairy-plugin` 包文件如需保留，应同时备份安装来源。
