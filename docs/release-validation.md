# 严格端侧安装验收与可选公开发行

本文说明如何对 `task package` 实际组装的 `FAIRY.app` 采集端侧运行边界证据。端侧验收允许 macOS ad-hoc code signing；Developer ID、notarization 与 staple 只属于可选的公开发行流程，不是 FAIRY 的运行依赖或端侧完成条件。源码测试、mock provider 或本地模型服务不能替代实际 App 验收。

## 本地离线 package 预检

开发机可以先对 `task package` 生成的组装包执行较弱的运行边界预检：

```bash
build/observe-package-preflight-darwin.sh \
  --app "$PWD/bin/FAIRY.app" \
  --duration 8 \
  --runs 2 \
  --output /tmp/fairy-package-preflight
```

该脚本使用私有空 HOME 和仅包含 macOS 系统工具的 PATH，先运行 App package verifier 与随包 SeekDB 动态 verifier，再直接启动同一 GUI App 两轮，记录子进程、监听端口、网络 socket 和动态库。启动环境会故意设置 `TELEMETRY_ENABLED=true`，并注入假的 `FAIRY_RUNTIME_PROFILE=full`、PostgreSQL/OpenSERP、OpenAI/CodeBuddy endpoint 与 credential、HTTP(S)/ALL proxy 环境变量；正式 Desktop 必须仍保持 endpoint-strict、从空 profile 启动且零网络，不能读取这些开发机覆盖。该场景同时验证 FAIRY 会在进程内强制关闭 SeekDB 上游遥测，不产生 `sh`、`curl` 或未声明 OceanBase 出站，并要求第二轮复用第一轮创建的端侧 profile。

预检元数据固定写入当前 `host_os_version`、`host_arch` 与 App 的 `app_minimum_system_version`，并硬性要求本轮 darwin/arm64 架构以及宿主版本不低于 App 最低版本。开始时 `host_platform_supported=false`、`package_verification=fail`、`result=preflight_fail`；平台门通过后才写入 `host_platform_supported=true`，package layout 与随包 SeekDB 动态验证都通过后才提升为 `package_verification=pass`，全部运行边界检查通过后再提升为 `result=preflight_pass`。它不接收 provider origin 或 credential，所以只能证明离线 package、零网络、零子进程和重启 profile 边界；可作为 8.4 的 package 证据，但不能单独完成需要真实 provider 的 8.1–8.3。

## 前置条件

1. 在 FAIRY 管理工作区中保存第三方聊天 provider、第三方 1024 维 semantic embedding provider，以及本次场景需要的 OpenSERP origin。
2. Credential 只通过 FAIRY 的 SecretStore 保存。不要把 API key 放进命令行、环境变量、日志或证据目录。
3. 目标机器不安装或启动本地 LLM、embedding engine、模型权重、Node、Python、Homebrew、Docker、外部数据库、OneBot 或远程 Core。
4. 使用 `task package` 生成并验证实际 App。无需 Apple Developer 账号；若另行验证公开发行，再执行文末的可选发行步骤。

## 采集运行边界

在 `desktop` 目录运行：

```bash
build/observe-release-darwin.sh \
  --app /Applications/FAIRY.app \
  --chat-origin https://chat-provider.example \
  --embedding-origin https://embedding-provider.example \
  --openserp-origin https://openserp.example \
  --duration 60 \
  --runs 2 \
  --output /tmp/fairy-endpoint-evidence
```

origin 参数只声明允许的网络边界，不携带 credential。聊天与 embedding 可以指向同一第三方服务，脚本不会人为要求它们解析到不同 IP；发生重叠时，`capability_origin_overlap=true`，同一 socket 在证据中携带逗号分隔的 capability 标签。由于 TCP 采样无法区分同一远端端点上的 HTTP 请求，这些标签只证明连接属于声明的 origin 集合，不证明聊天生成或 1024 维 embedding 行为成功。真实 chat/embedding 集成测试仍须分别通过。

脚本只使用 macOS 15 自带的系统工具，不要求安装 Xcode 或 Command Line Tools。默认模式接受 `task package` 生成的 ad-hoc App，只验证 code graph 完整性、package layout、随包 SeekDB 和真实运行边界。只有显式传入 `--require-public-distribution` 时，才额外要求 Developer ID、系统 distribution policy、公证/staple 与 Gatekeeper。

脚本会：

