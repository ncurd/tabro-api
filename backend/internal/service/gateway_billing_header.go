package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/cespare/xxhash/v2"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var ccVersionInBillingRe = regexp.MustCompile(`cc_version=[^;\s]+`)

// cchValueRe matches the 5-hex cch value in billing header text,
// scoped to x-anthropic-billing-header to avoid touching user content.
var cchValueRe = regexp.MustCompile(`\bcch=([0-9a-f]{5});`)

const cchSeed uint64 = 0x4D659218E32A3268

// syncBillingHeaderVersion updates proxy-managed billing blocks before signing.
// The fingerprint depends on both the CLI version and the first user text.
// Native caller-owned billing blocks must not enter this rewrite path.
func syncBillingHeaderVersion(body []byte, userAgent string) []byte {
	version := ExtractCLIVersion(userAgent)
	if version == "" {
		return body
	}

	systemResult := gjson.GetBytes(body, "system")
	if !systemResult.Exists() || !systemResult.IsArray() {
		return body
	}

	replacement := "cc_version=" + version + "." + computeClaudeBillingFingerprint(claudeBillingFingerprintMessageText(body), version)
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

// signBillingHeaderCCH signs the final proxy-managed request body. Callers decide
// whether the request is proxy-managed; this function does not detect native
// clients. It changes only the existing five billing CCH digits.
func signBillingHeaderCCH(body []byte) []byte {
	billing := gjson.GetBytes(body, "system.0.text")
	if billing.Type != gjson.String || !strings.HasPrefix(billing.String(), "x-anthropic-billing-header:") {
		return body
	}
	match := cchValueRe.FindStringSubmatchIndex(billing.Raw)
	if len(match) != 4 {
		return body
	}

	offset := billing.Index + match[2]
	unsignedBody := bytes.Clone(body)
	copy(unsignedBody[offset:offset+5], "00000")
	hashBody, err := normalizeClaudeCCHInput(unsignedBody)
	if err != nil {
		return body
	}

	cch := fmt.Sprintf("%05x", xxHash64Seeded(hashBody, cchSeed)&0xFFFFF)
	copy(unsignedBody[offset:offset+5], cch)
	return unsignedBody
}

const claudeBillingFingerprintSalt = "59cf53e54c78"

func claudeBillingVersionFromUA(userAgent string) string {
	if version := ExtractCLIVersion(userAgent); version != "" {
		return version
	}
	return claude.CLICurrentVersion
}

func computeClaudeBillingFingerprint(messageText, version string) string {
	indices := [3]int{4, 7, 20}
	units := utf16.Encode([]rune(messageText))
	var sampled [3]uint16
	for i, idx := range indices {
		sampled[i] = '0'
		if idx < len(units) {
			sampled[i] = units[idx]
		}
	}
	input := claudeBillingFingerprintSalt + string(utf16.Decode(sampled[:])) + version
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:3]
}

func generateClaudeBillingHeader(version, messageText, entrypoint, workload string) string {
	if strings.TrimSpace(version) == "" {
		version = claudeBillingVersionFromUA("")
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

// claudeBillingFingerprintMessageText matches the first-user sampling in
// CLIProxyAPI's claude_executor_cloaking.go at commit 681ce41b6e44c45b15b79378b003909d6ba29e68.
func claudeBillingFingerprintMessageText(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	var text string
	messages.ForEach(func(_, message gjson.Result) bool {
		if message.Get("role").String() != "user" {
			return true
		}
		content := message.Get("content")
		if content.Type == gjson.String {
			text = content.String()
		} else if content.IsArray() {
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() != "text" {
					return true
				}
				value := block.Get("text").String()
				if (strings.HasPrefix(value, "<system-reminder>") && strings.Contains(value, "</system-reminder>")) ||
					strings.HasPrefix(value, "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is ") {
					return true
				}
				text = value
				return false
			})
		}
		return false
	})
	return text
}

// xxHash64Seeded computes xxHash64 of data with a custom seed.
func xxHash64Seeded(data []byte, seed uint64) uint64 {
	d := xxhash.NewWithSeed(seed)
	_, _ = d.Write(data)
	return d.Sum64()
}
