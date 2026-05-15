# Audio And Video Generation Gateway Design

## Context

The gateway now supports OpenAI-compatible image generation. The next media surfaces are audio speech generation and video generation. The user wants an OpenAI-compatible external API, no Codex Responses bridge for audio or video, and provider routing through the existing account, group, scheduler, and usage-log system.

Audio uses Azure Speech text-to-speech:

- Realtime short-form REST TTS: `POST https://{region}.tts.speech.microsoft.com/cognitiveservices/v1`
- Batch synthesis: `PUT/GET https://{region}.api.cognitive.microsoft.com/texttospeech/batchsyntheses/{job_id}?api-version=2024-04-01`

Video uses two async providers:

- Alibaba DashScope HappyHorse: `POST https://dashscope.aliyuncs.com/api/v1/services/aigc/video-generation/video-synthesis` and `GET https://dashscope.aliyuncs.com/api/v1/tasks/{task_id}`
- Volcengine Ark Seedance 2.0: `POST https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks` and `GET https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks/{task_id}`

All async jobs need local persistence so status checks return to the original account and provider.

## Scope

Add OpenAI-compatible media endpoints for audio speech and video generation:

- `POST /v1/audio/speech`
- `POST /v1/audio/speech/jobs`
- `GET /v1/audio/speech/jobs/{id}`
- `POST /v1/videos/generations`
- `GET /v1/videos/generations/{id}`

The implementation will not add Codex `/v1/responses` audio or video tool bridging. It will not download and permanently store generated audio or video files. Result URLs from upstream providers are returned and persisted with their known expiration window.

## Platform And Account Model

Reuse the existing `Account` model and scheduler contracts. Add platform identifiers:

- `azure_speech`
- `dashscope`
- `volcengine_ark`

Credentials:

```json
{
  "platform": "azure_speech",
  "subscription_key": "azure-speech-key",
  "region": "eastasia",
  "tts_endpoint": "https://eastasia.tts.speech.microsoft.com",
  "batch_endpoint": "https://eastasia.api.cognitive.microsoft.com"
}
```

```json
{
  "platform": "dashscope",
  "api_key": "dashscope-key",
  "region": "cn-beijing",
  "base_url": "https://dashscope.aliyuncs.com"
}
```

```json
{
  "platform": "volcengine_ark",
  "api_key": "ark-key",
  "region": "cn-beijing",
  "base_url": "https://ark.cn-beijing.volces.com/api/v3"
}
```

`model_mapping` remains the main model availability control. Accounts without a mapping allow all models for that platform, matching existing behavior. Recommended mappings:

- Azure: `tts-1`, `tts-1-hd`, or explicit aliases mapped to Azure voices or internal labels.
- DashScope: `happyhorse-1.0-r2v`
- Volcengine Ark: `doubao-seedance-2-0-260128`, `doubao-seedance-2-0-fast-260128`

## Local Async Job Persistence

Add an Ent schema named `MediaGenerationJob` backed by table `media_generation_jobs`.

Fields:

- `id`: Ent-managed internal integer primary key.
- `public_id`: client-facing ID, prefixed by media type, for example `audjob_...` or `vidjob_...`.
- `kind`: `audio_speech` or `video_generation`.
- `provider`: `azure_speech`, `dashscope`, or `volcengine_ark`.
- `platform`: account platform at creation time.
- `status`: `queued`, `running`, `succeeded`, `failed`, `canceled`, `unknown`.
- `upstream_status`: raw upstream status.
- `upstream_task_id`: Azure batch synthesis ID, DashScope `task_id`, or Ark task ID.
- `upstream_request_id`: upstream diagnostic request ID when available.
- `user_id`, `api_key_id`, `group_id`, `account_id`.
- `model`, `request_json`, `upstream_response_json`.
- `result_url`, `result_content_type`, `expires_at`.
- `audio_voice`, `audio_format`, `audio_character_count`.
- `video_duration_seconds`, `video_resolution`, `video_ratio`, `video_count`.
- `error_code`, `error_message`.
- `usage_recorded_at`: set once final usage is written.
- `created_at`, `updated_at`, `submitted_at`, `completed_at`.

The table owns the relationship between a public job ID and the account that created it. Query endpoints never reschedule a different account for an existing job.

## Audio Speech API

### Synchronous Speech

Endpoint:

`POST /v1/audio/speech`

Request body:

```json
{
  "model": "tts-1",
  "input": "需要合成的文本",
  "voice": "zh-CN-XiaoxiaoNeural",
  "response_format": "mp3",
  "speed": 1.0,
  "language": "zh-CN"
}
```

Provider routing:

1. Authenticate and check billing eligibility.
2. Select an `azure_speech` account supporting the requested model.
3. Convert input to SSML and call Azure realtime TTS.
4. Return the binary audio response directly.
5. Record usage immediately with character count and output format.

Azure request:

- URL: `https://{region}.tts.speech.microsoft.com/cognitiveservices/v1`, unless account credentials provide `tts_endpoint`.
- Headers:
  - `Ocp-Apim-Subscription-Key: <subscription_key>`
  - `Content-Type: application/ssml+xml`
  - `X-Microsoft-OutputFormat: <mapped output format>`
  - `User-Agent: sub2api`
