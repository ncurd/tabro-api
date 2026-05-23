package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/cespare/xxhash/v2"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ccVersionInBillingRe matches the semver part of cc_version (X.Y.Z), preserving
// the trailing message-derived suffix (e.g. ".c02") if present.
var ccVersionInBillingRe = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+`)

// cchValueRe matches the 5-hex cch value in billing header text,
// scoped to x-anthropic-billing-header to avoid touching user content.
var cchValueRe = regexp.MustCompile(`\bcch=([0-9a-f]{5});`)

const cchSeed uint64 = 0x6E52736AC806831E

// syncBillingHeaderVersion rewrites cc_version in x-anthropic-billing-header
// system text blocks to match the version extracted from userAgent.
// Only touches system array blocks whose text starts with "x-anthropic-billing-header".
func syncBillingHeaderVersion(body []byte, userAgent string) []byte {
	version := ExtractCLIVersion(userAgent)
	if version == "" {
		return body
	}

	systemResult := gjson.GetBytes(body, "system")
	if !systemResult.Exists() || !systemResult.IsArray() {
		return body
	}

	replacement := "cc_version=" + version
	idx := 0
	systemResult.ForEach(func(_, item gjson.Result) bool {
		text := item.Get("text")
		if text.Exists() && text.Type == gjson.String &&
			strings.HasPrefix(text.String(), "x-anthropic-billing-header") {
			newText := ccVersionInBillingRe.ReplaceAllString(text.String(), replacement)
			if newText != text.String() {
				if updated, err := sjson.SetBytes(body, fmt.Sprintf("system.%d.text", idx), newText); err == nil {
					body = updated
				}
			}
		}
		idx++
		return true
	})

	return body
}

// signBillingHeaderCCH computes the xxHash64-based CCH signature for the request
// body and replaces the billing header cch value with the computed 5-hex hash.
func signBillingHeaderCCH(body []byte) []byte {
	billingHeader := gjson.GetBytes(body, "system.0.text").String()
	if !strings.HasPrefix(billingHeader, "x-anthropic-billing-header:") {
		return body
	}
	if !cchValueRe.MatchString(billingHeader) {
		return body
	}

	unsignedBillingHeader := cchValueRe.ReplaceAllString(billingHeader, "cch=00000;")
	unsignedBody, err := sjson.SetBytes(body, "system.0.text", unsignedBillingHeader)
	if err != nil {
		return body
	}

	cch := fmt.Sprintf("%05x", xxHash64Seeded(unsignedBody, cchSeed)&0xFFFFF)
	signedBillingHeader := cchValueRe.ReplaceAllString(unsignedBillingHeader, "cch="+cch+";")
	signedBody, err := sjson.SetBytes(unsignedBody, "system.0.text", signedBillingHeader)
	if err != nil {
		return unsignedBody
	}
	return signedBody
}

const claudeBillingFingerprintSalt = "59cf53e54c78"

func claudeBillingVersionFromUA(userAgent string) string {
	if version := ExtractCLIVersion(userAgent); version != "" {
		return version
	}
	return "2.1.63"
}

func computeClaudeBillingFingerprint(messageText, version string) string {
	indices := [3]int{4, 7, 20}
	runes := []rune(messageText)
	var sb strings.Builder
	for _, idx := range indices {
		if idx < len(runes) {
			sb.WriteRune(runes[idx])
		} else {
			sb.WriteRune('0')
		}
	}
	input := claudeBillingFingerprintSalt + sb.String() + version
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:3]
}

func generateClaudeBillingHeader(version, messageText, entrypoint, workload string) string {
	if strings.TrimSpace(version) == "" {
		version = "2.1.63"
	}
	if strings.TrimSpace(entrypoint) == "" {
		entrypoint = "cli"
	}
	buildHash := computeClaudeBillingFingerprint(messageText, version)
	workloadPart := ""
	if strings.TrimSpace(workload) != "" {
		workloadPart = fmt.Sprintf(" cc_workload=%s;", strings.TrimSpace(workload))
	}
	return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s; cch=00000;%s", version, buildHash, entrypoint, workloadPart)
}

// xxHash64Seeded computes xxHash64 of data with a custom seed.
func xxHash64Seeded(data []byte, seed uint64) uint64 {
	d := xxhash.NewWithSeed(seed)
	_, _ = d.Write(data)
	return d.Sum64()
}
