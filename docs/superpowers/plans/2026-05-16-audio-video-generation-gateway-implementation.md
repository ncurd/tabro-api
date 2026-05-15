# Audio Video Generation Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add OpenAI-compatible audio speech and video generation endpoints backed by Azure Speech, DashScope HappyHorse, and Volcengine Ark Seedance 2.0.

**Architecture:** Reuse existing account, group, scheduler, auth, billing eligibility, and usage-log patterns. Add a small media-generation layer with provider adapters and a local async job table so async status checks return to the original account. Keep provider conversion logic in service files and route/authorization behavior in handler files.

**Tech Stack:** Go, Gin, Ent/Postgres repository, existing scheduler/cache/repository patterns, Azure Speech REST API, DashScope async task API, Volcengine Ark content generation tasks API.

---

## Current Worktree Note

The worktree already contains uncommitted OpenAI image-generation changes. Do not revert them. Keep audio/video changes scoped and avoid rewriting the image implementation unless a shared helper is deliberately introduced.

## Files

- Modify: `backend/internal/service/account.go`
- Modify: `backend/internal/service/channel.go`
- Modify: `backend/internal/handler/endpoint.go`
- Modify: `backend/internal/server/routes/gateway.go`
- Modify: `backend/internal/server/routes/gateway_test.go`
- Create: `backend/ent/schema/media_generation_job.go`
- Generate: Ent files under `backend/ent/` by running `go generate ./ent`
- Create: `backend/internal/service/media_generation_job.go`
- Create: `backend/internal/repository/media_generation_job_repo.go`
- Modify: `backend/internal/repository/wire.go`
- Create: `backend/internal/service/media_generation_provider.go`
- Create: `backend/internal/service/media_generation_provider_test.go`
- Create: `backend/internal/service/media_generation_service.go`
- Create: `backend/internal/service/media_generation_service_test.go`
- Modify: `backend/internal/service/wire.go`
- Create: `backend/internal/handler/media_generation_handler.go`
- Create: `backend/internal/handler/media_generation_handler_test.go`
- Modify: `backend/internal/handler/handlers.go`
- Modify: `backend/internal/handler/wire.go`
- Generate: `backend/cmd/server/wire_gen.go` after adding DI providers by running `go generate ./cmd/server`

## Task 1: Register Platforms And Routes

**Files:**
- Modify: `backend/internal/service/account.go`
- Modify: `backend/internal/service/channel.go`
- Modify: `backend/internal/handler/endpoint.go`
- Modify: `backend/internal/server/routes/gateway.go`
- Test: `backend/internal/server/routes/gateway_test.go`

- [ ] **Step 1: Write failing route tests**

Add tests to `backend/internal/server/routes/gateway_test.go`:

```go
func TestGatewayRoutesMediaGenerationPathsAreRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPost, path: "/v1/audio/speech", body: `{"model":"tts-1","input":"hello"}`},
		{method: http.MethodPost, path: "/v1/audio/speech/jobs", body: `{"model":"tts-1","input":"long text"}`},
		{method: http.MethodGet, path: "/v1/audio/speech/jobs/audjob_test"},
		{method: http.MethodPost, path: "/v1/videos/generations", body: `{"model":"happyhorse-1.0-r2v","prompt":"draw"}`},
		{method: http.MethodGet, path: "/v1/videos/generations/vidjob_test"},
		{method: http.MethodPost, path: "/audio/speech", body: `{"model":"tts-1","input":"hello"}`},
		{method: http.MethodGet, path: "/videos/generations/vidjob_test"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}
			req := httptest.NewRequest(tt.method, tt.path, body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			router.ServeHTTP(w, req)

			require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should hit media generation handler", tt.path)
		})
	}
}
```

- [ ] **Step 2: Run the failing route test**

Run:

```bash
cd backend && go test -tags=unit ./internal/server/routes -run 'TestGatewayRoutesMediaGenerationPathsAreRegistered' -count=1
```

Expected: fail because routes and handler methods do not exist.

- [ ] **Step 3: Add platform, billing-mode, and endpoint constants**

In `backend/internal/service/account.go`, extend the platform constants block:

```go
const (
	PlatformAnthropic     = "anthropic"
	PlatformOpenAI        = "openai"
	PlatformGemini        = "gemini"
	PlatformAntigravity   = "antigravity"
	PlatformAzureSpeech   = "azure_speech"
	PlatformDashScope     = "dashscope"
	PlatformVolcengineArk = "volcengine_ark"
)
```

