package service

import "github.com/tidwall/gjson"

// applyOpenAIImageUsageDetails accepts only reported modality/cache counts.
// Missing modality data cannot be reconstructed from size or quality.
func applyOpenAIImageUsageDetails(body []byte, usage *OpenAIUsage) {
	value := gjson.GetBytes(body, "usage")
	image := value.Get("input_tokens_details.image_tokens")
	text := value.Get("input_tokens_details.text_tokens")
	validCount := func(v gjson.Result) bool {
		return v.Type == gjson.Number && v.Int() >= 0 && v.Float() == float64(v.Int())
	}
	if !validCount(image) || !validCount(text) || !validCount(value.Get("input_tokens")) || !validCount(value.Get("output_tokens")) {
		return
	}
	usage.ImageInputTokens = int(image.Int())
	if cached := value.Get("cached_tokens"); cached.Exists() {
		if !validCount(cached) {
			return
		}
		usage.CacheReadInputTokens = int(cached.Int())
	}
	imageCache, textCache := value.Get("cached_tokens_details.cached_image_tokens"), value.Get("cached_tokens_details.cached_text_tokens")
	if imageCache.Exists() || textCache.Exists() {
		if imageCache.Exists() && !validCount(imageCache) || textCache.Exists() && !validCount(textCache) {
			return
		}
		if !value.Get("cached_tokens").Exists() && !value.Get("input_tokens_details.cached_tokens").Exists() {
			if !imageCache.Exists() || !textCache.Exists() {
				return
			}
			usage.CacheReadInputTokens = int(imageCache.Int() + textCache.Int())
		}
		if imageCache.Exists() {
			usage.CacheReadImageTokens = int(imageCache.Int())
		} else {
			usage.CacheReadImageTokens = usage.CacheReadInputTokens - int(textCache.Int())
		}
		if imageCache.Exists() && textCache.Exists() && int(imageCache.Int()+textCache.Int()) != usage.CacheReadInputTokens {
			return
		}
	} else if usage.CacheReadInputTokens > 0 {
		switch {
		case image.Int() == 0:
			usage.CacheReadImageTokens = 0
		case text.Int() == 0:
			usage.CacheReadImageTokens = usage.CacheReadInputTokens
		default:
			return // A mixed cache total has no reliable modality split.
		}
	}
	if usage.CacheCreationInputTokens != 0 || image.Int()+text.Int() != int64(usage.InputTokens) || usage.CacheReadInputTokens < 0 || usage.CacheReadInputTokens > usage.InputTokens ||
		usage.CacheReadImageTokens < 0 || usage.CacheReadImageTokens > usage.ImageInputTokens || usage.CacheReadImageTokens > usage.CacheReadInputTokens ||
		usage.CacheReadInputTokens-usage.CacheReadImageTokens > int(text.Int()) {
		return
	}
	if output := value.Get("output_tokens_details.image_tokens"); output.Exists() {
		if !validCount(output) || output.Int() > int64(usage.OutputTokens) {
			return
		}
		usage.ImageOutputTokens = int(output.Int())
	}
	usage.ImageUsageComplete = true
}

// Unlike a general Responses usage object, an Images API/image_gen usage
// object has image output when no explicit output modality split is reported.
func extractOpenAIImageUsageFromJSONBytes(body []byte) (OpenAIUsage, bool) {
	usage, ok := extractOpenAIUsageFromJSONBytes(body)
	if ok && !gjson.GetBytes(body, "usage.output_tokens_details.image_tokens").Exists() {
		usage.ImageOutputTokens = usage.OutputTokens
	}
	return usage, ok
}

// OpenAIImageSVGUsage is charged separately from the image used as the SVG
// reference. ReasoningTokens is a subset of OutputTokens, not an extra charge.
type OpenAIImageSVGUsage struct {
	Model           string `json:"model"`
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	ReasoningTokens int    `json:"reasoning_tokens"`
	TotalTokens     int    `json:"total_tokens"`
}

func parseOpenAIImageSVGUsage(value gjson.Result) (OpenAIImageSVGUsage, bool) {
	usage := OpenAIImageSVGUsage{
		Model: value.Get("model").String(), InputTokens: int(value.Get("input_tokens").Int()),
		OutputTokens: int(value.Get("output_tokens").Int()), ReasoningTokens: int(value.Get("reasoning_tokens").Int()),
		TotalTokens: int(value.Get("total_tokens").Int()),
	}
	if !value.IsObject() || usage.Model == "" {
		return usage, false
	}
	for _, field := range []string{"input_tokens", "output_tokens", "reasoning_tokens", "total_tokens"} {
		v := value.Get(field)
		if v.Type != gjson.Number || v.Int() < 0 || v.Float() != float64(v.Int()) {
			return usage, false
		}
	}
	return usage, usage.TotalTokens == usage.InputTokens+usage.OutputTokens && usage.ReasoningTokens <= usage.OutputTokens
}
