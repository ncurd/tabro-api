// Package aliyun contains the documented Model Studio text model inventory.
// Capability data is independent of pricing so absent prices never become free.
package aliyun

import (
	_ "embed"
	"encoding/json"
	"net/url"
	"strings"
)

//go:embed text_models.json
var catalogJSON []byte

type TextModel struct {
	ID                         string `json:"id"`
	ContextWindow              int64  `json:"context_window"`
	MaxInputTokens             int64  `json:"max_input_tokens"`
	MaxOutputTokens            int64  `json:"max_output_tokens"`
	NonThinkingMaxOutputTokens int64  `json:"non_thinking_max_output_tokens,omitempty"`
	ThinkingMaxOutputTokens    int64  `json:"thinking_max_output_tokens,omitempty"`
	Source                     string `json:"source"`
	Notes                      string `json:"notes,omitempty"`
	DefaultSuggestion          bool   `json:"default_suggestion"`
}

var textModels = func() []TextModel {
	var models []TextModel
	if err := json.Unmarshal(catalogJSON, &models); err != nil {
		panic("invalid bundled Aliyun model catalogue: " + err.Error())
	}
	return models
}()

// LookupTextModel accepts exact documented IDs and explicit Model Studio
// namespaces. Dates, suffixes and arbitrary user aliases are never inferred.
func LookupTextModel(model string) (TextModel, bool) {
	id := strings.ToLower(strings.TrimSpace(model))
	id = strings.TrimPrefix(id, "models/")
	for _, prefix := range []string{"aliyun/", "dashscope/"} {
		if strings.HasPrefix(id, prefix) {
			id = strings.TrimPrefix(id, prefix)
			break
		}
	}
	for _, entry := range textModels {
		if strings.ToLower(entry.ID) == id {
			return entry, true
		}
	}
	return TextModel{}, false
}

func DefaultTextModelIDs() []string {
	ids := make([]string, 0, len(textModels))
	for _, model := range textModels {
		if model.DefaultSuggestion {
			ids = append(ids, model.ID)
		}
	}
	return ids
}

// IsCompatibleBaseURL recognizes official DashScope endpoints by hostname,
// rather than matching a provider name embedded in an unrelated URL.
func IsCompatibleBaseURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, domain := range []string{"dashscope.aliyuncs.com", "dashscope-intl.aliyuncs.com", "dashscope-us.aliyuncs.com", "dashscope.aliyuncs.cn", "dashscope-intl.aliyuncs.cn", "dashscope-us.aliyuncs.cn", "maas.aliyuncs.com", "maas.aliyuncs.cn"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
