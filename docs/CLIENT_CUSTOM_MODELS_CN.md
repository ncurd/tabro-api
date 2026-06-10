# 自定义模型客户端接入指南

本文说明如何把 Tabro 的接口接入 VS Code 自定义模型、Claude Code、Codex，以及如何通过 CC Switch 管理 Claude Code / Codex 的供应商配置。

## 准备信息

先准备以下值：

| 名称 | 示例 | 说明 |
|------|------|------|
| Tabro Base URL | `https://tabro.example.com` | 不带末尾 `/` |
| API Key | `sk-xxxx` | 在 Tabro 用户端创建，并确保 Key 所属分组已绑定正确平台 |
| OpenAI Responses Base | `https://tabro.example.com/v1` | Codex 等 OpenAI 兼容客户端使用 |
| OpenAI Responses Endpoint | `https://tabro.example.com/v1/responses` | VS Code Custom Endpoint 需要完整 URL |
| Claude Messages Base | `https://tabro.example.com` | Claude Code 会自动拼接 `/v1/messages` |
| Claude Messages Endpoint | `https://tabro.example.com/v1/messages` | VS Code Messages API 使用 |
| Antigravity Claude Base | `https://tabro.example.com/antigravity` | Antigravity Claude 专用入口 |
| Antigravity Claude Endpoint | `https://tabro.example.com/antigravity/v1/messages` | VS Code Messages API 使用 |

常用模型示例：

| 场景 | 模型示例 |
|------|----------|
| OpenAI / Codex 兼容 | `gpt-5.4` |
| Claude / Anthropic 兼容 | `claude-fable-5`、`claude-sonnet-4-6` |
| Antigravity Claude | `claude-fable-5`、`claude-opus-4-6-thinking` |

如果你给 Key 配了模型白名单或模型映射，客户端里的模型 ID 必须能被该分组和账号支持。

## VS Code Custom Endpoint

VS Code 的 BYOK 模型通过 **Chat: Manage Language Models** 管理。官方文档里 Custom Endpoint 支持 `chat-completions`、`responses`、`messages` 三种 API 类型，并会打开 `chatLanguageModels.json` 让你编辑模型配置。

注意：VS Code 官方文档目前把 Custom Endpoint 标为 Insiders 功能，并说明它替代旧的 OpenAI Compatible provider。若你的稳定版 VS Code 看不到 `Custom Endpoint`，请切换 VS Code Insiders，或使用文档里提到的语言模型 provider 扩展方案。

### 推荐入口

1. 打开 VS Code 命令面板。
2. 运行 `Chat: Manage Language Models`。
3. 点击 `Add Models`。
4. 选择 `Custom Endpoint`。
5. 输入分组名，例如 `Tabro`。
6. 输入显示名和 API Key。
7. 根据要接入的接口选择 API Type：
   - OpenAI / Codex 模型：选择 `Responses`。
   - Claude / Antigravity Claude 模型：选择 `Messages`。
8. VS Code 打开 `chatLanguageModels.json` 后，按下面示例调整 `models`。
9. 保存文件，在 Chat 模型选择器中选择对应模型。如果模型没有出现，重启 VS Code。

### OpenAI Responses 示例

用于 Tabro 的 OpenAI 兼容入口，推荐给 `gpt-5.4`、`gpt-5.4-mini` 等模型。

```json
[
  {
    "name": "Tabro",
    "vendor": "customendpoint",
    "apiKey": "sk-xxxx",
    "apiType": "responses",
    "models": [
      {
        "id": "gpt-5.4",
        "name": "Tabro GPT-5.4",
        "url": "https://tabro.example.com/v1/responses",
        "toolCalling": true,
        "vision": true,
        "thinking": true,
        "supportsReasoningEffort": ["low", "medium", "high", "xhigh"],
        "maxInputTokens": 1000000,
        "maxOutputTokens": 128000
      }
    ]
  }
]
```

### Claude Messages 示例

用于 Tabro 的 Anthropic Messages 兼容入口。

```json
[
  {
    "name": "Tabro Claude",
    "vendor": "customendpoint",
    "apiKey": "sk-xxxx",
    "apiType": "messages",
    "models": [
      {
        "id": "claude-fable-5",
        "name": "Claude Fable 5",
        "url": "https://tabro.example.com/v1/messages",
        "toolCalling": true,
        "vision": true,
        "thinking": true,
        "maxInputTokens": 1000000,
        "maxOutputTokens": 128000
      }
    ]
  }
]
```

### Antigravity Claude 示例

用于已授权 Antigravity 账号的 Claude 专用入口。

