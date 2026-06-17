# FAIRY

FAIRY 是一个面向学习资料和论文阅读的 Galgame 原生体验项目。它把文档内容交给 Agent 生成分幕剧情，再为每一幕准备立绘、背景、台词、选项和语音，让用户以视觉小说的方式完成学习、复习和自由讨论。

## 当前能力

- 教学剧情生成：根据导入材料生成分幕节点、角色台词和选项互动。
- 角色资源管理：角色可以绑定语音、立绘、差分表情、背景等资源。
- 渐进式准备：每一幕按顺序生成，并在后端准备语音后再进入可播放状态。
- 语音合成适配：支持 mock、macOS、GPT-SoVITS、火山引擎等 provider。
- Web 与桌面入口：Web 由 Hertz 提供接口和静态资源，桌面端预留 Wails 入口。

## 快速启动

```bash
npm --prefix web install
npm --prefix web run build
go run ./cmd/fairy
```

默认服务地址：

```text
http://localhost:8787
```

桌面模式：

```bash
wails dev
```

## 常用配置

配置优先由前端保存和注入；本地调试时也可以使用环境变量覆盖。

```bash
FAIRY_ADDR=:8787
FAIRY_WEB_DIR=web/dist
FAIRY_AGENT_ENGINE=fairy
FAIRY_AGENT_ENDPOINT=http://127.0.0.1:8000/v1
FAIRY_AGENT_API_KEY=your-api-key
FAIRY_AGENT_MODEL=deepseek-v4-flash
FAIRY_VOICE_ENGINE=mock
```

本地数据默认写入 `data/`，测试用素材和模型文件建议放在 `local/` 或其他已忽略目录，不要提交到版本记录。

## 目录结构

```text
cmd/        服务端与桌面入口
internal/   领域、适配器、运行时和 HTTP 服务
pkg/        可复用的项目级基础包
web/        React 前端
configs/    默认配置
data/       本地运行数据
openspec/   需求与设计变更记录
```

## 开发命令

```bash
go test ./...
npm --prefix web run build
```