In `backend/internal/handler/endpoint.go`, add:

```go
const (
	EndpointAudioSpeech      = "/v1/audio/speech"
	EndpointAudioSpeechJobs  = "/v1/audio/speech/jobs"
	EndpointVideoGenerations = "/v1/videos/generations"
)
```

Update `NormalizeInboundEndpoint` to return those constants for matching paths.

In `backend/internal/service/channel.go`, add media billing modes and validation:

```go
const (
	BillingModeAudio BillingMode = "audio"
	BillingModeVideo BillingMode = "video"
)
```

Update `BillingMode.IsValid()` to accept `BillingModeAudio` and `BillingModeVideo`. These modes are initially recorded with zero cost unless channel pricing later defines media model prices.

- [ ] **Step 4: Add stub handler methods and route registration**

Create `backend/internal/handler/media_generation_handler.go` with stubs:

```go
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type MediaGenerationHandler struct{}

func (h *MediaGenerationHandler) AudioSpeech(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": "Media generation service is not configured"}})
}

func (h *MediaGenerationHandler) CreateAudioSpeechJob(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": "Media generation service is not configured"}})
}

func (h *MediaGenerationHandler) GetAudioSpeechJob(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": "Media generation service is not configured"}})
}

func (h *MediaGenerationHandler) CreateVideoGeneration(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": "Media generation service is not configured"}})
}

func (h *MediaGenerationHandler) GetVideoGeneration(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": "Media generation service is not configured"}})
}
```

Add `MediaGeneration *MediaGenerationHandler` to `handler.Handlers` and initialize it wherever handlers are constructed.

In `backend/internal/server/routes/gateway.go`, register both `/v1` routes and non-prefixed aliases with existing API key middleware:

```go
gateway.POST("/audio/speech", h.MediaGeneration.AudioSpeech)
gateway.POST("/audio/speech/jobs", h.MediaGeneration.CreateAudioSpeechJob)
gateway.GET("/audio/speech/jobs/:id", h.MediaGeneration.GetAudioSpeechJob)
gateway.POST("/videos/generations", h.MediaGeneration.CreateVideoGeneration)
gateway.GET("/videos/generations/:id", h.MediaGeneration.GetVideoGeneration)

r.POST("/audio/speech", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.MediaGeneration.AudioSpeech)
r.POST("/audio/speech/jobs", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.MediaGeneration.CreateAudioSpeechJob)
r.GET("/audio/speech/jobs/:id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.MediaGeneration.GetAudioSpeechJob)
r.POST("/videos/generations", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.MediaGeneration.CreateVideoGeneration)
r.GET("/videos/generations/:id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.MediaGeneration.GetVideoGeneration)
```

- [ ] **Step 5: Run route tests**

Run:

```bash
cd backend && go test -tags=unit ./internal/server/routes -run 'TestGatewayRoutesMediaGenerationPathsAreRegistered|TestGatewayRoutesOpenAIImagesGenerationsPathIsRegistered' -count=1
```

Expected: pass.

## Task 2: Add Media Job Model And Repository

**Files:**
- Create: `backend/ent/schema/media_generation_job.go`
- Create: `backend/internal/service/media_generation_job.go`
- Create: `backend/internal/repository/media_generation_job_repo.go`
- Modify: `backend/internal/repository/wire.go`
- Test: `backend/internal/repository/media_generation_job_repo_test.go`

- [ ] **Step 1: Write failing repository tests**

Create `backend/internal/repository/media_generation_job_repo_test.go`:

```go
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMediaGenerationJobRepository_CreateAndGetByPublicID(t *testing.T) {
	repo := NewMediaGenerationJobRepository(nil)
	ctx := context.Background()
	now := time.Now().UTC()

	job := &service.MediaGenerationJob{
		PublicID:       "vidjob_test",
		Kind:           service.MediaJobKindVideoGeneration,
		Provider:       service.MediaProviderDashScope,
		Platform:       service.PlatformDashScope,
		Status:         service.MediaJobStatusQueued,
		UpstreamTaskID: "task-1",
		UserID:         1,
		APIKeyID:       2,
		AccountID:      3,
		Model:          "happyhorse-1.0-r2v",
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	require.NoError(t, repo.Create(ctx, job))
	got, err := repo.GetByPublicID(ctx, "vidjob_test")

	require.NoError(t, err)
	require.Equal(t, "vidjob_test", got.PublicID)
	require.Equal(t, "task-1", got.UpstreamTaskID)
}
```