- 重新验证 App code graph 与内部 package verifier；内部 verifier 会再次要求 SeekDB catalog 为 `verified`，并复核 dylib 的架构、最低系统版本、SDK、install name、系统依赖、导出 ABI、许可证及固定 App 路径；随后先用最终 App 的 `--verify-endpoint-readiness` 只读打开正式 endpoint-strict profile，检查嵌入式 SeekDB、加密 SecretStore、已保存的聊天 provider credential 与第三方 1024 维 semantic embedding provider credential；传入 `--openserp-origin` 时还要求 OpenSERP 配置当前可达。该预检不发送聊天或 embedding 请求，也不把“存在 credential”冒充为 provider smoke；不满足时会在两轮观察前快速失败；最后从实际 App 固定路径真实执行 SeekDB `Open`、全量 migration、`CheckSchema` 与 `Close`，并要求宿主完成 marker；公开发行模式才额外执行 Developer ID、公证/staple 与 Gatekeeper 检查；
- 用清空后的进程环境直接启动最终 bundle，不继承开发 endpoint、proxy 或 credential；
- 至少启动、退出和重启两轮，在观察窗口内记录 FAIRY 的子进程、监听 socket、出站连接与动态库；
- 所有 TCP 出站（包括 loopback）都必须命中声明的聊天、embedding 或 OpenSERP origin；loopback 只可能由显式保存的 OpenSERP origin 授权；
- 每次启动观察窗口都必须捕获声明的 chat、embedding origin 出站；两者共享 origin 时，一条连接只计作声明 origin 集合边界证据。传入 `--openserp-origin` 时每轮也必须捕获 OpenSERP 出站。metadata 会写入逐轮与聚合结果，仅第一轮出现连接、重启后未恢复不能生成 PASS；
- 拒绝任何 TCP/UDP listener、未分类 socket、辅助子进程和 App/System 之外的动态库；子进程证据只记录脱敏后的可执行文件名；
- 输出不含请求正文、API key、原始 App 日志或用户私有数据的 TSV 证据。

观察窗口内应反复执行本次场景，直到脚本捕获对应连接：正常 provider 对话与记忆写入、OpenSERP 可用/阻断、模型 provider 阻断，以及关闭后的重启恢复。脚本只证明运行边界，不能替代这些行为断言。metadata 初始固定为 `verification_level=endpoint_attempt`、`endpoint_eligible=false`、`package_verification=fail`、`provider_configuration_checked=false`、`provider_smoke_checked=false`、`provider_egress_boundary_checked=false`、`release_eligible=false` 与 `result=fail`；`--verify-endpoint-readiness` 通过后只把 `provider_configuration_checked` 提升为 `true`，`egress_attribution=declared_origin_set` 则明确限定网络证据语义。平台、package 和全部声明 origin 的逐轮出站边界都通过后，默认模式提升为 `verification_level=final_endpoint`、`endpoint_eligible=true`、`provider_egress_boundary_checked=true` 与 `result=pass`，但不会把配置预检或 TCP 观察伪装成 provider 行为 smoke；`release_eligible` 仍为 `false`。只有显式公开发行模式还通过 Developer ID、distribution policy、公证/staple 与 Gatekeeper 后，才写入 `verification_level=final_public_release` 与 `release_eligible=true`。

进程表属于运行时采样，可能无法单独捕获极短命的 `exec`。端侧证据必须同时包含生产源码的 `os/exec`/辅助进程反向门；只有静态门与实际 App 运行观测同时通过，才能声称零辅助进程。

如果缺少第三方测试 credential、verified SeekDB 工件、实际组装 App 或干净机器证据，必须如实把对应 OpenSpec task 保持未完成，不能用本地 mock 或候选 dylib 冒充验收。缺少 Apple Developer 账号不属于端侧阻塞。

## SeekDB 宿主进程完成门

全仓 `integration` 测试不能直接使用 `go test -tags=integration ./...`：Go 会在同一个 package 测试进程中连续执行多个不同 DataDir 场景，而当前进程内 SeekDB engine 出于上游关闭安全约束会一直存活到宿主退出。仓库提供隔离入口，让每个由 `integration` build tag 新增的顶层测试运行在独立 Go 测试进程中；这只是开发/CI 测试编排，不是 FAIRY.app 的运行时子进程：

```bash
FAIRY_SEEKDB_LIBRARY=/absolute/path/to/libseekdb.dylib \
  task seekdb:test-integration-all
```

普通 Go unit/race/vet 仍按模块整体运行；`integration` 源码还要在 race 构建下完成编译：

```bash
go test -race ./...
go test -race -tags=integration ./... -run '^$'
go vet -tags=integration ./...
```

