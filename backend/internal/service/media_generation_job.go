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
	ID                   int64
	PublicID             string
	Kind                 string
	Provider             string
	Platform             string
	Status               string
	UpstreamStatus       string
	UpstreamTaskID       string
	UpstreamRequestID    string
	UserID               int64
	APIKeyID             int64
	GroupID              *int64
	AccountID            int64
	Model                string
	RequestJSON          []byte
	UpstreamResponseJSON []byte
	ResultURL            string
	ResultContentType    string
	ExpiresAt            *time.Time
	AudioVoice           string
	AudioFormat          string
	AudioCharacterCount  int
	VideoDurationSeconds int
	VideoResolution      string
	VideoRatio           string
	VideoCount           int
	ErrorCode            string
	ErrorMessage         string
	UsageRecordedAt      *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	SubmittedAt          *time.Time
	CompletedAt          *time.Time
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