This repository test intentionally uses `NewMediaGenerationJobRepository(nil)`. The implementation must provide a deterministic in-memory fallback for nil Ent clients so focused unit tests can cover create, get by public ID, update status, and usage-record idempotency without Postgres.

- [ ] **Step 2: Run the failing repository test**

Run:

```bash
cd backend && go test -tags=unit ./internal/repository -run 'TestMediaGenerationJobRepository' -count=1
```

Expected: fail because the repository and service model do not exist.

- [ ] **Step 3: Add service model and repository interface**

Create `backend/internal/service/media_generation_job.go`:

```go
package service

import (
	"context"
	"time"
)

const (
	MediaJobKindAudioSpeech     = "audio_speech"
	MediaJobKindVideoGeneration = "video_generation"

	MediaProviderAzureSpeech   = "azure_speech"
	MediaProviderDashScope     = "dashscope"
	MediaProviderVolcengineArk = "volcengine_ark"

	MediaJobStatusQueued    = "queued"
	MediaJobStatusRunning   = "running"
	MediaJobStatusSucceeded = "succeeded"
	MediaJobStatusFailed    = "failed"
	MediaJobStatusCanceled  = "canceled"
	MediaJobStatusUnknown   = "unknown"
)

type MediaGenerationJob struct {
	ID                    int64
	PublicID              string
	Kind                  string
	Provider              string
	Platform              string
	Status                string
	UpstreamStatus        string
	UpstreamTaskID        string
	UpstreamRequestID     string
	UserID                int64
	APIKeyID              int64
	GroupID               *int64
	AccountID             int64
	Model                 string
	RequestJSON           []byte
	UpstreamResponseJSON  []byte
	ResultURL             string
	ResultContentType     string
	ExpiresAt             *time.Time
	AudioVoice            string
	AudioFormat           string
	AudioCharacterCount   int
	VideoDurationSeconds  int
	VideoResolution       string
	VideoRatio            string
	VideoCount            int
	ErrorCode             string
	ErrorMessage          string
	UsageRecordedAt       *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
	SubmittedAt           *time.Time
	CompletedAt           *time.Time
}

type MediaGenerationJobRepository interface {
	Create(ctx context.Context, job *MediaGenerationJob) error
	GetByPublicID(ctx context.Context, publicID string) (*MediaGenerationJob, error)
	UpdateFromUpstream(ctx context.Context, publicID string, update MediaGenerationJobUpdate) (*MediaGenerationJob, error)
	MarkUsageRecorded(ctx context.Context, publicID string, at time.Time) (bool, error)
}

type MediaGenerationJobUpdate struct {
	Status               string
	UpstreamStatus       string
	UpstreamRequestID    string
	UpstreamResponseJSON []byte
	ResultURL            string
	ResultContentType    string
	ExpiresAt            *time.Time
	VideoDurationSeconds int
	VideoResolution      string
	VideoRatio           string
	VideoCount           int
	ErrorCode            string
	ErrorMessage         string
	CompletedAt          *time.Time
}
```

- [ ] **Step 4: Add Ent schema**

Create `backend/ent/schema/media_generation_job.go`:

