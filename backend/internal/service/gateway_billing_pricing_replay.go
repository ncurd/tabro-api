package service

import (
	"bytes"
	"encoding/json"
	"fmt"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/shopspring/decimal"
)

// restoreGatewayPricingNumberEncoding recreates the Go encoding of known
// pricing fields after PostgreSQL JSONB has expanded exponent notation. It is
// used only to recover an existing intent's original fingerprint. The stored
// snapshot remains unchanged, and unknown fields and decimal strings survive.
func restoreGatewayPricingNumberEncoding(raw json.RawMessage) (json.RawMessage, error) {
	var snapshot gatewayCreditPriceSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("%w: invalid frozen gateway price", bc.ErrConflict)
	}
	knownJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid frozen gateway price", bc.ErrConflict)
	}
	decode := func(data []byte) (any, error) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		err := decoder.Decode(&value)
		return value, err
	}
	original, err := decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid frozen gateway price", bc.ErrConflict)
	}
	known, err := decode(knownJSON)
	if err != nil {
		return nil, err
	}
	restored, err := restoreKnownGatewayPriceNumbers(original, known)
	if err != nil {
		return nil, err
	}
	return json.Marshal(restored)
}

func restoreKnownGatewayPriceNumbers(original, known any) (any, error) {
	switch expected := known.(type) {
	case json.Number:
		if actual, ok := original.(json.Number); ok {
			// Restoring notation must not round a changed decimal price back
			// onto the old float64 value, including changes below float precision.
			before, beforeErr := decimal.NewFromString(string(actual))
			after, afterErr := decimal.NewFromString(string(expected))
			if beforeErr != nil || afterErr != nil || !before.Equal(after) {
				return nil, fmt.Errorf("%w: frozen gateway price changed", bc.ErrConflict)
			}
			return expected, nil
		}
	case map[string]any:
		if object, ok := original.(map[string]any); ok {
			for key, value := range object {
				if knownValue, exists := expected[key]; exists {
					restored, err := restoreKnownGatewayPriceNumbers(value, knownValue)
					if err != nil {
						return nil, err
					}
					object[key] = restored
				}
			}
		}
	case []any:
		if array, ok := original.([]any); ok {
			for i := range array {
				if i < len(expected) {
					restored, err := restoreKnownGatewayPriceNumbers(array[i], expected[i])
					if err != nil {
						return nil, err
					}
					array[i] = restored
				}
			}
		}
	}
	return original, nil
}
