# 插件开发与安装

FAIRY 插件使用 ABI v1 JSON envelope。严格端侧正式包的 release inventory 是显式空清单：OpenSERP 是宿主原生能力，QQ / OneBot 是非严格扩展，二者都不以伪造的 builtin manifest 充当已安装插件。

仓库中的参考实现：

| 插件 | 目录 | 能力 |
| --- | --- | --- |
| WebSearch（历史/合同测试） | `fairy/plugin/websearch` | 不进入严格正式 inventory；严格 Web 走原生 OpenSERP adapter |
| QQ / OneBot（非严格） | `fairy/plugin/qqonebot` | `http.request`、`http.ingress`、`event.emit`、`action.complete` |

ABI schema 位于 `fairy/plugin/schema/`。可用 testhost 在不启动 Desktop、不编译 TinyGo 的情况下验证合同：

```bash
go test -C fairy ./plugin/websearch ./plugin/qqonebot ./runtime/wasm -count=1 -race
```

## 授权

实例默认无权限。未授予的 capability 会被拒绝且可诊断，桌面对话继续。严格 profile 不给插件任意网络 origin，也不加载 QQ / OneBot；OpenSERP 网络策略由宿主 authority 强制执行。

凭据使用命名句柄由 Host 注入。配置文档不得包含 token、password 或 `sk-live`。管理工作区只投影 allowlist、endpoint 和 readiness。

## 升级

`.fairy-plugin` 升级先校验 ABI 与包，失败则恢复上一版本和状态。见 Host 安装合同测试。

正式 release inventory 中的每个非空条目都必须对应真实 `.fairy-plugin`：包含 `module.wasm`、manifest、checksum、license、ABI、来源、平台与安装证据，并声明空的外部运行依赖。只有 manifest、Go testhost 或未登记文件都不能进入正式 App。