- Body: XML-escaped SSML.

Format mapping:

- `mp3` -> `audio-24khz-48kbitrate-mono-mp3`
- `wav` -> `riff-24khz-16bit-mono-pcm`
- `opus` -> `ogg-24khz-16bit-mono-opus`
- Unknown or empty -> `audio-24khz-48kbitrate-mono-mp3`

Speed mapping:

- OpenAI-style `speed` is translated to SSML prosody `rate`.
- `1.0` means no change.
- Values below `1.0` become negative percentages.
- Values above `1.0` become positive percentages.
- The implementation clamps extreme values to Azure-safe limits.

### Batch Speech

Create endpoint:

`POST /v1/audio/speech/jobs`

Request body:

```json
{
  "model": "tts-1",
  "input": "长文本内容",
  "voice": "zh-CN-XiaoxiaoNeural",
  "response_format": "mp3",
  "speed": 1.0,
  "language": "zh-CN"
}
```

Response:

```json
{
  "id": "audjob_abc123",
  "object": "audio.speech.job",
  "status": "queued",
  "model": "tts-1",
  "created_at": 1778918400
}
```

Query endpoint:

`GET /v1/audio/speech/jobs/{id}`

Response while pending:

```json
{
  "id": "audjob_abc123",
  "object": "audio.speech.job",
  "status": "running",
  "model": "tts-1",
  "created_at": 1778918400
}
```

Response on success:

```json
{
  "id": "audjob_abc123",
  "object": "audio.speech.job",
  "status": "succeeded",
  "model": "tts-1",
  "result": {
    "url": "https://...",
    "content_type": "audio/mpeg"
  },
  "usage": {
    "characters": 12000
  }
}
```

Azure Batch details:

- Create with `PUT {batch_endpoint}/texttospeech/batchsyntheses/{upstream_job_id}?api-version=2024-04-01`, where `batch_endpoint` defaults to `https://{region}.api.cognitive.microsoft.com`.
- Query with `GET {batch_endpoint}/texttospeech/batchsyntheses/{upstream_job_id}?api-version=2024-04-01`.
- Store the Azure job ID in `upstream_task_id`.
- Record usage the first time local status transitions to `succeeded`.

## Video Generation API

Create endpoint:

`POST /v1/videos/generations`

Request body:

```json
{
  "model": "happyhorse-1.0-r2v",
  "prompt": "视频提示词",
  "input": {
    "media": [
      {
        "type": "reference_image",
        "url": "https://example.com/image.png"
      }
    ]
  },
  "duration": 5,
  "ratio": "16:9",
  "resolution": "720p",
  "watermark": true,
  "generate_audio": true
}
```

Response:

```json
{
  "id": "vidjob_abc123",
  "object": "video.generation.job",
  "status": "queued",
  "model": "happyhorse-1.0-r2v",
  "created_at": 1778918400
}
```

Query endpoint:

`GET /v1/videos/generations/{id}`

Response on success:

```json
{
  "id": "vidjob_abc123",
  "object": "video.generation.job",
  "status": "succeeded",
  "model": "happyhorse-1.0-r2v",
  "result": {
    "url": "https://...",
    "content_type": "video/mp4"
  },
  "usage": {
    "duration": 5,
    "output_video_duration": 5,
    "video_count": 1,
    "resolution": "720p",
    "ratio": "16:9"
  }
}
```

## DashScope HappyHorse Adapter

Models:

- `happyhorse-1.0-r2v`

Create request:

- URL: `{base_url}/api/v1/services/aigc/video-generation/video-synthesis`
- Headers:
  - `Authorization: Bearer <api_key>`
  - `Content-Type: application/json`
  - `X-DashScope-Async: enable`

Request conversion:

```json
{
  "model": "happyhorse-1.0-r2v",
  "input": {
    "prompt": "<prompt>",
    "media": [
      {
        "type": "reference_image",
        "url": "https://example.com/image.png"
      }
    ]
  },
  "parameters": {
    "resolution": "720P",
    "ratio": "16:9",
    "duration": 5,
    "watermark": true,
    "seed": 123
  }
}
```

Query request:

- URL: `{base_url}/api/v1/tasks/{task_id}`
- Headers:
  - `Authorization: Bearer <api_key>`

Status mapping:

- `PENDING` -> `queued`
- `RUNNING` -> `running`
- `SUCCEEDED` -> `succeeded`
- `FAILED` -> `failed`
- `CANCELED` -> `canceled`
- `UNKNOWN` -> `unknown`

On success, persist `output.video_url` and usage fields `duration`, `input_video_duration`, `output_video_duration`, `video_count`, `SR`, and `ratio`.

## Volcengine Ark Seedance Adapter

Models:

- `doubao-seedance-2-0-260128`
- `doubao-seedance-2-0-fast-260128`

Create request:

- URL: `{base_url}/contents/generations/tasks`
- Headers:
  - `Authorization: Bearer <api_key>`
  - `Content-Type: application/json`

