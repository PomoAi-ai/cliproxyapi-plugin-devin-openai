# Devin OpenAI 网关插件

为 CLIProxyAPI v8.0.23 添加独立的 `devin-openai` provider，通过 Devin OpenAI-compatible 网关调用模型。它不替换 CPA 内置的 `devin` Connect-RPC 通道。

实现仅包含认证与模型发现；推理由 CPA 原生 OpenAICompatExecutor 负责，包括协议转换、流式响应、工具调用、用量统计和账号调度。不增加自己的代理服务。

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

## 代理与安全边界

- 登录换 token 和模型目录使用宿主 `host.http.do`，遵循 CPA **全局代理**。v8.0.23 的该 C ABI 回调不会应用账号级 `proxy_url`。
- 推理由原生 executor 执行，遵循其账号代理规则。若网络必须使用指定代理，请确认全局代理与账号配置满足实际网络要求。
- 凭据只进入 CPA 认证存储与运行时认证对象，不写入插件配置、业务日志或模型目录。
- 不修改既有账号的冷却设置；重试、冷却和调度仍由 CPA 控制，插件不承诺覆盖宿主错误策略。
- 网关模型目录与 CLI 私有目录可能不同，以网关对当前账号返回的目录为准。

## 本地开发验证

插件仅使用 Go 标准库，C ABI 不依赖宿主 Go module。模块最低声明 Go 1.22，需要 C 编译器；本次开发检查使用 Go 1.25.1。

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

这些检查覆盖插件接入和协议传递；尚未验证真实 Devin 登录、网关账号权益，或 Claude Code/OpenCode 的实际 subagent 端到端任务。

## Linux 构建产物

在 GitHub Actions 手动运行 `Build Linux plugin`，成功后下载 `devin-openai-linux-amd64` artifact，包含 `devin-openai.so` 和 `SHA256SUMS`。编译使用 Go 1.25.1 / Debian 12，仅支持 Linux amd64；安装前运行 `sha256sum -c SHA256SUMS`。

全局插件已启用时，将动态库放到 `<plugins.dir>/linux/amd64/devin-openai.so`，再通过管理认证调用 `PATCH /v0/management/plugins/devin-openai/enabled`，请求体为 `{"enabled":true}`，即可触发热加载。通过 `GET /v8/management/plugins` 确认 `registered`、`effective_enabled`、`supports_oauth` 均为 `true`。单独停用该插件时将 enabled 改为 false。

发布采用获授权后的 CI/CD 构建；这里没有创建发布包、镜像或自动部署流程，也没有将测试加入 CI。