```go
package schema

import (
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type MediaGenerationJob struct {
	ent.Schema
}

func (MediaGenerationJob) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "media_generation_jobs"}}
}

func (MediaGenerationJob) Mixin() []ent.Mixin {
	return []ent.Mixin{mixins.TimeMixin{}}
}

func (MediaGenerationJob) Fields() []ent.Field {
	return []ent.Field{
		field.String("public_id").MaxLen(80).Unique(),
		field.String("kind").MaxLen(40),
		field.String("provider").MaxLen(40),
		field.String("platform").MaxLen(40),
		field.String("status").MaxLen(30).Validate(validateMediaGenerationJobStatus),
		field.String("upstream_status").MaxLen(80).Optional().Nillable(),
		field.String("upstream_task_id").MaxLen(160).Optional().Nillable(),
		field.String("upstream_request_id").MaxLen(160).Optional().Nillable(),
		field.Int64("user_id"),
		field.Int64("api_key_id"),
		field.Int64("group_id").Optional().Nillable(),
		field.Int64("account_id"),
		field.String("model").MaxLen(160),
		field.JSON("request_json", json.RawMessage{}).Optional(),
		field.JSON("upstream_response_json", json.RawMessage{}).Optional(),
		field.String("result_url").Optional().Nillable(),
		field.String("result_content_type").MaxLen(120).Optional().Nillable(),
		field.Time("expires_at").Optional().Nillable(),
		field.String("audio_voice").MaxLen(160).Optional().Nillable(),
		field.String("audio_format").MaxLen(80).Optional().Nillable(),
		field.Int("audio_character_count").Default(0),
		field.Int("video_duration_seconds").Default(0),
		field.String("video_resolution").MaxLen(40).Optional().Nillable(),
		field.String("video_ratio").MaxLen(40).Optional().Nillable(),
		field.Int("video_count").Default(0),
		field.String("error_code").MaxLen(120).Optional().Nillable(),
		field.String("error_message").Optional().Nillable(),
		field.Time("usage_recorded_at").Optional().Nillable(),
		field.Time("submitted_at").Optional().Nillable(),
		field.Time("completed_at").Optional().Nillable(),
	}
}

func (MediaGenerationJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("public_id").Unique(),
		index.Fields("user_id", "created_at"),
		index.Fields("api_key_id", "created_at"),
		index.Fields("account_id", "status"),
		index.Fields("provider", "upstream_task_id"),
	}
}

func validateMediaGenerationJobStatus(status string) error {
	switch status {
	case "queued", "running", "succeeded", "failed", "canceled", "unknown":
		return nil
	default:
		return fmt.Errorf("invalid media generation job status: %s", status)
	}
}
```

- [ ] **Step 5: Generate Ent code**

Run:

```bash
cd backend && go generate ./ent
```

Expected: generated `mediagenerationjob` files appear under `backend/ent`.

- [ ] **Step 6: Implement repository**

Create `backend/internal/repository/media_generation_job_repo.go` with Ent-backed methods and a nil-client in-memory fallback:

```go
func NewMediaGenerationJobRepository(client *ent.Client) service.MediaGenerationJobRepository {
	return &mediaGenerationJobRepository{client: client, memory: map[string]*service.MediaGenerationJob{}}
}

type mediaGenerationJobRepository struct {
	client *ent.Client
	mu     sync.Mutex
	memory map[string]*service.MediaGenerationJob
}
```

Implement `Create`, `GetByPublicID`, `UpdateFromUpstream`, and `MarkUsageRecorded`. `MarkUsageRecorded` must return `false` when `usage_recorded_at` is already set.

Add `NewMediaGenerationJobRepository` to `backend/internal/repository/wire.go` provider set.

- [ ] **Step 7: Run repository tests**

Run:

```bash
cd backend && go test -tags=unit ./internal/repository -run 'TestMediaGenerationJobRepository' -count=1
```

Expected: pass.

## Task 3: Provider Request Builders

**Files:**
- Create: `backend/internal/service/media_generation_provider.go`
- Test: `backend/internal/service/media_generation_provider_test.go`

- [ ] **Step 1: Write failing provider builder tests**

Create `backend/internal/service/media_generation_provider_test.go`:

```go
package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildAzureSpeechSSML_EscapesInputAndAppliesVoice(t *testing.T) {
	ssml := buildAzureSpeechSSML(AzureSpeechRequest{
		Input:    `hello <world> & "friends"`,
		Voice:    "zh-CN-XiaoxiaoNeural",
		Language: "zh-CN",
		Speed:    1.2,
	})

	require.Contains(t, ssml, `xml:lang="zh-CN"`)
	require.Contains(t, ssml, `name="zh-CN-XiaoxiaoNeural"`)
	require.Contains(t, ssml, `hello &lt;world&gt; &amp; &#34;friends&#34;`)
	require.Contains(t, ssml, `rate="+20%"`)
}

func TestMapAzureSpeechOutputFormat(t *testing.T) {
	require.Equal(t, "audio-24khz-48kbitrate-mono-mp3", mapAzureSpeechOutputFormat("mp3"))
	require.Equal(t, "riff-24khz-16bit-mono-pcm", mapAzureSpeechOutputFormat("wav"))
	require.Equal(t, "ogg-24khz-16bit-mono-opus", mapAzureSpeechOutputFormat("opus"))
	require.Equal(t, "audio-24khz-48kbitrate-mono-mp3", mapAzureSpeechOutputFormat(""))
}

