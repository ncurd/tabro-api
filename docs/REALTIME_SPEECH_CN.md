# 实时语音输入与回答朗读

Tabro Agent 通过现有模型网关凭据调用本接口。调用方使用自己的网关 API Key 或交换后的
OAuth access token，阿里云凭据只保存在网关的 `dashscope` 账号中。分组、渠道映射、
模型白名单、并发和中心计费沿用网关现有规则。

## 支持范围

| 用途 | 上游模型 | 网关路径 |
| --- | --- | --- |
| 持续转写 | `paraformer-realtime-*`、`fun-asr*realtime*`、`qwen-audio-3.0-asr-flash-streaming*` | `GET /v1/audio/transcriptions/stream?model=<公开模型名>`，WebSocket |
| 回答朗读 | `qwen3-tts-flash-realtime` 及其日期快照 | `POST /v1/audio/speech`，返回 WAV 或 PCM 二进制 |

接口也支持无 `/v1` 的路径。如果 Agent 模型提供商 Base URL 已包含 `/v1`，只追加
`/audio/transcriptions/stream` 或 `/audio/speech`，不要再添加一层 `/v1`。

生产部署建议将 Agent Base URL 配置为网关 API 根路径，例如 `https://llm.example.com/v1`。
使用无 `/v1` 的别名时，内嵌前端与前置代理必须将 `/audio`、`/audio/...` 交给 API，
不能回退为 SPA 首页。语音 WebSocket 的握手应返回 `101 Switching Protocols`；
无凭据探测应得到 `401` JSON 鉴权错误，`200 text/html` 表示请求被前端路由接管。
升级旧版网关后再使用无 `/v1` 的语音路径，或先使用带 `/v1` 的 Base URL。

`/v1/models` 的 `speech_tasks` 字段说明本版网关实际实现的能力。实时输入为
`transcription.streaming`，朗读为 `speech.synthesis`；未实现的模型显式返回 `[]`。
公开别名按已授权账号及渠道的上游映射判断能力。仅模型名中含 `audio`、`realtime`
不能证明可调用。

本版不接入只接受 8 kHz 音频的 `*-8k*` 型号；尚不转接使用另一套 session/update 协议的 `qwen3-asr-flash-realtime`，也不支持
按 token 计费的 Qwen-Audio 3.1 ASR 或其他语音合成家族。它们不会被公布为可用语音任务。
原有完整录音 JSON 转写、`voice_*` 克隆音色 URL 响应和 Azure Speech 路径保留。

## 配置阿里云账号

在 `dashscope` 平台配置同地域、同业务空间且允许所选模型的 `api_key` 与 `base_url`。
官方 DashScope 原生 HTTP Base URL 为 `https://<WorkspaceId>.cn-beijing.maas.aliyuncs.com/api/v1`；
OpenAI 兼容 HTTP Base URL 使用同主机的 `/compatible-mode/v1`。这两种都是合法配置，
不是 WebSocket 地址。新版实时适配器接受根地址以及
`/api/v1` 或 `/compatible-mode/v1` 前缀（含末尾斜杠）：仅在建立语音 WebSocket 时去掉
这两个明确的 HTTP 前缀，再使用同一个主机的 `/api-ws/v1/inference` 或
`/api-ws/v1/realtime?model=...`。其他 HTTP 模型仍使用原配置，不会被修改。
任意其他路径、编码路径、userinfo、query 或 fragment 会被拒绝，不接受完整 WebSocket 地址。

旧版实时适配器只接受根地址，错误地拒绝了官方合法的 HTTP 前缀。若模型为
`paraformer-realtime-v2`，账号配置以 `/api/v1` 结尾，而 Agent 提示
`speech_provider_start_failed`，应更新网关使用上述兼容逻辑。仅需临时绕过旧版限制时，
才考虑将该账号地址改为根地址，并确认同账号的其他 HTTP 用途。这里是 tabro-api 的
DashScope 账号，不是 Agent 内置提供商的 `https://llm.tabro.cn/v1`；平台 Token 交换配置不变。