真实内嵌 SeekDB 场景只使用上面的隔离套件。SeekDB engine 会按进程生命周期保留上游 C++ 后台线程；在业务断言已经 `PASS` 后，Go race runtime 可能停在 `runtime.racefini`，而 Go race 也不会检测未插桩的 SeekDB C++ 代码。因此仓库不提供“真实 SeekDB + Go race”伪发布门，也不得用超时强杀后截取到的 `PASS` 冒充成功。隔离脚本通过比较普通构建与 `integration` 构建的测试列表自动选择 integration-only 顶层测试，不会用“只跑已知用例名”的静态清单漏掉新增场景。

SeekDB 真库用例不能只看 `go test` 的进程退出码。上游内嵌库在启动失败路径可能调用 `_Exit(0)`，这会让测试二进制在断言尚未执行时以零状态提前结束。仓库使用父进程测试和 durable marker 证明 `Open`、完整 schema migration、readiness check 与逻辑 `Close` 都真正返回到 Go 宿主：

```bash
FAIRY_SEEKDB_LIBRARY=/absolute/path/to/libseekdb.dylib \
  go test -tags=integration ./runtime/seekdb \
  -run '^TestRealSeekDBMigrationReturnsToHostProcess$' -count=1 -v
```

只有父测试自身输出明确的 `--- PASS` 才算通过。子测试只有 `=== RUN` 后测试进程显示 `ok`、但没有写出最终 marker，属于内嵌库提前终止，必须判定失败。所有真实写入、召回、重建和重启套件都应在这道门通过后执行。

完整 Core/Edge 真库验收还有第二道父进程门，证明无 provider 的 endpoint-strict composition、本地管理投影与逻辑关闭均返回宿主：

```bash
FAIRY_SEEKDB_LIBRARY=/absolute/path/to/libseekdb.dylib \
  go test -tags=integration ./app/edge \
  -run '^TestEndpointStrictEdgeReturnsToHostProcess$' -count=1 -v
```

只有 SeekDB 基础门和 Edge composition 门都明确输出父测试 `--- PASS`，才可以继续把同一工件用于 Core/Edge 写入、召回、重建与重启套件。

Desktop 的 `task package`、可选 `release-darwin.sh` 与运行边界采集脚本都会调用 `build/verify-seekdb-runtime-darwin.sh`，以清空后的环境从实际 `.app/Contents/Frameworks/libseekdb.dylib` 打开完整 endpoint-strict Core/Edge。除了宿主完成门，它还会在私有空目录创建验收角色和 Desktop 会话，写入并完成一轮 Turn，写入 text-only 个人记忆与知识，逻辑关闭后用同一 App 重新打开，再逐项验证角色、profile、会话 identity、消息、记忆、知识与 FTS 召回均已恢复。该门同时确认加密 SecretStore 和 deny-by-default WASM Host 可用，而聊天、embedding 与 OpenSERP 在没有已保存配置时保持未配置且不构造 fallback。helper 即使以状态 0 提前退出，只要没有在私有临时目录写出 `host-completed`，打包或证据采集就会失败。只有两轮完整验证与两次逻辑 `Close` 返回后才会写 marker；marker 持久化后若 SeekDB v1.3 卡在 C++ 静态析构阶段，脚本会在固定宽限期后有界回收 helper，避免端侧验收无限阻塞。

## 真实第三方 provider smoke

真实 smoke 只通过 `live` build tag 启用。测试变量由受控 CI secret 或临时测试环境注入；测试会把 credential 保存进临时 `SecretStore` 后再走正式 endpoint-strict transport，生产 Desktop 本身仍不读取这些环境变量。不要把真实 key 写入仓库、Taskfile、命令历史或证据目录。

聊天 provider 需要同时提供：

- `FAIRY_CHAT_TEST_BASE_URL`：第三方 provider 的 HTTP(S) base URL，不得指向本机模型服务；
- `FAIRY_CHAT_TEST_MODEL`；
- `FAIRY_CHAT_TEST_API_KEY`；
- 可选 `FAIRY_CHAT_TEST_PROTOCOL`，缺省为 `chat_completions`，也可显式设为 `responses`。

Embedding provider 需要同时提供：

- `FAIRY_EMBEDDING_TEST_BASE_URL`：第三方 provider 的 HTTP(S) base URL，不得指向本机 embedding 服务；
- `FAIRY_EMBEDDING_TEST_MODEL`；
- `FAIRY_EMBEDDING_TEST_API_KEY`；
- 可选 `FAIRY_EMBEDDING_TEST_PROVIDER`，缺省为 `openai_compatible_api`。

在 `fairy` 目录运行：

