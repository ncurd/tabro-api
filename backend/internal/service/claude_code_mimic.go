package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (s *GatewayService) resolveAnthropicOAuthTLSProfile(account *Account) *tlsfingerprint.Profile {
	if account == nil {
		return nil
	}
	if !account.IsAnthropicOAuthOrSetupToken() {
		if s != nil && s.tlsFPProfileService != nil {
			return s.tlsFPProfileService.ResolveTLSProfile(account)
		}
		return nil
	}
	if account.IsCustomBaseURLEnabled() && strings.TrimSpace(account.GetCustomBaseURL()) != "" {
		if s != nil && s.tlsFPProfileService != nil {
			return s.tlsFPProfileService.ResolveTLSProfile(account)
		}
		return nil
	}
	if s != nil && s.tlsFPProfileService != nil {
		if profile := s.tlsFPProfileService.ResolveTLSProfile(account); profile != nil {
			return profile
		}
	}
	return &tlsfingerprint.Profile{Name: "Built-in Default (Node.js 24.x)"}
}

func rewriteBodyWithCLIProxySystemBlocks(body []byte) []byte {
	messageText := claudeBillingFingerprintMessageText(body)
	version := claudeBillingVersionFromUA(claude.DefaultHeaders["User-Agent"])
	billingText := generateClaudeBillingHeader(version, messageText, "cli", "")

	billingBlock, err1 := marshalAnthropicSystemTextBlock(billingText, false)
	agentBlock, err2 := marshalAnthropicSystemTextBlock(claudeCodeSystemPrompt, false)
	if err1 != nil || err2 != nil {
		return body
	}
	if out, ok := setJSONRawBytes(body, "system", buildJSONArrayRaw([][]byte{billingBlock, agentBlock})); ok {
		return out
	}
	return body
}

func systemTextForForwarding(system any) string {
	system = normalizeSystemParam(system)
	switch v := system.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			text, ok := m["text"].(string)
			if ok && strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
		return strings.Join(parts, "\n\n")
	default:
		return ""
	}
}

func sanitizeForwardedSystemPrompt(text string) string {
	return strings.TrimSpace(text)
}

func prependSystemReminderToFirstUserMessage(body []byte, text string) []byte {
	text = strings.TrimSpace(text)
	if text == "" {
		return body
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}

	firstUserIdx := -1
	messages.ForEach(func(idx, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			firstUserIdx = int(idx.Int())
			return false
		}
		return true
	})
	if firstUserIdx < 0 {
		return body
	}

	prefixBlock := fmt.Sprintf(`<system-reminder>
As you answer the user's questions, you can use the following context from the system:
%s

IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.
</system-reminder>
`, text)

	contentPath := fmt.Sprintf("messages.%d.content", firstUserIdx)
	content := gjson.GetBytes(body, contentPath)
	if content.IsArray() {
		newBlock := fmt.Sprintf(`{"type":"text","text":%q}`, prefixBlock)
		if content.Raw == "[]" || content.Raw == "" {
			body, _ = sjson.SetRawBytes(body, contentPath, []byte("["+newBlock+"]"))
			return body
		}
		body, _ = sjson.SetRawBytes(body, contentPath, []byte("["+newBlock+","+content.Raw[1:]))
		return body
	}
	if content.Type == gjson.String {
		body, _ = sjson.SetBytes(body, contentPath, prefixBlock+content.String())
		return body
	}
	body, _ = sjson.SetRawBytes(body, contentPath, []byte("["+fmt.Sprintf(`{"type":"text","text":%q}`, prefixBlock)+"]"))
	return body
}

func applyClaudeOAuthTransportHeaders(req *http.Request, isStream bool) {
	if req == nil {
		return
	}
	setHeaderRaw(req.Header, "Connection", "keep-alive")
	if req.URL != nil && strings.EqualFold(req.URL.Hostname(), "api.anthropic.com") {
		// Claude's JS SDK negotiates streaming in body.stream, while its HTTP
		// Accept and compression headers remain the same on both paths.
		setHeaderRaw(req.Header, "Accept", "application/json")
		setHeaderRaw(req.Header, "Accept-Encoding", "gzip, deflate, br, zstd")
		setHeaderRaw(req.Header, "Anthropic-Dangerous-Direct-Browser-Access", "true")
	} else if isStream {
		setHeaderRaw(req.Header, "Accept", "text/event-stream")
		setHeaderRaw(req.Header, "Accept-Encoding", "identity")
	} else {
		setHeaderRaw(req.Header, "Accept", "application/json")
		setHeaderRaw(req.Header, "Accept-Encoding", "gzip, deflate, br, zstd")
	}
	if req.URL != nil &&
		strings.EqualFold(req.URL.Scheme, "https") &&
		strings.EqualFold(req.URL.Host, "api.anthropic.com") &&
		getHeaderRaw(req.Header, "x-client-request-id") == "" {
		setHeaderRaw(req.Header, "x-client-request-id", uuid.NewString())
	}
}

func syncClaudeCodeSessionIDHeader(req *http.Request, body []byte, token string) {
	if req == nil {
		return
	}
	if uid := gjson.GetBytes(body, "metadata.user_id").String(); uid != "" {
		if parsed := ParseMetadataUserID(uid); parsed != nil && parsed.SessionID != "" {
			setHeaderRaw(req.Header, "X-Claude-Code-Session-Id", parsed.SessionID)
			return
		}
	}
	if strings.TrimSpace(token) != "" {
		setHeaderRaw(req.Header, "X-Claude-Code-Session-Id", generateSessionUUID("claude-code-session::"+strings.TrimSpace(token)))
	}
}
