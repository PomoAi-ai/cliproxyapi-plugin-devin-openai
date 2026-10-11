# Devin OpenAI 网关插件

为 CLIProxyAPI v8.0.23 添加独立的 `devin-openai` provider，通过 Devin OpenAI-compatible 网关调用模型。它不替换 CPA 内置的 `devin` Connect-RPC 通道。

插件提供认证、模型发现和账号额度查询；推理由 CPA 原生 OpenAICompatExecutor 负责，包括协议转换、流式响应、工具调用、用量统计和账号调度。不增加自己的代理服务。

## 社区核查

2026-10-11 检索结果：

- [CPA 官方插件商店](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store/blob/main/registry.json) 的 114 项登记中未发现 Devin/Cognition/Codeium/Windsurf 网关插件。
- [历史 Codeium 插件](https://github.com/senran-N/cliproxyapi-codeium) 在搜索索引中有记录，但仓库返回 404，无法确认实现或安装。
- [通用 OpenAI OAuth 插件](https://github.com/syepes/CLIProxyAPI-plugin-openai-oauth) 使用 client_credentials，不支持 Devin 浏览器 PKCE 登录。
- 本插件按 [@cognitionai/opencode-devin 0.3.0](https://www.npmjs.com/package/%40cognitionai/opencode-devin) 发布包公开协议独立实现：PKCE 登录、Bearer 认证、`GET /api/v1/models` 与 OpenAI-compatible 推理。

这表示未找到当前可用的同类公开插件，不意味着能证明所有私有或未索引项目都不存在。

## 接入方式

```text
客户端 → CPA 标准 API → 原生 OpenAICompatExecutor → Devin /api/v1/chat/completions
                       ↑
          devin-openai 插件：认证 + 按账号模型目录
```

默认网关为 `https://server.codeium.com`，登录页面为 `https://app.devin.ai`，授权码交换地址为 `https://api.devin.ai/auth/cli/token`。

插件不删除或改写系统提示来处理审核拒绝，也不在网关失败时切换 Connect-RPC。网关权限、模型权益及拒绝策略由 Devin 决定。使用 OpenAI-compatible 协议本身不能保证所有客户端的 subagent 功能都兼容。

## 启用配置

以下是供审核的配置片段，不是现有生产配置的替换文件。生产安装、发布和重启需要另外授权。

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    devin-openai:
      enabled: true
```

插件文件应命名为 `devin-openai.dylib`（macOS）或 `devin-openai.so`（Linux），放在宿主插件目录。使用支持动态库的 CPA 发行包，不能使用 `no-plugin` 包。

源码仓库为 [PomoAi-ai/cliproxyapi-plugin-devin-openai](https://github.com/PomoAi-ai/cliproxyapi-plugin-devin-openai)，插件元数据指向此地址。当前提供源码，尚未提供 Release 动态库或登记插件商店，不承诺商店安装或在线更新。

不要为此 provider 再添加 `openai-compatibility` 配置；认证解析会提供 `base_url` 和 `api_key`，宿主为独立 provider 选择原生转发器。插件不设置 `compat_name`，避免 CPA 将执行器归入另一命名空间而与按账号注册的模型不匹配。

### 已有凭据导入

在 CPA 配置的 `oauth.auth-dir` 中创建独立认证文件（例如 `devin-openai-account.json`），权限设为仅账号所有者可读写。真实凭据不放在本源码目录，不写入提交或命令历史。

```json
{
  "type": "devin-openai",
  "api_key": "<通过安全方式填入 Devin session token 或网关 service key>",
  "label": "Devin OpenAI gateway"
}
```

可选字段为 `inference_url`、`proxy_url`、`disabled`。`inference_url` 是网关 origin（不含 `/api/v1`），默认使用官方地址；只接受 HTTPS 或回环 HTTP。推理端点由此 origin 拼接 `/api/v1`。不要直接修改原有 `type: devin` 文件。

`api_key` 必须是可直接作为 Bearer 凭据的完整值：session token 需要保留 `devin-session-token$` 前缀，不能只填原始 JWT；service key 使用其完整 `cog_...` 值。插件自己的 PKCE 登录会自动添加 session token 前缀。

模型来自该账号实际返回的目录，具体 variant UID 也作为可调用模型暴露。调用时使用 `devin-openai/<模型ID>`，以隔离原有通道。没有硬编码兜底模型；目录失败会报告错误。

### 浏览器 PKCE 登录

可通过 CPA 管理 API 完成无需回调监听端口的登录。以下路径均为现有 CPA 管理接口，按宿主要求携带管理认证：

1. `GET /v8/management/oauth/auth-url?provider=devin-openai`，取得 `url` 和 `state`。
2. 在浏览器打开 `url`，完成 Devin 登录并复制网页返回的授权码。
3. `POST /v8/management/oauth/callback`，JSON 请求体为 `{"provider":"devin-openai","state":"第一步的state","code":"网页授权码"}`。
4. `GET /v8/management/oauth/status?state=第一步的state`，直到成功或明确错误。成功后由 CPA 保存独立认证文件。

登录状态及 PKCE verifier 只保留在插件内存，重启插件后需重新开始登录。授权码只能使用一次。session token 没有已公开的刷新协议；遇到 401 应重新登录，插件不会伪造刷新成功。

管理面板对自定义 provider 的 UI 支持取决于面板版本；上述管理 API 是确定的入口，不承诺现有 CPAMP 会额外出现登录按钮。

## 额度查询

从 0.2.0 起提供 CPA `QuotaProvider`：查询 Devin seat-management 的日／周剩余百分比、重置时间及套餐名称。该信息属于账号套餐额度，**不是 AI Gateway 独立余额，也不证明账号具有网关权限**。即使额度有剩余，模型目录或推理仍可能因缺少 AI Gateway 权限返回 403。

额度查询只支持 `devin-session-token$…` 凭据；service key 尚无已确认的额度协议，会明确返回不支持。插件不提供额度重置功能。上游错误或损坏响应会报错，不伪装为成功或无限额度。

管理端可通过 `GET /v8/management/plugins` 发现 `supports_quota` 和 `quota_provider`，再按 CPA 的插件额度接口传入目标账号 `auth_index` 查询。面板能否自动显示入口取决于其对通用插件额度的支持。

在 CPA v8.0.23 中，可先通过 `GET /v8/management/credentials` 获取账号的 `auth_index`，再调用 `POST /v8/management/plugins/devin-openai/quota`，正文为 `{"auth_index":"目标账号索引"}`。两步均需管理认证。返回的 `groups[].buckets[]` 包含 `remainingFraction`（0 到 1）和可选 `resetTime`（UTC）。

## Claude Messages 与思考参数

Claude API 客户端可使用 CPA 的 `/v1/messages`，选择 `devin-openai/<模型ID>`；CPA 将请求转换为网关使用的 OpenAI Chat Completions，并转换返回的工具调用、流式内容及 reasoning。它不意味着网关提供原生 Anthropic `/messages` 端点。

模型目录没有声明可接受的 thinking budget 或 reasoning effort 范围。插件将这些模型标记为 CPA 自定义模型，让 CPA 保留用户显式思考意图并交给上游验证，不再因缺少 `Thinking` 声明而删除参数。Claude 的 `thinking` / `output_config.effort` 由 CPA 按其现有规则转换；不同协议的预算不保证逐 token 等价。

网关 `available_variants` 中的 UID 是实际模型选择。插件保留其 UID、展示名及上下文限制，不根据 High/Fast 等名称编造 effort 档位。需要指定某个变体时直接选该 UID；参数被保留不代表上游一定接受它。

CPA v8.0.23 的转换存在边界：网关 reasoning 可以转换为 Claude thinking 内容，但不会生成 Anthropic 原生签名；无签名 thinking 历史再次提交时可能被宿主过滤。因此不能承诺 Claude 原生签名或完整 thinking 历史无损往返。

## 代理与安全边界

- 登录换 token 和模型目录使用宿主 `host.http.do`，遵循 CPA **全局代理**。v8.0.23 的该 C ABI 回调不会应用账号级 `proxy_url`。
- 推理由原生 executor 执行，遵循其账号代理规则。若网络必须使用指定代理，请确认全局代理与账号配置满足实际网络要求。
- 凭据只进入 CPA 认证存储与运行时认证对象，不写入插件配置、业务日志或模型目录。
- 不修改既有账号的冷却设置；重试、冷却和调度仍由 CPA 控制，插件不承诺覆盖宿主错误策略。
- 网关模型目录与 CLI 私有目录可能不同，以网关对当前账号返回的目录为准。

## 本地开发验证

插件 C ABI 不依赖宿主 Go module；额度协议使用 `google.golang.org/protobuf/encoding/protowire` 解析二进制字段。模块最低声明 Go 1.22，需要 C 编译器；本次开发检查使用 Go 1.25.1。

```sh
go test ./...
go vet ./...
```

用于隔离开发验证的动态库可编译到临时目录，例如 macOS：

```sh
CGO_ENABLED=1 go build -buildmode=c-shared -o /private/tmp/devin-openai.dylib .
uv run --no-project python3 tools/verify_host.py \
  --host /path/to/cli-proxy-api \
  --plugin /private/tmp/devin-openai.dylib
```

验证工具使用临时目录、回环模拟网关和虚构凭据，退出时清理测试宿主。不使用生产账号，不发真实模型请求。Linux 验证时动态库后缀为 `.so`。

本次已通过 `go test ./...`、`go test -race ./...` 和 `go vet ./...`；独立代码审查已通过。使用官方 CPA v8.0.23 macOS arm64 宿主实际加载动态库后，11 次本地模拟请求验证通过：

- 双账号模型目录与凭据隔离。
- Chat Completions 非流式、SSE 流式的工具调用与工具结果往返。
- Messages API 到 OpenAI 网关的非流式工具调用与结果转换。
- 系统提示内容、工具 ID 和结果保留，上游 403 明确返回。

另已验证 CI 构建的 Linux amd64 动态库可由 CPA v8.0.23 热加载，管理接口确认注册、启用和 OAuth 能力。

0.2.0 的扩展验证通过 17 次本地模拟推理请求及额度查询：额外检查显式 effort、模型后缀 effort、Claude budget/adaptive 转换、Messages thinking SSE 与工具往返，以及日 75%／周 0% 的剩余额度和重置时间。service key 查询额度明确失败且不泄露凭据。

这些检查覆盖插件接入和协议传递；尚未验证真实 Devin 登录、网关账号权益，或 Claude Code/OpenCode 的实际 subagent 端到端任务。

## Linux 构建产物

在 GitHub Actions 手动运行 `Build Linux plugin`，成功后下载 `devin-openai-linux-amd64` artifact，包含 `devin-openai.so` 和 `SHA256SUMS`。编译使用 Go 1.25.1 / Debian 12，仅支持 Linux amd64；安装前运行 `sha256sum -c SHA256SUMS`。

全局插件已启用时，将动态库放到 `<plugins.dir>/linux/amd64/devin-openai.so`，再通过管理认证调用 `PATCH /v0/management/plugins/devin-openai/enabled`，请求体为 `{"enabled":true}`，即可触发热加载。通过 `GET /v8/management/plugins` 确认 `registered`、`effective_enabled`、`supports_oauth` 均为 `true`。单独停用该插件时将 enabled 改为 false。

升级已有插件时，不覆盖正在加载的动态库：将新库放到版本化路径，例如 `devin-openai-v0.2.0.so`。单独停用该插件并确认 `effective_enabled=false` 后重新启用，核对元数据版本与 `supports_quota=true`；重复提交相同的 enabled=true 配置不保证触发重新加载。保留旧库及配置备份供回退。

发布采用获授权后的 CI/CD 构建；这里没有创建发布包、镜像或自动部署流程，也没有将测试加入 CI。