```json
[
  {
    "name": "Tabro Antigravity Claude",
    "vendor": "customendpoint",
    "apiKey": "sk-xxxx",
    "apiType": "messages",
    "models": [
      {
        "id": "claude-fable-5",
        "name": "Claude Fable 5 via Antigravity",
        "url": "https://tabro.example.com/antigravity/v1/messages",
        "toolCalling": true,
        "vision": true,
        "thinking": true,
        "maxInputTokens": 1000000,
        "maxOutputTokens": 128000
      }
    ]
  }
]
```

### VS Code 常见问题

- **模型不显示**：确认 `toolCalling` 为 `true`，保存后重启 VS Code。
- **401/403**：确认 API Key 可用，并用 curl 先测通：

  ```bash
  curl https://tabro.example.com/v1/models \
    -H "Authorization: Bearer sk-xxxx"
  ```

- **404/405**：VS Code Custom Endpoint 的 `url` 要填完整 endpoint，例如 `/v1/responses` 或 `/v1/messages`，不是只填 `/v1`。
- **工具/Agent 不可用**：VS Code Chat Agent 的模型需要支持 tool calling。对轻量工具任务，可在设置中把 `chat.utilityModel` 和 `chat.utilitySmallModel` 指向已添加的 Tabro 模型。
- **企业版策略限制**：Copilot Business / Enterprise 可能需要管理员允许 BYOK。

## Claude Code 直接配置

Claude Code 使用 Anthropic 兼容入口。Tabro API Key 推荐放在 `ANTHROPIC_AUTH_TOKEN`。

普通 Claude / Anthropic 分组：

```bash
export ANTHROPIC_BASE_URL="https://tabro.example.com"
export ANTHROPIC_AUTH_TOKEN="sk-xxxx"
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
```

Antigravity Claude 分组：

```bash
export ANTHROPIC_BASE_URL="https://tabro.example.com/antigravity"
export ANTHROPIC_AUTH_TOKEN="sk-xxxx"
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
```

也可以写入 `~/.claude/settings.json`：

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "https://tabro.example.com/antigravity",
    "ANTHROPIC_AUTH_TOKEN": "sk-xxxx",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "CLAUDE_CODE_ATTRIBUTION_HEADER": "0"
  }
}
```

注意：Claude Code 的 `ANTHROPIC_BASE_URL` 是 base URL，不要写成 `/v1/messages`。

## Codex 直接配置

Codex CLI / IDE 扩展共用 `~/.codex/config.toml`。Tabro 推荐使用 Responses API。

`~/.codex/config.toml`：

```toml
model_provider = "tabro"
model = "gpt-5.4"
review_model = "gpt-5.4"
model_reasoning_effort = "xhigh"
disable_response_storage = true
network_access = "enabled"
model_context_window = 1000000
model_auto_compact_token_limit = 900000

[model_providers.tabro]
name = "Tabro"
base_url = "https://tabro.example.com/v1"
wire_api = "responses"
requires_openai_auth = true
```

`~/.codex/auth.json`：

```json
{
  "OPENAI_API_KEY": "sk-xxxx"
}
```

注意：

- Codex 的 `base_url` 是 OpenAI API base，填到 `/v1` 即可，不要填 `/v1/responses`。
- Codex 当前仍可指向支持 Chat Completions 或 Responses 的自定义 provider，但 Chat Completions 支持已在官方文档中标记为 deprecated，建议优先使用 Responses。
- 如果通过 Nginx 反代 Tabro 并使用 Codex，建议确认 Nginx 配置了 `underscores_in_headers on;`，否则包含下划线的会话相关 header 可能被丢弃。

## 通过 CC Switch 管理 Claude Code 和 Codex

CC Switch 是一个桌面配置管理器，支持 Claude Code、Codex、Gemini CLI、OpenCode 等工具。它会写入对应工具的配置文件，所以适合多供应商切换。

### 安装

从官方 Release 下载对应系统版本：

```text
https://github.com/farion1231/cc-switch/releases
```

安装后打开 CC Switch。

### 添加 Claude Code 供应商

1. 左侧选择 `Claude Code`。
2. 点击右上角 `+`。
3. 选择 `App-specific Provider`。
4. 选择 `Custom` 预设。
5. 填写名称，例如 `Tabro Antigravity Claude`。
6. Endpoint 填：
   - 普通 Claude：`https://tabro.example.com`
   - Antigravity Claude：`https://tabro.example.com/antigravity`
