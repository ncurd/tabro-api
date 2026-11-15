package service

import (
	"encoding/json"
	"fmt"
	"strconv"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
)

func applyFrozenMediaDurationBound(bounds map[string]bc.Decimal, raw json.RawMessage) error {
	var snapshot gatewayCreditPriceSnapshot
	if json.Unmarshal(raw, &snapshot) != nil {
		return bc.ErrState
	}
	if snapshot.MediaPrice == nil || snapshot.MediaPrice.Unit != mediaUnitOutputAudioSecond {
		return nil
	}
	if snapshot.MediaKind != MediaJobKindAudioSpeech || snapshot.MediaPrice.Mode != BillingModeDuration ||
		snapshot.MediaOutputSecondsLimit <= 0 || snapshot.MediaOutputSecondsLimit > MediaSpeechOutputSecondsMaximum || bounds == nil {
		return fmt.Errorf("%w: frozen output audio duration bound is invalid", bc.ErrState)
	}
	bounds["audio_seconds"] = bc.Decimal(strconv.Itoa(snapshot.MediaOutputSecondsLimit))
	return nil
}

// Shadow estimates must use the original authority quote's meter. The local
// debit can use generated seconds while the legacy quote still prices text.
func shadowSpeechUsesCharacterQuote(snapshot *bc.ExecutionSnapshot) bool {
	return snapshot != nil && snapshot.Mode == "shadow" &&
		snapshot.Quote.Request.MaximumUsage["audio_characters"] != "" && snapshot.Quote.Request.MaximumUsage["audio_seconds"] == ""
}
