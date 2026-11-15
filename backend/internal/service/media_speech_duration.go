package service

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

const MediaSpeechOutputSecondsMaximum = 600
const mediaSpeechPCMBytesPerSecond = 24000 * 2 // 24 kHz, one channel, signed PCM16.

// MediaSpeechOutputSecondsLimit describes only adapters with an enforced output
// bound and measurable audio. Callers must still validate the selected format
// and the mapped supplier model before dispatching a duration-priced request.
func MediaSpeechOutputSecondsLimit(model string) (int, bool) {
	if IsQwenRealtimeTTSModel(model) || strings.EqualFold(strings.TrimSpace(model), "azure-tts") {
		return MediaSpeechOutputSecondsMaximum, true
	}
	return 0, false
}

func mediaSpeechDurationRequired(snapshot *MediaBillingSnapshot) bool {
	return snapshot != nil && (snapshot.Price.Mode == BillingModeDuration ||
		snapshot.AccountPrice != nil && snapshot.AccountPrice.Mode == BillingModeDuration)
}

func mediaUnsupportedSpeechDuration(reason string) error {
	return &InvalidMediaRequestError{Message: "duration-priced speech is unavailable: " + reason}
}

func mediaSpeechPCMSeconds(size int64) (json.Number, error) {
	if size <= 0 || size%2 != 0 {
		return "", fmt.Errorf("%w: incomplete PCM16 audio samples", ErrMediaUsageIncomplete)
	}
	// Preserve fractional seconds independently of network or synthesis time.
	seconds := decimal.NewFromInt(size).DivRound(decimal.NewFromInt(mediaSpeechPCMBytesPerSecond), 12)
	return json.Number(seconds.String()), nil
}

func speechTTSUsageEvidence(requestID string, characters int64, pcmBytes int64, complete bool) []byte {
	usage := map[string]any{}
	if characters > 0 {
		usage["characters"] = characters
	}
	if seconds, err := mediaSpeechPCMSeconds(pcmBytes); err == nil {
		usage["audio_seconds"] = seconds
	}
	body, _ := json.Marshal(map[string]any{"request_id": requestID, "usage": usage, "usage_complete": complete})
	return body
}

// Azure's wav and pcm options both request RIFF PCM24k mono16. Walk the real
// chunks instead of subtracting 44 bytes: WAV can contain extra metadata chunks.
// A malformed header, compressed format, duplicate chunk, or truncated data is
// not evidence of a billable playback duration.
func mediaSpeechWAVPCMBytes(body []byte) (int64, error) {
	invalid := func() (int64, error) {
		return 0, fmt.Errorf("%w: expected complete 24kHz mono PCM16 WAV audio", ErrMediaUsageIncomplete)
	}
	if len(body) < 12 || string(body[:4]) != "RIFF" || string(body[8:12]) != "WAVE" || uint64(binary.LittleEndian.Uint32(body[4:8]))+8 != uint64(len(body)) {
		return invalid()
	}
	foundFormat, foundData := false, false
	var pcmBytes int64
	for position := 12; position < len(body); {
		if len(body)-position < 8 {
			return invalid()
		}
		size := uint64(binary.LittleEndian.Uint32(body[position+4 : position+8]))
		end := uint64(position) + 8 + size
		paddedEnd := end + size%2
		if paddedEnd > uint64(len(body)) {
			return invalid()
		}
		switch string(body[position : position+4]) {
		case "fmt ":
			if foundFormat || size < 16 {
				return invalid()
			}
			format := body[position+8 : int(end)]
			if binary.LittleEndian.Uint16(format[0:2]) != 1 || binary.LittleEndian.Uint16(format[2:4]) != 1 ||
				binary.LittleEndian.Uint32(format[4:8]) != 24000 || binary.LittleEndian.Uint32(format[8:12]) != mediaSpeechPCMBytesPerSecond ||
				binary.LittleEndian.Uint16(format[12:14]) != 2 || binary.LittleEndian.Uint16(format[14:16]) != 16 {
				return invalid()
			}
			foundFormat = true
		case "data":
			if foundData || size == 0 || size%2 != 0 {
				return invalid()
			}
			foundData, pcmBytes = true, int64(size)
		}
		position = int(paddedEnd)
	}
	if !foundFormat || !foundData {
		return invalid()
	}
	return pcmBytes, nil
}
