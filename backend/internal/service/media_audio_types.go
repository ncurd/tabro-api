package service

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

const (
	MediaJobKindVoiceClone         = "voice_clone"
	MediaJobKindAudioTranscription = "audio_transcription"
	QwenVoiceEnrollmentModel       = "qwen-voice-enrollment"
	QwenVoiceCloneModel            = "qwen3-tts-vc-2026-01-22"
)

var ErrClonedVoiceNotFound = errors.New("cloned voice not found")
var qwenVoiceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)

type AudioTranscriptionRequest struct {
	Model       string `json:"model"`
	AudioURL    string `json:"audio_url,omitempty"`
	AudioBase64 string `json:"audio_base64,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
	Language    string `json:"language,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	EnableITN   *bool  `json:"enable_itn,omitempty"`
}

type AudioTranscriptionResult struct {
	Text      string          `json:"text"`
	Model     string          `json:"model"`
	RequestID string          `json:"request_id,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
}

type VoiceCloneRequest struct {
	Model       string `json:"model"`
	Name        string `json:"name"`
	AudioURL    string `json:"audio_url,omitempty"`
	AudioBase64 string `json:"audio_base64,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
	Text        string `json:"text,omitempty"`
	Language    string `json:"language,omitempty"`
}

type VoiceResource struct {
	ID             string `json:"id"`
	Object         string `json:"object"`
	Model          string `json:"model"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	CreatedAt      int64  `json:"created_at"`
	Language       string `json:"language,omitempty"`
	FallbackMode   bool   `json:"fallback_mode,omitempty"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	AccountID      int64  `json:"-"`
	UpstreamModel  string `json:"-"`
}

type ClonedSpeechRequest struct {
	Model          string `json:"model"`
	Voice          string `json:"voice"`
	Input          string `json:"input"`
	Language       string `json:"language,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
}

type ClonedSpeechResult struct {
	URL       string          `json:"url"`
	ExpiresAt *int64          `json:"expires_at,omitempty"`
	Model     string          `json:"model"`
	Voice     string          `json:"voice"`
	RequestID string          `json:"request_id,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
}

// MediaAudioResourceRepository lists only local resources belonging to a caller.
// Listing the provider account's voices would disclose resources of other tenants.
type MediaAudioResourceRepository interface {
	ListMediaResources(context.Context, int64, int64, *int64, string, int, int) ([]*MediaGenerationJob, error)
}

func normalizeAudioInput(audioURL, encoded, mediaType string, cloning bool) (string, error) {
	if (audioURL == "") == (encoded == "") {
		return "", &InvalidMediaRequestError{Message: "provide exactly one of audio_url or audio_base64"}
	}
	value := audioURL
	if encoded != "" {
		if mediaType == "" {
			return "", &InvalidMediaRequestError{Message: "mime_type is required with audio_base64"}
		}
		value = "data:" + mediaType + ";base64," + encoded
	}
	if err := ValidateMediaInputURL(value, "audio", true, false); err != nil {
		return "", &InvalidMediaRequestError{Message: err.Error()}
	}
	if strings.HasPrefix(value, "data:") {
		mime, decodedBytes, err := validateMediaDataURL(value)
		if err != nil {
			return "", &InvalidMediaRequestError{Message: err.Error()}
		}
		// Qwen ASR also includes the encoded payload in its 10 MB input limit.
		if decodedBytes > 10*1024*1024 || (!cloning && len(value) > 10*1024*1024) {
			return "", &InvalidMediaRequestError{Message: "audio input exceeds the 10 MiB provider limit"}
		}
		if cloning && mime != "audio/wav" && mime != "audio/mpeg" && mime != "audio/mp4" {
			return "", &InvalidMediaRequestError{Message: "voice cloning requires audio/wav, audio/mpeg, or audio/mp4"}
		}
	}
	return value, nil
}
