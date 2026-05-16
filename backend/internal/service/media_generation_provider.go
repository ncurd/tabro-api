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
	input := map[string]any{
		"prompt": req.Prompt,
	}
	mediaItems := make([]map[string]string, 0, len(req.Media))
	for _, media := range req.Media {
		if media.Type == "reference_image" && media.URL != "" {
			mediaItems = append(mediaItems, map[string]string{
				"type": media.Type,
				"url":  media.URL,
			})
		}
	}
	if len(mediaItems) > 0 {
		input["media"] = mediaItems
	}

	parameters := map[string]any{}
	if req.Duration > 0 {
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

	body := map[string]any{
		"model":      req.Model,
		"input":      input,
		"parameters": parameters,
	}
	return json.Marshal(body)
}

func buildArkVideoRequest(req VideoGenerationRequest) ([]byte, error) {
	content := []map[string]any{
		{
			"type": "text",
			"text": req.Prompt,
		},
	}

	for _, media := range req.Media {
		contentType, field, role, ok := mapArkMediaType(media)
		if !ok || media.URL == "" {
			continue
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
	if req.Duration > 0 {
		body["duration"] = req.Duration
	}
	if req.Ratio != "" {
		body["ratio"] = req.Ratio
	}
	if req.Resolution != "" {
		body["resolution"] = req.Resolution
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
	case "reference_image":
		return "image_url", "image_url", "reference_image", true
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
