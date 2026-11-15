package service

import (
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
)

// Native ASR start frames are already bounded to 64 KiB. Bound nesting too,
// and check each object independently, including objects inside arrays.
func nativeASRJSONKeysUnique(value gjson.Result, depth int) bool {
	if depth > 64 {
		return false
	}
	valid := true
	if value.IsObject() {
		seen := make(map[string]struct{})
		value.ForEach(func(key, child gjson.Result) bool {
			folded := nativeASRFoldJSONKey(key.String())
			if _, duplicate := seen[folded]; duplicate {
				valid = false
				return false
			}
			seen[folded] = struct{}{}
			valid = nativeASRJSONKeysUnique(child, depth+1)
			return valid
		})
	} else if value.IsArray() {
		value.ForEach(func(_, child gjson.Result) bool {
			valid = nativeASRJSONKeysUnique(child, depth+1)
			return valid
		})
	}
	return valid
}

// Match strings.EqualFold without an O(n²) pairwise key scan. Folding each rune
// to its smallest SimpleFold representative also handles non-ASCII fold cycles.
func nativeASRFoldJSONKey(key string) string {
	return strings.Map(func(r rune) rune {
		folded := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < folded {
				folded = next
			}
		}
		return folded
	}, key)
}
