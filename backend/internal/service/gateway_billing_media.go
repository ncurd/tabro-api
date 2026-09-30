package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
)

func mediaBillingQuoteRequest(path, operationID string, body []byte) (bc.QuoteRequest, bool, error) {
	path = strings.TrimPrefix(path, "/v1")
	q := bc.QuoteRequest{OperationID: operationID, ServiceTier: "default", RequestPayloadHash: HashUsageRequestPayload(append([]byte(path+"\x00"), body...))}
	switch path {
	case "/videos/generations", "/audio/transcriptions", "/audio/voices", "/audio/speech", "/audio/speech/jobs":
	default:
		return q, false, nil
	}
	if !json.Valid(body) {
		return q, true, bc.ErrConflict
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		return q, true, fmt.Errorf("model is required")
	}
	q.ProductKey = "ai:" + model
	q.MaximumUsage = map[string]bc.Decimal{"request_count": "1"}
	switch path {
	case "/videos/generations":
		var req VideoGenerationRequest
		if json.Unmarshal(body, &req) != nil {
			return q, true, bc.ErrConflict
		}
		duration := req.Duration
		if duration == 0 {
			duration = 5
		}
		if duration == -1 && strings.HasPrefix(model, "doubao-seedance-2-0") {
			duration = 15
		}
		if duration <= 0 || duration > 3600 {
			return q, true, fmt.Errorf("video duration requires an enforced positive bound")
		}
		q.MaximumUsage["video_seconds"] = bc.Decimal(strconv.Itoa(duration))
		if req.Resolution != "" {
			q.ServiceTier = normalizeMediaBillingTier(req.Resolution)
		} else {
			q.ServiceTier = "720P"
		}
	case "/audio/transcriptions":
		// Only the realtime Flash family is accepted by the service. Its documented
		// provider limit is one file of at most five minutes; file-trans is separate.
		if model != "qwen3-asr-flash" && model != "qwen3-asr-flash-2026-02-10" && model != "qwen3-asr-flash-2025-09-08" {
			return q, true, fmt.Errorf("audio model has no verified duration bound")
		}
		q.MaximumUsage["audio_seconds"] = "300"
	case "/audio/voices":
		q.ProductKey = "ai:" + QwenVoiceEnrollmentModel
	case "/audio/speech", "/audio/speech/jobs":
		input := gjson.GetBytes(body, "input").String()
		if len([]rune(input)) == 0 {
			return q, true, fmt.Errorf("speech input is required")
		}
		q.MaximumUsage["audio_characters"] = bc.Decimal(strconv.Itoa(len([]rune(input))))
	}
	return q, true, nil
}

// Persisted media jobs contain only the frozen quote and operation identity.
// A worker never needs to retain an end-user bearer token to settle the result.
func handoffMediaExecution(ctx context.Context) {
	if e := bc.ExecutionFromContext(ctx); e != nil {
		e.MarkHandedOff()
	}
}

func exactPositiveMediaQuantity(value gjson.Result) (decimal.Decimal, error) {
	raw := value.Raw
	if value.Type == gjson.String {
		raw = value.String()
	}
	v, err := decimal.NewFromString(raw)
	if err != nil || !v.IsPositive() {
		return decimal.Zero, ErrMediaUsageIncomplete
	}
	return v, nil
}
func mediaBillingMeters(job *MediaGenerationJob) (map[string]bc.Decimal, error) {
	usage := map[string]bc.Decimal{"request_count": "1"}
	body := job.UpstreamResponseJSON
	switch job.Kind {
	case MediaJobKindVideoGeneration:
		paths := []string{"duration", "usage.duration"}
		if job.Provider == MediaProviderDashScope {
			paths = []string{"usage.output_video_duration", "usage.duration"}
		}
		var value decimal.Decimal
		var err error = ErrMediaUsageIncomplete
		for _, path := range paths {
			if v := gjson.GetBytes(body, path); v.Exists() {
				value, err = exactPositiveMediaQuantity(v)
				break
			}
		}
		if err != nil && job.Provider == MediaProviderVolcengineArk {
			frames, e1 := exactPositiveMediaQuantity(gjson.GetBytes(body, "frames"))
			fps, e2 := exactPositiveMediaQuantity(gjson.GetBytes(body, "framespersecond"))
			if e1 == nil && e2 == nil {
				value = frames.DivRound(fps, 12)
				err = nil
			}
		}
		if err != nil {
			return nil, err
		}
		usage["video_seconds"] = bc.Decimal(value.String())
	case MediaJobKindAudioSpeech:
		value, err := exactPositiveMediaQuantity(gjson.GetBytes(body, "usage.characters"))
		if err != nil && job.AudioCharacterCount > 0 {
			value = decimal.NewFromInt(int64(job.AudioCharacterCount))
			err = nil
		}
		if err != nil {
			return nil, err
		}
		usage["audio_characters"] = bc.Decimal(value.String())
	case MediaJobKindAudioTranscription:
		value, err := exactPositiveMediaQuantity(gjson.GetBytes(body, "usage.seconds"))
		if err != nil {
			return nil, err
		}
		usage["audio_seconds"] = bc.Decimal(value.String())
	case MediaJobKindVoiceClone:
	default:
		return nil, ErrMediaUsageIncomplete
	}
	return usage, nil
}
