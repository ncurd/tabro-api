package service

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Fixed vectors from router-for-me/CLIProxyAPI commit
// 681ce41b6e44c45b15b79378b003909d6ba29e68, claude_signing_test.go.
const claudeBillingCCHReferenceBody = `{"model":"model-a","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.test; cc_entrypoint=sdk-cli; cch=00000;"},{"type":"text","text":"system-x"}],"tools":[],"metadata":{"user_id":"meta-x"},"max_tokens":1,"thinking":{"type":"adaptive","display":"omitted"},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"output_config":{"effort":"high"},"stream":true}`

func TestSignBillingHeaderCCHLatestKnownVectors(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{"base", claudeBillingCCHReferenceBody, "7ee87"},
		{"model ignored", strings.Replace(claudeBillingCCHReferenceBody, `"model":"model-a"`, `"model":"model-b"`, 1), "7ee87"},
		{"max tokens ignored", strings.Replace(claudeBillingCCHReferenceBody, `"max_tokens":1`, `"max_tokens":2`, 1), "7ee87"},
		{"message signed", strings.Replace(claudeBillingCCHReferenceBody, `"text":"x"`, `"text":"y"`, 1), "b9cc8"},
		{"system signed", strings.Replace(claudeBillingCCHReferenceBody, `"system-x"`, `"system-y"`, 1), "a30d3"},
		{"metadata signed", strings.Replace(claudeBillingCCHReferenceBody, `"user_id":"meta-x"`, `"user_id":"meta-y"`, 1), "7a89d"},
		{"tool signed", strings.Replace(claudeBillingCCHReferenceBody, `"tools":[]`, `"tools":[{"name":"t","description":"d","input_schema":{"type":"object"}}]`, 1), "3d78d"},
		{"nested model ignored", strings.Replace(claudeBillingCCHReferenceBody, `"metadata":{"user_id":"meta-x"}`, `"metadata":{"user_id":"meta-x","model":"a"}`, 1), "0601b"},
		{"nested max tokens excluded", strings.Replace(claudeBillingCCHReferenceBody, `"metadata":{"user_id":"meta-x"}`, `"metadata":{"user_id":"meta-x","max_tokens":2}`, 1), "7ee87"},
		{"fallbacks excluded", strings.Replace(claudeBillingCCHReferenceBody, `"stream":true}`, `"stream":true,"fallbacks":[{"model":"fallback-a"}]}`, 1), "7ee87"},
		{"credit token excluded", strings.Replace(claudeBillingCCHReferenceBody, `"stream":true}`, `"stream":true,"fallback_credit_token":"a"}`, 1), "7ee87"},
		{"native trailing dispatch run", strings.Replace(claudeBillingCCHReferenceBody, `"metadata":{"user_id":"meta-x"}`, `"metadata":{"user_id":"meta-x","max_tokens":999,"fallbacks":[{"model":"fallback-model"}]}`, 1), "4589b"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := gjson.GetBytes(signBillingHeaderCCH([]byte(tt.body)), "system.0.text").String()
			require.Contains(t, got, "cch="+tt.want+";")
		})
	}
}

func TestSignBillingHeaderCCHPreservesSerializedBytes(t *testing.T) {
	body := []byte("{\n  \"system\" : [{\"type\":\"text\",\"text\":\"x-\\u0061nthropic-billing-header: cc_version=2.1.280.test; cc_entrypoint=cli; cch=00000;\"}],\n  \"messages\" : [{\"role\":\"user\",\"content\":\"literal cch=00000; \\u0061\"}], \"max_tokens\":123 }")
	signed := signBillingHeaderCCH(body)
	require.True(t, gjson.ValidBytes(signed))
	require.NotEqual(t, string(body), string(signed))
	unsigned := cchValueRe.ReplaceAll(signed, []byte("cch=00000;"))
	require.Equal(t, string(body), string(unsigned), "signing may change only the five billing CCH digits")
}

func TestClaudeBillingFingerprintLatestUTF16Vectors(t *testing.T) {
	// claude_cloaked_cache_repro_test.go at the same upstream commit.
	for _, tt := range []struct{ text, want string }{
		{"😀abcdefghijklmnopqrstuvwxyz", "dac"},
		{"abc😀abcdefghijklmnop", "695"},
	} {
		require.Equal(t, tt.want, computeClaudeBillingFingerprint(tt.text, "2.1.280"))
	}
}

func TestNormalizeClaudeCCHInputReferenceCases(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{
		{"model emptied", `{"model":"claude","keep":1}`, `{"model":"","keep":1}`},
		{"first excluded", `{"max_tokens":1,"keep":2}`, `{"keep":2}`},
		{"middle excluded", `{"keep":1,"fallbacks":[{"model":"x"}],"tail":2}`, `{"keep":1,"tail":2}`},
		{"last excluded", `{"keep":1,"fallback_credit_token":"secret"}`, `{"keep":1}`},
		{"all excluded", `{"max_tokens":1,"fallbacks":[],"fallback_credit_token":"secret"}`, `{}`},
		{"native trailing comma", `{"keep":1,"max_tokens":1,"fallbacks":[]}`, `{"keep":1,}`},
		{"nested excluded", `{"outer":{"model":"x","max_tokens":1,"keep":"y"}}`, `{"outer":{"model":"","keep":"y"}}`},
		{"string literal untouched", `{"text":"literal \"model\":\"x\" and \"max_tokens\":1"}`, `{"text":"literal \"model\":\"x\" and \"max_tokens\":1"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeClaudeCCHInput([]byte(tt.body))
			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))
		})
	}
	_, err := normalizeClaudeCCHInput([]byte(`{"messages":`))
	require.Error(t, err)
}

func TestClaudeBillingFingerprintMessageText(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{
		{"first user only", `{"messages":[{"role":"assistant","content":"assistant"},{"role":"user","content":"first user"},{"role":"user","content":"later user"}]}`, "first user"},
		{"skip reminder blocks", `{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>context</system-reminder>"},{"type":"image"},{"type":"text","text":"real user text"}]}]}`, "real user text"},
		{"no user text", `{"messages":[{"role":"user","content":[{"type":"image"}]},{"role":"user","content":"later user"}]}`, ""},
		{"no messages", `{"system":"system is not the sampled message"}`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, claudeBillingFingerprintMessageText([]byte(tt.body)))
		})
	}
}

func TestClaudeBillingDefaultVersionMatchesCLIProfile(t *testing.T) {
	require.Equal(t, claude.CLICurrentVersion, claudeBillingVersionFromUA(""))
	require.Equal(t, claude.CLICurrentVersion, claudeBillingVersionFromUA("Python-SDK"))
	require.Contains(t, generateClaudeBillingHeader("", "user text", "cli", ""), "cc_version="+claude.CLICurrentVersion+".")
}

func TestSyncBillingHeaderVersionRecomputesFingerprintFromFirstUser(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.63.aaa; cc_entrypoint=cli; cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>context</system-reminder>"},{"type":"text","text":"abc😀abcdefghijklmnop"}]}]}`)
	updated := syncBillingHeaderVersion(body, "claude-cli/2.1.280 (external, cli)")
	require.Contains(t, gjson.GetBytes(updated, "system.0.text").String(), "cc_version=2.1.280.695;")
	require.Equal(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(updated, "messages").Raw)
}