func TestBuildDashScopeVideoRequest(t *testing.T) {
	body, err := buildDashScopeVideoRequest(VideoGenerationRequest{
		Model:      "happyhorse-1.0-r2v",
		Prompt:     "生成视频",
		Duration:   5,
		Ratio:      "16:9",
		Resolution: "720p",
		Watermark:  boolPtr(false),
		Media: []VideoGenerationMedia{{Type: "reference_image", URL: "https://example.com/a.png"}},
	})

	require.NoError(t, err)
	require.Equal(t, "happyhorse-1.0-r2v", gjson.GetBytes(body, "model").String())
	require.Equal(t, "生成视频", gjson.GetBytes(body, "input.prompt").String())
	require.Equal(t, "720P", gjson.GetBytes(body, "parameters.resolution").String())
	require.False(t, gjson.GetBytes(body, "parameters.watermark").Bool())
}

func TestBuildArkVideoRequest(t *testing.T) {
	body, err := buildArkVideoRequest(VideoGenerationRequest{
		Model:         "doubao-seedance-2-0-260128",
		Prompt:        "生成视频",
		Duration:      8,
		Ratio:         "16:9",
		GenerateAudio: boolPtr(true),
		Media: []VideoGenerationMedia{
			{Type: "reference_image", URL: "https://example.com/a.png"},
			{Type: "reference_video", URL: "https://example.com/a.mp4"},
			{Type: "reference_audio", URL: "https://example.com/a.mp3"},
		},
	})

	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	require.Equal(t, "doubao-seedance-2-0-260128", gjson.GetBytes(body, "model").String())
	require.Equal(t, "text", gjson.GetBytes(body, "content.0.type").String())
	require.Equal(t, "image_url", gjson.GetBytes(body, "content.1.type").String())
	require.Equal(t, "video_url", gjson.GetBytes(body, "content.2.type").String())
	require.Equal(t, "audio_url", gjson.GetBytes(body, "content.3.type").String())
	require.True(t, gjson.GetBytes(body, "generate_audio").Bool())
}
```

- [ ] **Step 2: Run the failing provider tests**

Run:

```bash
cd backend && go test -tags=unit ./internal/service -run 'TestBuildAzureSpeechSSML|TestMapAzureSpeechOutputFormat|TestBuildDashScopeVideoRequest|TestBuildArkVideoRequest' -count=1
```

Expected: fail because builder types/functions do not exist.

- [ ] **Step 3: Implement provider builders**

Create `backend/internal/service/media_generation_provider.go` with:

```go
type AzureSpeechRequest struct {
	Model          string
	Input          string
	Voice          string
	Language       string
	ResponseFormat string
	Speed          float64
}

type VideoGenerationMedia struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type VideoGenerationRequest struct {
	Model         string
	Prompt        string
	Media         []VideoGenerationMedia
	Duration      int
	Ratio         string
	Resolution    string
	Watermark     *bool
	GenerateAudio *bool
	Seed          *int64
}
```

Implement:

- `buildAzureSpeechSSML(req AzureSpeechRequest) string`
- `mapAzureSpeechOutputFormat(format string) string`
- `buildDashScopeVideoRequest(req VideoGenerationRequest) ([]byte, error)`
- `buildArkVideoRequest(req VideoGenerationRequest) ([]byte, error)`
- `normalizeDashScopeResolution(resolution string) string`
- `mapArkMediaType(media VideoGenerationMedia) (contentType string, field string, role string, ok bool)`

- [ ] **Step 4: Run provider tests**

Run:

```bash
cd backend && go test -tags=unit ./internal/service -run 'TestBuildAzureSpeechSSML|TestMapAzureSpeechOutputFormat|TestBuildDashScopeVideoRequest|TestBuildArkVideoRequest' -count=1
```

Expected: pass.

## Task 4: Media Generation Service

**Files:**
- Create: `backend/internal/service/media_generation_service.go`
- Test: `backend/internal/service/media_generation_service_test.go`

- [ ] **Step 1: Write failing service tests**

Create `backend/internal/service/media_generation_service_test.go` with tests for:

```go
func TestMediaGenerationService_ForwardAzureSpeech_ReturnsBinaryAudio(t *testing.T) {
	// Use httpUpstreamRecorder from existing service tests.
	// Assert target URL, Azure headers, SSML body, content type, and Usage fields.
}

