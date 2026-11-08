package service

import "github.com/tidwall/gjson"

// Only content deltas without fields handled by the compatibility rewrites may
// bypass decoding. Validate the whole payload and inspect all top-level keys so
// escaped or repeated keys retain encoding/json's last-value semantics.
func anthropicContentDeltaCanPassThrough(data string) bool {
	if !gjson.Valid(data) {
		return false
	}
	eventType := ""
	canPassThrough := true
	gjson.Parse(data).ForEach(func(key, value gjson.Result) bool {
		switch key.Str {
		case "type":
			eventType = value.Str
		case "message", "content_block":
			canPassThrough = false
			return false
		}
		return true
	})
	return canPassThrough && eventType == "content_block_delta"
}