将实际开通的型号加入账号白名单或设置公开别名。配置管理员基础价或渠道媒体价格：ASR 使用每音频秒
的价格；TTS 可在模型管理选择「按时长」配置音频输出秒价，也可保留每计费字符的价格，或管理员明确配置的每次调用价格。缺价时请求拒绝，
不会免计费转发。Qwen 实时 TTS 的报价上界按汉字 2 个字符、其他字符 1 个估算，
实际结算使用供应商返回的用量；已有克隆音色和 Azure 使用各自原有口径。
供应商账单币种与网关配置币种仍须按原有渠道价格规则处理。

## Nginx 返回 426：Connection header "close" does not contain Upgrade

如果运维错误记录的入站端点是 `/v1/audio/transcriptions/stream`，状态为 426，正文为
`WebSocket protocol violation: Connection header "close" does not contain Upgrade`，
表示网关收到的入站请求缺少 WebSocket 升级头。该错误发生在身份与账号选择之后、连接
阿里云之前；保留现有平台 Token、模型和分组配置，先修复代理。

在 Nginx 的 `http {}` 作用域添加一次以下映射；如果已有等效映射，复用它：

```nginx
map $http_upgrade $tabro_connection_upgrade {
    default upgrade;
    ''      "";
}
```

空值让普通 HTTP 请求不发送 `Connection` 头，保留 HTTP/1.1 keepalive；WebSocket 请求则发送
`Connection: upgrade`。这两种请求可以共用原有 SSE/API 代理位置。

在 **实际匹配语音请求** 的现有 `location` 中添加或更新以下指令，保留原有 `proxy_pass`、
认证头、Host 与 TLS 配置：

```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection $tabro_connection_upgrade;
proxy_read_timeout 660s;
proxy_send_timeout 660s;
```