func TestMediaGenerationService_CreateDashScopeVideoJob_PersistsOriginalAccount(t *testing.T) {
	// Stub account selector returns dashscope account.
	// Upstream returns {"output":{"task_id":"task-1","task_status":"PENDING"},"request_id":"req-1"}.
	// Assert local job has account_id, provider dashscope, upstream task id.
}

func TestMediaGenerationService_QueryDashScopeVideoJob_RecordsUsageOnce(t *testing.T) {
	// Repo contains job with provider dashscope and account_id.
	// Upstream returns SUCCEEDED with output.video_url and usage.
	// Call query twice and assert usage repo Create called once.
}

func TestMediaGenerationService_CreateArkVideoJob_BuildsContentGenerationTask(t *testing.T) {
	// Assert POST /contents/generations/tasks, Authorization header, content[] body.
}
```

- [ ] **Step 2: Run failing service tests**

Run:

```bash
cd backend && go test -tags=unit ./internal/service -run 'TestMediaGenerationService' -count=1
```

Expected: fail because `MediaGenerationService` does not exist.

- [ ] **Step 3: Implement service constructor and public methods**

Create `backend/internal/service/media_generation_service.go`:

```go
type MediaGenerationService struct {
	accountRepo AccountRepository
	jobRepo     MediaGenerationJobRepository
	usageRepo   UsageLogRepository
	httpUpstream HTTPUpstream
	cfg         *config.Config
}

func NewMediaGenerationService(
	accountRepo AccountRepository,
	jobRepo MediaGenerationJobRepository,
	usageRepo UsageLogRepository,
	httpUpstream HTTPUpstream,
	cfg *config.Config,
) *MediaGenerationService {
	return &MediaGenerationService{accountRepo: accountRepo, jobRepo: jobRepo, usageRepo: usageRepo, httpUpstream: httpUpstream, cfg: cfg}
}
```

Add public methods:

```go
func (s *MediaGenerationService) ForwardAzureSpeech(ctx context.Context, account *Account, req AzureSpeechRequest) (*MediaSyncAudioResult, []byte, http.Header, error)
func (s *MediaGenerationService) CreateAudioSpeechJob(ctx context.Context, meta MediaRequestMeta, account *Account, req AzureSpeechRequest) (*MediaGenerationJob, error)
func (s *MediaGenerationService) RefreshAudioSpeechJob(ctx context.Context, job *MediaGenerationJob, account *Account) (*MediaGenerationJob, error)
func (s *MediaGenerationService) CreateVideoJob(ctx context.Context, meta MediaRequestMeta, account *Account, req VideoGenerationRequest) (*MediaGenerationJob, error)
func (s *MediaGenerationService) RefreshVideoJob(ctx context.Context, job *MediaGenerationJob, account *Account) (*MediaGenerationJob, error)
```

Define `MediaRequestMeta` with `UserID`, `APIKeyID`, `GroupID`, and `RequestJSON`.

- [ ] **Step 4: Implement provider calls**

Implement Azure realtime, Azure batch create/query, DashScope create/query, and Ark create/query in the service. Use `account.GetCredential("...")` for credentials. Return `UpstreamFailoverError` for retryable upstream statuses before handlers write a response.

- [ ] **Step 5: Implement usage recording with zero cost**

Add helper:

```go
func (s *MediaGenerationService) recordMediaUsage(ctx context.Context, job *MediaGenerationJob) error
```

Write a `UsageLog` with:

- `Model: job.Model`
- `UserID`, `APIKeyID`, `AccountID`, `GroupID`
- `RequestType: RequestTypeSync`
- `BillingMode: string(BillingModeAudio)` for audio jobs and `string(BillingModeVideo)` for video jobs
- `TotalCost` and `ActualCost` left zero until media pricing is configured
- media request/result details remain in `MediaGenerationJob`; usage log stores normal account, user, model, and accounting fields only

- [ ] **Step 6: Run service tests**

Run:

```bash
cd backend && go test -tags=unit ./internal/service -run 'TestMediaGenerationService|TestBuildAzureSpeechSSML|TestBuildDashScopeVideoRequest|TestBuildArkVideoRequest' -count=1
```

Expected: pass.

## Task 5: Media Handler Integration

**Files:**
- Modify: `backend/internal/handler/media_generation_handler.go`
- Modify: `backend/internal/handler/handlers.go`
- Test: `backend/internal/handler/media_generation_handler_test.go`

- [ ] **Step 1: Write failing handler tests**

Create `backend/internal/handler/media_generation_handler_test.go` with focused tests for:

```go
func TestMediaGenerationHandler_AudioSpeechRequiresModelAndInput(t *testing.T) {
	// Empty input returns 400 invalid_request_error.
}

