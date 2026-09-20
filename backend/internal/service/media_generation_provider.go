package service

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"strings"
)

type AzureSpeechRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice,omitempty"`
	Language       string  `json:"language,omitempty"`
	ResponseFormat string  `json:"response_format,omitempty"`
	Speed          float64 `json:"speed,omitempty"`
}

type VideoGenerationMedia struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type VideoGenerationRequest struct {
	Model                 string                 `json:"model"`
	Prompt                string                 `json:"prompt,omitempty"`
	Media                 []VideoGenerationMedia `json:"media,omitempty"`
	Duration              int                    `json:"duration,omitempty"`
	Ratio                 string                 `json:"ratio,omitempty"`
	Resolution            string                 `json:"resolution,omitempty"`
	Watermark             *bool                  `json:"watermark,omitempty"`
	GenerateAudio         *bool                  `json:"generate_audio,omitempty"`
	Seed                  *int64                 `json:"seed,omitempty"`
	PromptExtend          *bool                  `json:"prompt_extend,omitempty"`
	ReturnLastFrame       *bool                  `json:"return_last_frame,omitempty"`
	CameraFixed           *bool                  `json:"camera_fixed,omitempty"`
	OutputFormat          string                 `json:"output_format,omitempty"`
	OmniReferenceTaskType string                 `json:"omni_reference_task_type,omitempty"`
	CallbackURL           string                 `json:"callback_url,omitempty"`
	ServiceTier           string                 `json:"service_tier,omitempty"`
	ExecutionExpiresAfter *int64                 `json:"execution_expires_after,omitempty"`
}

// InvalidMediaRequestError identifies a client request that cannot be forwarded.
type InvalidMediaRequestError struct{ Message string }

func (e *InvalidMediaRequestError) Error() string { return e.Message }

func (req VideoGenerationRequest) Validate() error {
	if strings.TrimSpace(req.Model) == "" {
		return &InvalidMediaRequestError{Message: "model is required"}
	}
	if strings.TrimSpace(req.Prompt) == "" && len(req.Media) == 0 {
		return &InvalidMediaRequestError{Message: "prompt or media is required"}
	}
	if req.Duration < -1 {
		return &InvalidMediaRequestError{Message: "duration must be positive or -1 for automatic duration"}
	}
	for i, media := range req.Media {
		if strings.TrimSpace(media.URL) == "" {
			return &InvalidMediaRequestError{Message: fmt.Sprintf("media[%d].url is required", i)}
		}
		switch media.Type {
		case "first_frame", "last_frame", "reference_image", "reference_video", "reference_audio", "file", "link":
		default:
			return &InvalidMediaRequestError{Message: fmt.Sprintf("unsupported media type: %s", media.Type)}
		}
		kind := videoMediaInputKind(media.Type)
		if err := ValidateMediaInputURL(media.URL, kind, kind == "image" || kind == "audio", true); err != nil {
			return &InvalidMediaRequestError{Message: fmt.Sprintf("media[%d].url: %s", i, err)}
		}
	}
	return nil
}

func videoMediaInputKind(mediaType string) string {
	switch mediaType {
	case "first_frame", "last_frame", "reference_image":
		return "image"
	case "reference_video":
		return "video"
	case "reference_audio":
		return "audio"
	default:
		return mediaType
	}
}

// Provider restrictions are checked before submission: this gateway never
// uploads inline material on the caller's behalf to manufacture a public URL.
func validateVideoProviderMedia(req VideoGenerationRequest, ark bool) error {
	for i, media := range req.Media {
		kind := videoMediaInputKind(media.Type)
		allowData := kind == "image" || (ark && kind == "audio")
		if err := ValidateMediaInputURL(media.URL, kind, allowData, ark); err != nil {
			return &InvalidMediaRequestError{Message: fmt.Sprintf("media[%d].url: %s", i, err)}
		}
		if !strings.HasPrefix(media.URL, "data:") {
			continue
		}
		mediaType, size, err := validateMediaDataURL(media.URL)
		if err != nil {
			return &InvalidMediaRequestError{Message: fmt.Sprintf("media[%d].url: %s", i, err)}
		}
		maxBytes := int64(20 << 20)
		if ark {
			maxBytes = 30 << 20
			if kind == "audio" {
				maxBytes = 15 << 20
			}
		}
		if size > maxBytes {
			return &InvalidMediaRequestError{Message: fmt.Sprintf("media[%d].url: inline %s exceeds the provider limit of %d MiB; use a smaller input", i, kind, maxBytes>>20)}
		}
		validMIME := false
		switch mediaType {
		case "image/jpeg", "image/png", "image/bmp", "image/webp":
			validMIME = kind == "image"
		case "image/tiff", "image/gif", "image/heic", "image/heif":
			validMIME = ark && kind == "image"
		case "audio/mpeg", "audio/mp3", "audio/wav", "audio/x-wav":
			validMIME = ark && kind == "audio"
		}
		if !validMIME {
			return &InvalidMediaRequestError{Message: fmt.Sprintf("media[%d].url: unsupported inline %s MIME type %s", i, kind, mediaType)}
		}
	}
	return nil
}