7. API Key 填 `sk-xxxx`。
8. 如果 CC Switch 打开 JSON 编辑区，可使用：

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "https://tabro.example.com/antigravity",
    "ANTHROPIC_AUTH_TOKEN": "sk-xxxx",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "CLAUDE_CODE_ATTRIBUTION_HEADER": "0"
  }
}
```

保存后启用该供应商。CC Switch 文档说明 Claude Code 支持热切换，通常不需要重启。

### 添加 Codex 供应商

1. 左侧选择 `Codex`。
2. 点击右上角 `+`。
3. 选择 `App-specific Provider`。
4. 选择 `Custom` 或 OpenAI/Responses 兼容预设。
5. Endpoint 填 `https://tabro.example.com/v1`。
6. API Key 填 `sk-xxxx`。
7. Model 填 `gpt-5.4`，或填你在 Tabro 分组里允许的模型。
8. 协议选择 Responses；如果有 `Needs Local Routing` / 本地路由映射开关，Tabro Responses 入口不需要开启。
9. 如果 CC Switch 打开配置编辑区，可用：

```toml
model_provider = "tabro"
model = "gpt-5.4"
model_reasoning_effort = "xhigh"
disable_response_storage = true

[model_providers.tabro]
name = "Tabro"
base_url = "https://tabro.example.com/v1"
wire_api = "responses"
requires_openai_auth = true
```

并确保 `auth.json` 有：

```json
{
  "OPENAI_API_KEY": "sk-xxxx"
}
```

保存后启用该供应商。CC Switch 文档说明 Codex 切换后需要重启终端或重新打开 Codex。

### 自动获取模型

CC Switch 的添加/编辑供应商界面支持 `Fetch Models`。它会用配置的 API Key 调 OpenAI 兼容的 `/v1/models`：

```bash
curl https://tabro.example.com/v1/models \
  -H "Authorization: Bearer sk-xxxx"
```

如果返回正常，CC Switch 就能从下拉菜单选择模型。如果报 404/405，说明当前填写的 endpoint 不符合它拼接 `/v1/models` 的预期，可以手动输入模型 ID。

### Universal Provider

如果同一个 Tabro Key 同时用于 Claude Code 和 Codex，可以在 CC Switch 中创建 `Universal Provider`：

1. 切到 `Universal Provider`。
2. 填名称、API Key、Endpoint。
3. 勾选要同步的应用，例如 Claude Code 和 Codex。
4. 保存并同步。

需要注意，不同应用对 endpoint 的语义不同：

| 应用 | Endpoint 语义 | 推荐值 |
|------|---------------|--------|
| Claude Code | Anthropic base URL | `https://tabro.example.com` 或 `https://tabro.example.com/antigravity` |
| Codex | OpenAI API base URL | `https://tabro.example.com/v1` |

如果 Universal Provider 无法同时满足两边 URL，建议分别创建 Claude Code 和 Codex 的应用专属供应商。

## 快速排障

| 现象 | 优先检查 |
|------|----------|
| VS Code 401/403 | API Key、Key 所属分组、请求是否带 `Authorization: Bearer sk-xxxx` |
| VS Code 404/405 | `url` 是否填完整 endpoint，例如 `/v1/responses` 或 `/v1/messages` |
| VS Code 模型不显示 | `toolCalling: true`，保存后重启 VS Code |
| Claude Code 404 | `ANTHROPIC_BASE_URL` 不要带 `/v1/messages` |
| Claude Code 401 | `ANTHROPIC_AUTH_TOKEN` 是否是 Tabro API Key |
| Codex 404 | `base_url` 应为 `https://tabro.example.com/v1`，不是 `/v1/responses` |
| Codex 切换后仍旧配置 | 重启终端或重新启动 Codex |
| CC Switch 获取模型失败 | 用 curl 测 `/v1/models`，失败则手动输入模型 ID |
| 走错上游账号 | 检查 Tabro 后台 Key 所属分组、账号平台、模型映射和白名单 |

## 参考

- VS Code 官方文档：AI language models in VS Code, BYOK, Custom Endpoint, `chatLanguageModels.json`  
  https://code.visualstudio.com/docs/agent-customization/language-models
- OpenAI Codex 官方文档：配置文件、模型选择、自定义 model provider  
  https://developers.openai.com/codex/config-basic  
  https://developers.openai.com/codex/config-advanced  
  https://developers.openai.com/codex/models
- CC Switch 官方文档：添加供应商、自动获取模型、Claude Code / Codex 配置格式、切换生效方式  
  https://github.com/farion1231/cc-switch/blob/main/docs/user-manual/zh/2-providers/2.1-add.md  
  https://github.com/farion1231/cc-switch/blob/main/docs/user-manual/zh/2-providers/2.2-switch.md