func TestMediaGenerationHandler_CreateVideoGenerationReturnsJobObject(t *testing.T) {
	// Stub service returns MediaGenerationJob{PublicID:"vidjob_test"}.
	// Assert JSON object video.generation.job and status queued.
}

func TestMediaGenerationHandler_GetVideoGenerationEnforcesOwnership(t *testing.T) {
	// Stub repo/service returns job for another API key.
	// Assert 404.
}
```

- [ ] **Step 2: Run failing handler tests**

Run:

```bash
cd backend && go test -tags=unit ./internal/handler -run 'TestMediaGenerationHandler' -count=1
```

Expected: fail until handler has dependencies and logic.

- [ ] **Step 3: Add handler dependencies**

Change `MediaGenerationHandler` to:

```go
type MediaGenerationHandler struct {
	mediaService        *service.MediaGenerationService
	gatewayService      *service.GatewayService
	billingCacheService *service.BillingCacheService
}

func NewMediaGenerationHandler(mediaService *service.MediaGenerationService, gatewayService *service.GatewayService, billingCacheService *service.BillingCacheService) *MediaGenerationHandler {
	return &MediaGenerationHandler{mediaService: mediaService, gatewayService: gatewayService, billingCacheService: billingCacheService}
}
```

- [ ] **Step 4: Implement request parsing and account selection**

For sync audio:

1. Read request body with `pkghttputil.ReadRequestBodyWithPrealloc`.
2. Parse `model`, `input`, `voice`, `response_format`, `speed`, `language`.
3. Select account with `gatewayService.SelectAccountWithLoadAwareness(ctx, apiKey.GroupID, "", model, failedAccountIDs, "", subject.UserID)`.
4. Require `account.Platform == service.PlatformAzureSpeech`.
5. Call `mediaService.ForwardAzureSpeech`.
6. Write returned binary bytes with upstream content type.

For async create:

1. Select matching provider account by model.
2. Call create method.
3. Return OpenAI-style job object.

For query:

1. Load local job by public ID.
2. Verify `job.APIKeyID == apiKey.ID` or `job.UserID == subject.UserID`.
3. Load original account by `job.AccountID`.
4. Refresh status using provider-specific service method.
5. Return job object.

- [ ] **Step 5: Run handler tests**

Run:

```bash
cd backend && go test -tags=unit ./internal/handler -run 'TestMediaGenerationHandler' -count=1
```

Expected: pass.

## Task 6: Full Route And Compile Verification

**Files:**
- All files touched above

- [ ] **Step 1: Run focused tests**

Run:

```bash
cd backend && go test -tags=unit ./internal/server/routes ./internal/service ./internal/handler ./internal/repository -run 'MediaGeneration|AzureSpeech|DashScope|Ark|VideoGeneration|AudioSpeech' -count=1
```

Expected: pass.

- [ ] **Step 2: Run route and handler suites**

Run:

```bash
cd backend && go test -tags=unit ./internal/server/routes ./internal/handler -count=1
```

Expected: pass.

- [ ] **Step 3: Run service package with known caveat**

Run:

```bash
cd backend && go test -tags=unit ./internal/service -run 'MediaGeneration|AzureSpeech|DashScope|Ark|VideoGeneration|AudioSpeech' -count=1
```

Expected: pass. Do not use unrelated `TestGetModelPricing_OpenAIGPT51Fallback` failure as evidence against this feature; it is a pre-existing pricing test mismatch.

- [ ] **Step 4: Run go test compile for affected packages**

Run:

```bash
cd backend && go test -tags=unit ./internal/server/routes ./internal/handler ./internal/service ./internal/repository -run '^$' -count=1
```

Expected: compile pass.

## Self-Review

- Spec coverage: routes, Azure realtime, Azure batch, DashScope HappyHorse, Volcengine Ark Seedance, local async persistence, ownership checks, zero-cost usage, and no Codex bridge are all covered.
- Scope control: object storage transfer, real media pricing, and Codex `/v1/responses` bridging are excluded.
- Type consistency: media job constants, provider constants, request structs, repository interface, and handler method names are defined before use.
- Test strategy: every behavior starts with a failing unit test before implementation code.
