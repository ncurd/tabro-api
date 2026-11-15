package service

import (
	"fmt"

	"github.com/tidwall/gjson"
)

// Frozen units distinguish generated speech seconds from legacy character
// pricing. Never infer duration from request latency, text length, or a URL.
func mediaBillableUnitsForPrice(job *MediaGenerationJob, kind string, price MediaPriceSnapshot) (float64, string, error) {
	if price.Mode != BillingModeDuration {
		return mediaBillableUnits(job, kind)
	}
	switch {
	case kind == MediaJobKindAudioTranscription && price.Unit == mediaUnitInputAudioSecond:
		return mediaBillableUnits(job, kind)
	case kind == MediaJobKindVideoGeneration && price.Unit == mediaUnitOutputVideoSecond:
		return mediaBillableUnits(job, kind)
	case kind == MediaJobKindAudioSpeech && price.Unit == mediaUnitOutputAudioSecond:
		quantity, err := exactPositiveMediaQuantity(gjson.GetBytes(job.UpstreamResponseJSON, "usage.audio_seconds"))
		if err != nil {
			return 0, "", fmt.Errorf("%w: generated audio duration is missing", ErrMediaUsageIncomplete)
		}
		seconds, _ := quantity.Float64()
		if !validMediaPrice(seconds) || seconds <= 0 || seconds > MediaSpeechOutputSecondsMaximum {
			return 0, "", fmt.Errorf("%w: invalid generated audio duration", ErrMediaUsageIncomplete)
		}
		return seconds, "", nil
	default:
		return 0, "", fmt.Errorf("%w: duration price direction differs from media kind", ErrMediaUsageIncomplete)
	}
}