func buildAzureSpeechSSML(req AzureSpeechRequest) string {
	language := req.Language
	if language == "" {
		language = "en-US"
	}

	voice := req.Voice
	if voice == "" {
		voice = "en-US-JennyNeural"
	}

	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(req.Input))

	rate := azureSpeechRate(req.Speed)
	return fmt.Sprintf(`<speak version="1.0" xml:lang="%s"><voice name="%s"><prosody rate="%s">%s</prosody></voice></speak>`,
		xmlAttr(language),
		xmlAttr(voice),
		xmlAttr(rate),
		escaped.String(),
	)
}

func mapAzureSpeechOutputFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "wav", "pcm":
		return "riff-24khz-16bit-mono-pcm"
	case "opus", "ogg":
		return "ogg-24khz-16bit-mono-opus"
	default:
		return "audio-24khz-48kbitrate-mono-mp3"
	}
}

func buildDashScopeVideoRequest(req VideoGenerationRequest) ([]byte, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if err := validateVideoProviderMedia(req, false); err != nil {
		return nil, err
	}
	input := map[string]any{}
	if req.Prompt != "" {
		input["prompt"] = req.Prompt
	}
	if len(req.Media) > 0 {
		input["media"] = req.Media
	}

	parameters := map[string]any{}
	if req.Duration != 0 {
		parameters["duration"] = req.Duration
	}
	if req.Ratio != "" {
		parameters["ratio"] = req.Ratio
	}
	if resolution := normalizeDashScopeResolution(req.Resolution); resolution != "" {
		parameters["resolution"] = resolution
	}
	if req.Watermark != nil {
		parameters["watermark"] = *req.Watermark
	}
	if req.Seed != nil {
		parameters["seed"] = *req.Seed
	}

	if req.GenerateAudio != nil {
		parameters["audio"] = *req.GenerateAudio
	}
	if req.PromptExtend != nil {
		parameters["prompt_extend"] = *req.PromptExtend
	}

	body := map[string]any{
		"model":      req.Model,
		"input":      input,
		"parameters": parameters,
	}
	return json.Marshal(body)
}

func buildArkVideoRequest(req VideoGenerationRequest) ([]byte, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if err := validateVideoProviderMedia(req, true); err != nil {
		return nil, err
	}
	content := make([]map[string]any, 0, len(req.Media)+1)
	if req.Prompt != "" {
		content = append(content, map[string]any{"type": "text", "text": req.Prompt})
	}

	for _, media := range req.Media {
		contentType, field, role, ok := mapArkMediaType(media)
		if !ok {
			return nil, &InvalidMediaRequestError{Message: "unsupported Ark media type: " + media.Type}
		}
		content = append(content, map[string]any{
			"type": contentType,
			"role": role,
			field: map[string]any{
				"url": media.URL,
			},
		})
	}

	body := map[string]any{
		"model":   req.Model,
		"content": content,
	}
	if req.Duration != 0 {
		body["duration"] = req.Duration
	}
	if req.Ratio != "" {
		body["ratio"] = req.Ratio
	}
	if req.Resolution != "" {
		body["resolution"] = strings.ToLower(strings.TrimSpace(req.Resolution))
	}
	if req.Watermark != nil {
		body["watermark"] = *req.Watermark
	}
	if req.GenerateAudio != nil {
		body["generate_audio"] = *req.GenerateAudio
	}
	if req.Seed != nil {
		body["seed"] = *req.Seed
	}

	if req.ReturnLastFrame != nil {
		body["return_last_frame"] = *req.ReturnLastFrame
	}
	if req.CameraFixed != nil {
		body["camera_fixed"] = *req.CameraFixed
	}
	if req.OutputFormat != "" {
		body["output_format"] = req.OutputFormat
	}
	if req.OmniReferenceTaskType != "" {
		body["omni_reference_task_type"] = req.OmniReferenceTaskType
	}
	if req.CallbackURL != "" {
		body["callback_url"] = req.CallbackURL
	}
	if req.ServiceTier != "" {
		body["service_tier"] = req.ServiceTier
	}
	if req.ExecutionExpiresAfter != nil {
		body["execution_expires_after"] = *req.ExecutionExpiresAfter
	}

	return json.Marshal(body)
}

func normalizeDashScopeResolution(resolution string) string {
	switch strings.ToUpper(strings.TrimSpace(resolution)) {
	case "720P":
		return "720P"
	case "1080P":
		return "1080P"
	default:
		return strings.ToUpper(strings.TrimSpace(resolution))
	}
}

func mapArkMediaType(media VideoGenerationMedia) (contentType string, field string, role string, ok bool) {
	switch media.Type {
	case "reference_image", "first_frame", "last_frame":
		return "image_url", "image_url", media.Type, true
	case "reference_video":
		return "video_url", "video_url", "reference_video", true
	case "reference_audio":
		return "audio_url", "audio_url", "reference_audio", true
	default:
		return "", "", "", false
	}
}

func azureSpeechRate(speed float64) string {
	if speed <= 0 {
		speed = 1
	}
	percent := int(math.Round((speed - 1) * 100))
	if percent >= 0 {
		return fmt.Sprintf("+%d%%", percent)
	}
	return fmt.Sprintf("%d%%", percent)
}

func xmlAttr(value string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}
