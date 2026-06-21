# FAIRY

FAIRY 是一个面向学习资料和论文阅读的 Galgame 原生体验项目。它把文档内容交给 Agent 生成分幕剧情，再为每一幕准备立绘、背景、台词、选项和语音，让用户以视觉小说的方式完成学习、复习和自由讨论。

## 当前能力

- 教学剧情生成：根据导入材料生成分幕节点、角色台词和选项互动。
- 材料入口：只保留粘贴正文和单文件上传，复杂 URL/目录/repo 先整理成单文件再导入。
- 角色资源管理：角色可以绑定语音、立绘、差分表情、背景等资源。
- 渐进式准备：每一幕按顺序生成，并在后端准备语音后再进入可播放状态。
- 语音合成适配：当前主项目只保留火山引擎 provider。
- 运行观测：后端 generation、workflow、voice 关键失败会写入 session runtime events，并在前端制作日志展示。
- 桌面入口：以 Wails 桌面应用为主，React 前端只作为桌面 UI 资源；Hertz 只保留 API 和音频/图片资源服务。

## 快速启动

```bash
npm --prefix web install
wails dev
```

如需只启动后端 API：

```bash
go run ./cmd/fairy
```

## 常用配置

配置优先由前端保存和注入；本地调试时也可以使用环境变量覆盖。

```bash
FAIRY_ADDR=:8787
FAIRY_AGENT_ENGINE=fairy-agent
FAIRY_AGENT_ENDPOINT=http://127.0.0.1:8000/v1
FAIRY_AGENT_API_KEY=your-api-key
FAIRY_AGENT_MODEL=deepseek-v4-flash
FAIRY_AGENT_EXTRA_BODY='{"enable_thinking":false}'
FAIRY_VOICE_ENGINE=volcengine
FAIRY_VOLCENGINE_TTS_API_KEY=your-volcengine-token
FAIRY_VOLCENGINE_TTS_RESOURCE_ID=seed-icl-2.0
FAIRY_VOLCENGINE_TTS_SPEAKER=your-speaker-id
```

FAIRY 当前不在主进程中管理本地语音模型权重、参考音频或训练参数。需要本地模型时，先在外部方案中验证清楚边界，再考虑是否以插件或独立整理服务接入。

本地数据默认写入 `data/`，测试用素材和模型文件建议放在 `local/` 或其他已忽略目录，不要提交到版本记录。

## 目录结构

```text
cmd/        API 服务与桌面入口
internal/   领域、适配器、运行时和 HTTP 服务
pkg/        可复用的项目级基础包
web/        Wails 桌面 UI 资源
configs/    默认配置
data/       本地运行数据
openspec/   需求与设计变更记录
```

## 开发命令

```bash
go test ./...
npm --prefix web run build
npm --prefix web run visual:smoke
wails build
```

`npm --prefix web run visual:smoke` 会启动临时 API、Vite 和 headless Chrome，使用临时 session 数据检查桌面与 390px 移动视口下的历史、删除确认、日志和 VN 舞台布局。截图与报告默认输出到 `/tmp/fairy-visual-smoke/`。