```bash
go test -tags=live ./runtime/model -run '^TestLiveEndpoint' -count=1 -v
```

聊天 smoke 必须得到完整流式结束事件和严格回复 JSON；embedding smoke 必须得到等量、顺序一致的两条 1024 维有限向量以及非空 space identity。缺少全部 credential 时测试会明确 `SKIP`；只提供部分变量会失败。`SKIP` 只能证明验收入口存在，不能勾选第三方 provider 的真实验收任务。

完成两个 provider 的独立 smoke 后，还必须在同一个 endpoint-strict Edge 中执行联合验收。除上述变量外，先显式启动 OpenSERP，并同时设置 `FAIRY_OPENSERP_TEST_ORIGIN` 与最终 App 内同一 verified SeekDB dylib：

```bash
FAIRY_SEEKDB_LIBRARY="$PWD/../desktop/bin/FAIRY.app/Contents/Frameworks/libseekdb.dylib" \
FAIRY_OPENSERP_TEST_ORIGIN=http://127.0.0.1:7070 \
  go test -tags='integration live' ./app/edge \
  -run '^TestLiveEndpointStrictConversationSemanticMemoryIsolationAndRestart$' \
  -count=1 -v
```

该用例从私有空 profile 首次启动，通过正式管理 API 把环境提供的测试配置和 credential 写入临时 SecretStore；随后重新打开同一 Edge，完成真实聊天 Turn、第三方 1024 维向量记忆写入/召回、OpenSERP 搜索/正文提取、关闭与重启恢复。它还会分别替换聊天和 embedding credential 触发 fail-closed，并把 OpenSERP 保存为不可达 origin 后再次重启，验证只有对应能力不可用，SeekDB、本地历史、既有记忆、配置和管理仍可用；最后恢复模型 credential，证明没有选用本地模型或其他 provider fallback。

联合用例要求聊天、embedding 与 OpenSERP 三组变量同时完整；全部缺失时明确 `SKIP`，部分缺失则失败。测试不会启动 OpenSERP，也不会把 credential 写进日志或证据目录。它仍是源码层的真实服务验收，不能替代实际组装 App 在干净机器上的 UI 操作与 `observe-release-darwin.sh` 运行边界证据。

为避免在 credential 缺失或失效时重复运行耗时的本地套件，续跑顺序固定为：先分别执行两个 `runtime/model` live smoke；任一项 `SKIP`、401、维度错误或 provider 合同失败时立即停止，只修复对应第三方配置；两项都通过后再执行联合 Edge 用例；最后才对实际 App 采集两轮运行边界证据。Provider 失败不要求重跑已经通过且相关代码未变化的 SeekDB schema、package 或前端全量套件。

## 可选公开发行验收

需要把 App 分发给其他 macOS 用户且避免 Gatekeeper 警告时，才执行 Apple 发行流程：

```bash
task release

build/observe-release-darwin.sh \
  --app /Applications/FAIRY.app \
  --chat-origin https://chat-provider.example \
  --embedding-origin https://embedding-provider.example \
  --openserp-origin https://openserp.example \
  --require-public-distribution \
  --duration 60 \
  --runs 2 \
  --output /tmp/fairy-public-release-evidence
```

该模式才要求 `FAIRY_CODESIGN_IDENTITY`、`FAIRY_NOTARY_PROFILE`、Developer ID、公证、staple 与 Gatekeeper。它是发行附加门，不改变 `endpoint-strict` 的运行依赖定义。

## 真实 OpenSERP smoke

OpenSERP 由用户独立启动，FAIRY 不拉起其进程。将其规范化 origin 显式传给 live 测试：

```bash
FAIRY_OPENSERP_TEST_ORIGIN=http://127.0.0.1:7070 \
  go test -tags=live ./context/knowledge \
  -run '^TestLiveOpenSERPSearchAndExtractionUseDeclaredOrigin$' -count=1 -v
```

测试通过正式 `WebSearchService` 与 composition-owned OpenSERP authority 依次执行 readiness、`/mega/search` 聚合搜索和正文代理提取；聚合路由只在同一个 OpenSERP 实例内选择其已就绪的搜索引擎，不会扩展 FAIRY 的网络 authority。搜索结果 URL 只作为 `/extract` 参数，FAIRY 不直接连接结果站点。测试不会启动 OpenSERP，也不会从 `FAIRY_OPENSERP_URL`、系统代理或其他环境 endpoint 推导 authority。未提供 `FAIRY_OPENSERP_TEST_ORIGIN` 时只会明确 `SKIP`，不能冒充真实 OpenSERP 验收。