Request conversion:

```json
{
  "model": "doubao-seedance-2-0-260128",
  "content": [
    {
      "type": "text",
      "text": "<prompt>"
    },
    {
      "type": "image_url",
      "image_url": {
        "url": "https://example.com/image.png"
      },
      "role": "reference_image"
    },
    {
      "type": "video_url",
      "video_url": {
        "url": "https://example.com/video.mp4"
      },
      "role": "reference_video"
    },
    {
      "type": "audio_url",
      "audio_url": {
        "url": "https://example.com/audio.mp3"
      },
      "role": "reference_audio"
    }
  ],
  "ratio": "16:9",
  "duration": 8,
  "resolution": "720p",
  "watermark": true,
  "generate_audio": true
}
```

Query request:

- URL: `{base_url}/contents/generations/tasks/{task_id}`
- Headers:
  - `Authorization: Bearer <api_key>`

Status mapping:

- `queued`, `pending` -> `queued`
- `running` -> `running`
- `succeeded` -> `succeeded`
- `failed` -> `failed`
- `cancelled`, `canceled` -> `canceled`

On success, persist `content.video_url`. If Ark returns `content.last_frame_url`, keep it in `upstream_response_json` and include it in the public `result` as `last_frame_url`.

## Input Validation

Shared video validation:

- `prompt` is required.
- `duration` must be a positive integer. Provider-specific validators apply stricter ranges:
  - HappyHorse: 3 to 15 seconds.
  - Seedance 2.0: 4 to 15 seconds.
- `media` entries must have `type` and `url`.
- Supported media types:
  - `reference_image`
  - `first_frame`
  - `last_frame`
  - `reference_video`
  - `reference_audio`
- HappyHorse accepts image references only in the initial implementation.
- Seedance accepts image, video, and audio references.

Shared audio validation:

- `input` is required.
- `voice` defaults to `zh-CN-XiaoxiaoNeural`.
- `language` defaults to the locale prefix of the voice, or `zh-CN` if it cannot be inferred.
- `response_format` defaults to `mp3`.

## Billing And Usage

Prices are not configured at initial implementation time. The system records usage and leaves cost at zero until pricing rules are configured.

Usage fields:

- Sync audio: character count, voice, output format, model, account ID.
- Batch audio: character count, result URL, output format, model, account ID.
- Video: duration, output duration, video count, resolution, ratio, model, account ID, result URL.

Billing behavior:

- Sync audio records usage after a successful upstream response.
- Async audio and video record usage once, when local status first transitions to `succeeded`.
- Failed, canceled, and unknown jobs do not record billable usage.
- If pricing is later configured, media billing can be added through the same model-pricing/channel-pricing path as image generation.

## Error Handling

Synchronous audio:

- Upstream 401/403/429/5xx errors mark schedule failure and can fail over to another eligible `azure_speech` account before writing a response.
- Non-retryable validation errors are returned to the client with an OpenAI-style error object.

Async create:

- If upstream task creation fails with a retryable status, fail over before creating a local job.
- If local job persistence fails after upstream creation succeeds, return a 502 with a message that the gateway could not persist the job. This avoids returning an untrackable task to the client.

Async query:

- If the local job is missing or belongs to another user/API key, return 404.
- If the original account is no longer available, return the last local status with an error explaining that upstream refresh is temporarily unavailable.
- If upstream says the task is expired or unknown, update local status to `unknown`.
- Preserve upstream `request_id`, `code`, and `message` in internal metadata and expose safe error fields in public responses.

## Testing

Service tests:

- Azure realtime TTS builds correct endpoint, SSML, output-format header, and binary passthrough.
- Azure batch create persists a local job with original account ID and upstream job ID.
- Azure batch query updates local status and records usage once.
- DashScope create converts OpenAI-compatible video input into `input.media` and `parameters`.
- DashScope query maps `SUCCEEDED` and stores `video_url` plus usage.
- Ark create converts OpenAI-compatible video input into `content[]`.
- Ark query maps `succeeded`, stores `content.video_url`, and preserves `last_frame_url` when returned.
- Retryable upstream create errors do not write partial client responses before failover.

Handler and route tests:

- All five endpoints are registered.
- Audio sync returns binary response and content type.
- Async create returns OpenAI-style job objects.
- Query endpoints enforce API key ownership.
- Invalid provider/model combinations return clear 400 or 503 errors.

Repository tests:

- `media_generation_jobs` create, update, and lookup by `public_id`.
- Status transition to `succeeded` is idempotent for usage recording.
- Jobs preserve `account_id` and do not reschedule on query.

## Rollout

Implementation order:

1. Add platform constants, route constants, and route registration.
2. Add media job schema, repository, and service interfaces.
3. Implement Azure realtime TTS.
4. Implement Azure batch speech jobs.
5. Implement DashScope HappyHorse video jobs.
6. Implement Volcengine Ark Seedance video jobs.
7. Add usage logging with zero-cost media billing.
8. Add focused tests and run existing handler/service route suites.

The first usable milestone is synchronous Azure TTS plus video job persistence. The second milestone is async query/usage recording for all providers.
