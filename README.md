# FAIRY

桌面上的聊天机器人。

## 技术栈

- 桌面：Go + Wails 3（macOS 为主）
- 前端：React + Vite

## 开发运行

需要 Go、Node / pnpm、[Task](https://taskfile.dev)、Wails 3 CLI。

```bash
pnpm install
task build
open bin/FAIRY.app
```

前端单独开发：

```bash
pnpm --dir web dev
```

测试：

```bash
pnpm --dir web test
go test -C fairy ./...
```

## 文档

- 产品方向：`docs/ROADMAP.md`
- 项目约定与能力边界：`openspec/project.md`
- AI Coding 参考项目知识库：`openspec/domains/`