若 `/v1/` 有单独的 `location`，修改那个位置；只修改 `location /` 可能不会生效。
`proxy_set_header` 仅在当前层没有此类指令时才整体继承。即使 `server` 已配置
`proxy_set_header Connection ""`，`location` 中只要自己设置了 Host、Upgrade 等头，就需要
在同一位置显式配置 Connection；否则可能回到默认 `Connection: close`。重复的 Upgrade
指令应合并为一行。基于该站点现有路由的完整配置见
[Nginx 示例](../deploy/nginx/llm.tabro.cn.conf.example)，其中使用独立变量 `$tabro_ws_connection`。
若有多级代理，每一级都需要支持升级，不能在下一跳重新写成 `Connection close`。
不要将上述指令另建为一个缺少原有路由和认证配置的替代 `location`，也不要通过放宽
网关的握手校验来处理此问题。参考 [Nginx WebSocket 代理说明](https://nginx.org/en/docs/http/websocket.html)。

修改后在 Nginx 所在主机验证并平滑重载：

```sh
sudo nginx -t && sudo nginx -s reload
```

如果 Nginx 由容器或管理面板托管，使用对应实例的配置校验和重载操作。
重试会话麦克风，网关握手应为 `101 Switching Protocols`，随后进入持续听写。
未带凭据的公网探测只能得到鉴权错误，不能证明升级头已经正确透传；应以实际授权请求
的握手结果验收。

## WebSocket 已连接但识别任务启动失败

HTTP 101 只代表 WebSocket 握手成功，之后还需要模型返回 `task-started`。缺少模型价格、
中心计费授权失败、无效供应商地址、供应商鉴权或模型未开通，都会在这一阶段返回
`task-failed`；它们不能通过 HTTP 状态大于等于 400 的过滤器完整捕获。

新版网关将这类失败以 `websocket_task_error` 写入运维错误日志，保留实际握手状态 101、
模型、账号 ID、请求 ID、失败阶段、受控错误码及确切的上游 HTTP 状态（如果有）。
它不会标成“恢复成功”，也不会保存原始供应商错误正文、API Key、录音或转写。
运维面板的默认错误列表包含这类 WebSocket 终态失败；手动限定 HTTP 4xx/5xx 时仍不会
包含实际状态为 101 的记录。服务控制台同步输出
`[Speech] realtime task failed`，便于按请求 ID 定位。

Agent 同时识别网关的受控 `error_code`，会明确显示地址、缺价、计费、鉴权、权限、
网络或限流问题；未知供应商文本仍不直接展示。部署时更新 tabro-api 和 Agent Control Plane
才能完整使用新分类，更新 Agent Web 可获得中文提示。

### 提示语音计费授权暂不可用或模型缺价

旧版会把缺少语音价格包装成通用计费授权错误；新版保留原始缺价分类，返回
`speech_pricing_not_configured`。这不需要把平台 Token 模式改为自定义提供商。

管理员在 tabro-api 的「模型管理」（`/admin/models`）中找到实际用于计费的模型，
选择「按时长」，给 `paraformer-realtime-v2` 填写「音频输入／秒」，给实时 TTS 模型填写
「音频输出／秒」；也可保留原有「音频／秒」「音频／字符」或按请求价格。这里填写 USD 售价，需按自身
计费规则换算；不要直接把按分钟、万字符或人民币展示的供应商价格原样填入。
未填写的价格不会被当作免费，明确填写 0 才表示免费。

渠道价格优先于管理员基础价格；如果当前分组的渠道已有价格覆盖，应核对该覆盖。
模型别名的价格查找沿用渠道 `billing_model_source` 设置。新连接使用新价格，已经冻结
的操作保持原价。如果价格已配置仍返回计费授权错误，应按请求 ID 检查运维记录中的
错误码和阶段，再核对 Auth 计费中心的报价、预占及产品绑定配置。

输出音频秒数由当前适配器协商的 24 kHz、单声道、16-bit PCM 样本计算，WAV 只统计真实
音频数据块，不计文件头。Qwen 实时 TTS 每连接最多输出 600 秒；Azure 同步 TTS 的时长价
只支持 WAV/PCM，并校验真实格式及 600 秒上限。此处时长是生成音频的播放长度，允许小数秒。
Azure MP3/Opus、异步合成和仅返回 URL 的克隆语音当前不能提供这套可验证计量与上界，
会在供应商调用前拒绝时长价。原字符计价继续可用。
协议依据：[Qwen 实时 TTS](https://help.aliyun.com/en/model-studio/interactive-process-of-qwen-tts-realtime-synthesis)、
[Azure 同步语音合成](https://learn.microsoft.com/en-us/azure/ai-services/speech-service/rest-text-to-speech)。

## 实时转写协议

服务端先建立 WebSocket，发送 `Authorization: Bearer ...`，可为同一逻辑任务提供
稳定的 `Idempotency-Key`。`model` 查询参数必须与首条消息的公开模型名一致。网关
完成分组和账号选择后，只在服务器内部替换为上游模型名。
中心计费使用调用身份、该键和 `task_id` 建立去重身份；本地计费模式每次连接均为新任务。

首条消息：

```json
{
  "header": { "action": "run-task", "task_id": "<UUID>", "streaming": "duplex" },
  "payload": {
    "task_group": "audio",
    "task": "asr",
    "function": "recognition",
    "model": "paraformer-realtime-v2",
    "parameters": { "format": "pcm", "sample_rate": 16000 },
    "input": {}
  }
}
```

收到 `header.event=task-started` 后，持续发送 16 kHz、单声道、16-bit little-endian
PCM 二进制帧。每帧最多 256 KiB；每条连接只执行一个任务，音频总量最多 600 秒，
连接处理也有时限。前置代理需要支持 WebSocket upgrade，并允许持续二进制帧和心跳。

当实际上游模型为 `paraformer-realtime-v2` 且请求未指定 `parameters.heartbeat` 时，
网关默认添加 `heartbeat=true`，避免持续上传静音期间被供应商超时断开。显式的 false
仍被保留，Fun-ASR 和 Qwen 不会收到这个 Paraformer 专用参数。这不是免上传音频的保活：
任务仍须持续发送音频，并受原有时长限制。心跳结果不进入用户草稿。

`result-generated` 事件中的 `payload.output.sentence.text` 是当前句子的完整识别结果，
中间结果应替换而非简单拼接；`sentence_end=true` 表示句子确定。Qwen Streaming 有
`sentence_id`，Paraformer 可用 `begin_time` 关联句子；`heartbeat=true` 的结果忽略。
停止发送音频后提交：

```json
{
  "header": { "action": "finish-task", "task_id": "<相同UUID>", "streaming": "duplex" },
  "payload": { "input": {} }
}
```

等待最终识别结果和 `task-finished` 后关闭连接。`task-failed` 表示失败，客户端不得
无提示地重新发送整段音频；本接口不缓存或重播录音与历史转写。

中心模式为每个任务创建独立媒体 operation，在发送 `run-task` 前取得一次执行许可。
报价上界为 600 秒，实际结算依据上游累计 `usage.duration`，不会逐事件相加。
断线、供应商失败或缺少可验证 usage 时保留待核对责任，不自动重发收费任务或假定无费用。
作业只保存所有权、模型、冻结价格和用量，不保存音频帧或转写内容。
每次收到权威累计用量后先保存 checkpoint，再向客户端转发文本。任务异常结束时，已确认
用量保留在 `unknown` 作业中，并标记 `usage_complete=false`；它只是已知下界，不会被
自动当作完整成功用量结算。中心计费同时进入待核对状态；本地模式也保留相同证据供核对。

## 语音合成

```json
{
  "model": "qwen3-tts-flash-realtime",
  "input": "你好，这是本次回答。",
  "voice": "Cherry",
  "response_format": "wav"
}
```

`voice` 默认 `Cherry`；当前支持 `wav`（默认）和 `pcm`，均为 24 kHz、单声道、
PCM16。`speed` 省略或设为 1。网关本地将单次输入限制为 1–4000 个 Unicode 字符；
这是本接口的产品限额，不是对阿里云模型最大输入长度的声明。
网关通过供应商实时 TTS 协议收集真实 PCM 音频，合成完成后返回二进制；WAV 返回
`audio/wav`，裸 PCM 返回 `audio/pcm`。本 HTTP 接口不会将 JSON URL 响应伪装成音频。

当前接口适合完整回答或已经确定的正文片段朗读；它不表示网关已经提供双向文本/音频
流式的浏览器接口。输入内容、语音二进制和供应商密钥不会保存到媒体作业中。
TTS 实际结算累计各个 `response.done.response.usage.characters`，按 response ID 去重。
本地汉字计数用于报价上界，不覆盖供应商实际用量；缺少任一 response 的用量时保留
`unknown` 待核对状态，不以估算值标记成功或生成最终账单。

## 上游协议参考

- [官方 Base URL 总览](https://docs.bailian.console.aliyun.com/zh/model-studio/base-url)
- [获取与配置 API Key](https://docs.bailian.console.aliyun.com/zh/model-studio/get-api-key)
- [API Key 完整帮助页（地域、业务空间和模型权限）](https://help.aliyun.com/zh/model-studio/get-api-key)
- [Paraformer 客户端事件与静音心跳](https://docs.bailian.console.aliyun.com/zh/model-studio/paraformer-client-events)
- [阿里云 Paraformer WebSocket 协议](https://help.aliyun.com/en/model-studio/websocket-for-paraformer-real-time-service)
- [Paraformer 服务端事件与用量口径](https://help.aliyun.com/en/model-studio/paraformer-server-events)
- [Qwen TTS 实时合成流程](https://help.aliyun.com/en/model-studio/interactive-process-of-qwen-tts-realtime-synthesis)
- [Qwen TTS 客户端事件](https://help.aliyun.com/en/model-studio/qwen-tts-realtime-client-events)
- [Qwen TTS 服务端事件与计费用量](https://help.aliyun.com/en/model-studio/qwen-tts-realtime-server-events)

以上协议于 2026-11-14 按官方页面核对。当前 Paraformer 的 UUID 任务身份、单声道
16 kHz PCM、run-task/finish-task 空 input、累计 duration，以及 Qwen3 TTS 的 commit 模式、
24 kHz PCM、response.done 中的 response.usage.characters 均与文档一致。阿里云 API Key 保留在网关；
Agent 现有平台 Token 仅负责 Agent 到网关的授权。真实业务空间域名、模型开通和 Key 权限
仍需部署后实测；模拟测试不构成供应商连通性验证。
