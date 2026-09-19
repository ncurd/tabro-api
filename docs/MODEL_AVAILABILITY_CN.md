# 模型可用性清理记录

核查日期：**2026-09-19**。

本记录对应仓库默认模型目录、账号模型选择器、预设映射和默认测试模型的清理。依据为厂商正式停服公告，范围限于本次实际维护的列表；它不是所有模型、地区和代理渠道的实时可用性保证。

## 清理规则

- 区分“已停服”和“已弃用”。停服清理要求确认具体模型 ID、渠道与生效日期；Codex 已弃用模型另从 OAuth 默认推荐项中排除，不能据此宣称其他渠道也已停服。
- OpenAI API Key 与 ChatGPT OAuth 分开判断；Claude 官方 API 与 Bedrock、Antigravity 等渠道分别处理。
- 保留用户已保存的配置、手动输入模型 ID 的能力及历史定价。目录过滤依据映射的上游目标；旧名称映射到仍可用模型的自定义别名可以保留。
- 本次清理不批量改写已有账号映射，不把显式请求的旧模型自动迁移到更贵的模型。默认值的更新只作用于未配置相应选项的路径。
- 某托管平台停止供应开源模型，不代表该模型在自建服务或其他供应商处不可用。

## OpenAI

### API 已停服条目

