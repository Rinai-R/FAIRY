# 插件开发与安装

FAIRY 插件使用 ABI v1 JSON envelope。官方参考实现：

| 插件 | 目录 | 能力 |
| --- | --- | --- |
| 网络搜索 | `fairy/plugin/websearch` | `http.request` |
| QQ / OneBot | `fairy/plugin/qqonebot` | `http.request`、`http.ingress`、`event.emit`、`action.complete` |

ABI schema 位于 `fairy/plugin/schema/`。可用 testhost 在不启动 Desktop、不编译 TinyGo 的情况下验证合同：

```bash
go test -C fairy ./plugin/websearch ./plugin/qqonebot ./runtime/wasm -count=1 -race
```

## 授权

实例默认无权限。未授予的 `http.request` 会被拒绝，搜索工具失败且可诊断，桌面对话继续。QQ 未授权时群消息不会进入 Turn。

凭据使用命名句柄由 Host 注入。配置文档不得包含 token、password 或 `sk-live`。管理工作区只投影 allowlist、endpoint 和 readiness。

## 升级

`.fairy-plugin` 升级先校验 ABI 与包，失败则恢复上一版本和状态。见 Host 安装合同测试。
