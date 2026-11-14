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

`/v1/models` 的 `speech_tasks` 字段说明本版网关实际实现的能力。实时输入为
`transcription.streaming`，朗读为 `speech.synthesis`；未实现的模型显式返回 `[]`。
公开别名按已授权账号及渠道的上游映射判断能力。仅模型名中含 `audio`、`realtime`
不能证明可调用。

本版不接入只接受 8 kHz 音频的 `*-8k*` 型号；尚不转接使用另一套 session/update 协议的 `qwen3-asr-flash-realtime`，也不支持
按 token 计费的 Qwen-Audio 3.1 ASR 或其他语音合成家族。它们不会被公布为可用语音任务。
原有完整录音 JSON 转写、`voice_*` 克隆音色 URL 响应和 Azure Speech 路径保留。

## 配置阿里云账号

在 `dashscope` 平台配置同地域/业务空间的 `api_key` 与根 `base_url`，例如
`https://<WorkspaceId>.cn-beijing.maas.aliyuncs.com`。不要包含 `/api/v1`、
`/compatible-mode/v1` 或完整 WebSocket 路径。网关添加 `/api-ws/v1/inference`
或 `/api-ws/v1/realtime?model=...` 并使用该账号凭据。

将实际开通的型号加入账号白名单或设置公开别名。配置渠道媒体价格：ASR 使用每音频秒
的价格，TTS 使用每计费字符的价格，或管理员明确配置的每次调用价格。缺价时请求拒绝，
不会免计费转发。Qwen 实时 TTS 的报价上界按汉字 2 个字符、其他字符 1 个估算，
实际结算使用供应商返回的用量；已有克隆音色和 Azure 使用各自原有口径。
供应商账单币种与网关配置币种仍须按原有渠道价格规则处理。

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
PCM16。`speed` 省略或设为 1。单次输入限制 1–4000 个 Unicode 字符。
网关通过供应商实时 TTS 协议收集真实 PCM 音频，合成完成后返回二进制；WAV 返回
`audio/wav`，裸 PCM 返回 `audio/pcm`。本 HTTP 接口不会将 JSON URL 响应伪装成音频。

当前接口适合完整回答或已经确定的正文片段朗读；它不表示网关已经提供双向文本/音频
流式的浏览器接口。输入内容、语音二进制和供应商密钥不会保存到媒体作业中。
TTS 实际结算累计各个 `response.done.response.usage.characters`，按 response ID 去重。
本地汉字计数用于报价上界，不覆盖供应商实际用量；缺少任一 response 的用量时保留
`unknown` 待核对状态，不以估算值标记成功或生成最终账单。

## 上游协议参考

- [阿里云 Paraformer WebSocket 协议](https://help.aliyun.com/en/model-studio/websocket-for-paraformer-real-time-service)
- [Paraformer 服务端事件与用量口径](https://help.aliyun.com/en/model-studio/paraformer-server-events)
- [Qwen TTS 实时合成流程](https://help.aliyun.com/en/model-studio/interactive-process-of-qwen-tts-realtime-synthesis)
- [Qwen TTS 客户端事件](https://help.aliyun.com/en/model-studio/qwen-tts-realtime-client-events)
- [Qwen TTS 服务端事件与计费用量](https://help.aliyun.com/en/model-studio/qwen-tts-realtime-server-events)