以下模型从相关预设删除，或纳入后端目录过滤；对应日期均已早于核查日。依据：[OpenAI API 停服记录](https://developers.openai.com/api/docs/deprecations)。

| 停服日期 | 本次涉及的模型 ID |
| --- | --- |
| 2025-07-14 | `gpt-4.5-preview`、`gpt-4.5-preview-2025-02-27` |
| 2025-07-28 | `o1-preview`、`o1-preview-2024-09-12` |
| 2025-10-27 | `o1-mini`、`o1-mini-2024-09-12` |
| 2026-02-12 | `codex-mini-latest` |
| 2026-02-17 | `chatgpt-4o-latest` |
| 2026-03-26 | `gpt-4-0314`、`gpt-4-0125-preview`、`gpt-4-turbo-preview` |
| 2026-05-07 | `gpt-4o-audio-preview`、`gpt-4o-realtime-preview`、`gpt-4o-mini-audio-preview`、`gpt-4o-mini-realtime-preview` |
| 2026-05-12 | `dall-e-2`、`dall-e-3` |
| 2026-07-23 | `gpt-5-chat-latest`、`gpt-5-codex`、`gpt-5.1-chat-latest`、`gpt-5.1-codex`、`gpt-5.1-codex-max`、`gpt-5.1-codex-mini`、`gpt-5.2-codex` |
| 2026-08-10 | `gpt-5.2-chat-latest`、`gpt-5.3-chat-latest` |

### ChatGPT OAuth / Codex

`gpt-5.4`、`gpt-5.4-mini` 已于 **2026-08-31** 在使用 ChatGPT 账号的 Codex 中退役；`gpt-5.2`、`gpt-5.3-codex` 被官方列为该渠道已弃用模型，本次从 OAuth 默认推荐项排除。后两者的该页面没有给出精确停服日期。公告明确区分了 API Key 调用，不能据此从 API Key 目录删除仍可用的同名模型。依据：[Codex 模型与退役说明](https://learn.chatgpt.com/docs/models)。

OAuth 目录同时移除本地适配器中会解析到上述旧 Codex 模型的名称，包括 GPT-5、GPT-5.1、GPT-5.2 的旧别名/快照、`gpt-5.3-codex-spark`、`gpt-5.4-pro`、`gpt-5.4-2026-03-05`。这些别名的处理依据包含仓库适配逻辑，不表示它们在所有 OpenAI API 或代理渠道均已停服。

图像工具的宿主模型及 OpenAI 默认账号测试模型改为 `gpt-5.6-luna`；新增 `gpt-image-2.5-sunburst`、`gpt-image-2.5-flare` 及其 `2026-09-08` 快照。请求中的图像模型继续传入图像工具，宿主模型和图像模型分别设置。

### 尚未到停服日期的条目

保留 `gpt-image-1`（2026-10-23 停服），以及 `gpt-image-1-mini`、`gpt-image-1.5`、`chatgpt-image-latest`（2026-12-01 停服）的现有支持。GPT-5.5 的 Codex 退役日期为 2026-10-14，也未到期。其他未来停服条目不提前删除。依据：[API 时间表](https://developers.openai.com/api/docs/deprecations)、[Codex 时间表](https://learn.chatgpt.com/docs/models)。

## Anthropic Claude

从 Claude 官方渠道预设移除以下 **13 个**已退役 ID，并在账号目录中过滤相应上游目标。依据：[Anthropic 模型退役记录](https://platform.claude.com/docs/en/about-claude/model-deprecations)。

| 退役日期 | 模型 ID |
| --- | --- |
| 2024-11-06 | `claude-instant-1.2` |
| 2025-07-21 | `claude-2.0`、`claude-2.1`、`claude-3-sonnet-20240229` |
| 2025-10-28 | `claude-3-5-sonnet-20240620`、`claude-3-5-sonnet-20241022` |
| 2026-01-05 | `claude-3-opus-20240229` |
| 2026-02-19 | `claude-3-5-haiku-20241022`、`claude-3-7-sonnet-20250219` |
| 2026-04-20 | `claude-3-haiku-20240307` |
| 2026-06-15 | `claude-sonnet-4-20250514`、`claude-opus-4-20250514` |
| 2026-08-05 | `claude-opus-4-1-20250805` |

Bedrock 使用独立列表：依据 AWS 自身的 **2026-06-19 EOL** 公告，另外移除 `claude-3-5-haiku-20241022`（上游 ID 为 `anthropic.claude-3-5-haiku-20241022-v1:0`）。其他旧模型需按区域和推理方式核对，不能把 Anthropic 官方退役日期直接套用于合作方渠道。[AWS Haiku 3.5 模型卡](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-3-5-haiku.html)、[AWS 区域可用性表](https://docs.aws.amazon.com/bedrock/latest/userguide/models-region-compatibility.html)

未配置的 Claude 回退模型使用 `claude-sonnet-4-6`，已有设置保留。

## Google Gemini

移除默认目录和预设中的 `gemini-2.0-flash`；账号目录同时识别已停服快照 `gemini-2.0-flash-001`。两者于 **2026-06-01** 停服。默认账号测试模型改为 `gemini-2.5-flash`。依据：[Gemini 2026-06-01 更新](https://ai.google.dev/gemini-api/docs/changelog#june-1-2026)。

保留 `gemini-3-pro-preview`：旧模型虽已退出，但这个名称仍作为 `gemini-3.1-pro-preview` 的别名。保留 Gemini 2.5 Flash/Pro；`gemini-2.5-flash-image` 的停服日期为 2026-10-02，核查时尚未到期。Antigravity 的既有别名及转换按该渠道单独处理。依据：[别名更新](https://ai.google.dev/gemini-api/docs/changelog#march-9-2026)、[Gemini 停服时间表](https://ai.google.dev/gemini-api/docs/deprecations)。

## 其他厂商：确认删除的托管服务预设

| 厂商 / 渠道 | 已删除 ID | 停服日期与官方依据 |
| --- | --- | --- |
| Perplexity | `llama-3-sonar-small-32k-online`、`llama-3-sonar-large-32k-online`、`llama-3-sonar-small-32k-chat`、`llama-3-sonar-large-32k-chat` | 2024-08-12；[更新记录](https://docs.perplexity.ai/docs/resources/changelog) |
| Perplexity | `sonar-reasoning` | 2025-12-15；[更新记录](https://docs.perplexity.ai/docs/resources/changelog) |
| Mistral API | `open-mistral-7b`、`open-mixtral-8x7b`、`open-mixtral-8x22b` | 2025-03-30；[Mistral 7B](https://docs.mistral.ai/models/mistral-7b-0-3)、[Mixtral 8x7B](https://docs.mistral.ai/models/mixtral-8x7b-0-1)、[Mixtral 8x22B](https://docs.mistral.ai/models/mixtral-8x22b-0-1-0-3) |
| 阿里云百炼 | `qwen-max-longcontext` | 2024-10-17；[模型更新记录](https://help.aliyun.com/zh/model-studio/model-release-notes) |
| 阿里云百炼 | `qwen2-72b-instruct`、`qwen2-57b-a14b-instruct`、`qwen2-7b-instruct` | 2026-03-30；[已下线模型表](https://help.aliyun.com/zh/model-studio/rate-limit) |
| 阿里云百炼 | `qwen2.5-72b-instruct`、`qwen2.5-32b-instruct`、`qwen2.5-14b-instruct`、`qwen2.5-7b-instruct`、`qwen2.5-3b-instruct`、`qwen2.5-1.5b-instruct`、`qwen2.5-coder-32b-instruct`、`qwen2.5-coder-14b-instruct`、`qwen2.5-coder-7b-instruct`、`qwq-32b`、`qwq-32b-preview` | 2026-05-13；[已下线模型表](https://help.aliyun.com/zh/model-studio/rate-limit) |
| 腾讯混元 | `hunyuan-lite`、`hunyuan-standard`、`hunyuan-standard-256k`、`hunyuan-pro`、`hunyuan-turbo`、`hunyuan-large`、`hunyuan-code` | 2026-06-22；[模型下线公告](https://cloud.tencent.com/document/product/1729/131925) |
| 百度千帆 | `ernie-lite-8k`、`ernie-speed-8k`、`ernie-speed-128k`、`ernie-tiny-8k` | 2026-06-09 已退役批次；[模型下线公告](https://cloud.baidu.com/doc/qianfan/s/zmh4stou3) |
| 百度千帆 | `ernie-4.0-8k`、`ernie-4.0-turbo-8k`、`ernie-3.5-8k`、`ernie-speed-pro-128k`、`ernie-lite-pro-128k` | 2026-06-30；[模型下线公告](https://cloud.baidu.com/doc/qianfan/s/zmh4stou3) |

## 保留项与核查边界

- **Cohere**：`command`、`command-light`、`command-r`、`command-r-plus` 的弃用不等于立即停服；公告允许符合条件的已有用户继续使用，暂不删除。[Cohere 弃用规则](https://docs.cohere.com/docs/deprecations)
- **Mistral Pixtral**：`pixtral-12b-2409`、`pixtral-large-latest` 所指版本标记为弃用，尚无本次核实的停服依据，保留。[Pixtral 12B](https://docs.mistral.ai/models/pixtral-12b-24-09)、[Pixtral Large](https://docs.mistral.ai/models/pixtral-large-24-11)
- **xAI**：2026-05-15 退出的部分底层模型仍保留可调用名称并重定向至新模型；这与请求必然失败不同，本次不据此删除。旧名称可能对应新的价格，应以实际服务为准。[官方迁移说明](https://docs.x.ai/developers/migration/may-15-retirement)
- **通用别名**：保留 `qwen-turbo`、`qwen-plus`、`qwen-max`、`qwen-long`、`qwen3-235b-a22b`。某个日期快照或 `-latest` 名称下线，不自动推出无后缀名称也下线。[百炼生命周期说明](https://help.aliyun.com/zh/model-studio/model-depreciation)
- **未确认的精确名称**：`hunyuan-vision`、`ernie-4.0-8k-latest`、`ernie-3.5-128k`、`codestral-mamba` 等保留；未发现充分依据不代表已验证当前账号可调用。
- **其他列表**：智谱、MiniMax、零一万物、Moonshot、DeepSeek、星火、豆包等，本次未取得足以删除现有精确 ID 的官方直接渠道停服证据，未按模型年龄猜测删除。百炼、千帆或 LAS 停止供应某第三方模型的公告，仅适用于相应托管渠道。
- **自定义与开放权重**：Meta/Llama、Qwen、Mistral 等模型仍可能通过自建服务或第三方代理使用。保留自定义 ID、历史账单所需定价和已有映射；本记录不构成请求黑名单。

后续维护应同时核对公告日期、实际停服日期、精确模型 ID、账号类型及上游目标。官方文档存在冲突或只确认了不同别名时，先保留并单独核查。
