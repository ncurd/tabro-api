package service

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const OpsOpenAIImagesRequestSummaryKey = "ops_openai_images_request_summary"

// This diagnostic is a whitelist of routing and tool options from the final
// request. It must never contain prompts, image inputs, headers or proxy URLs.
type openAIImagesOAuthRequestSummary struct {
	Host              string `json:"host"`
	Path              string `json:"path"`
	ParentModel       string `json:"parent_model"`
	ReasoningPresent  bool   `json:"reasoning_present"`
	EffortPresent     bool   `json:"effort_present"`
	Effort            string `json:"effort,omitempty"`
	ImageModel        string `json:"image_model"`
	ComplexityPresent bool   `json:"complexity_present"`
	Complexity        string `json:"complexity,omitempty"`
	Quality           string `json:"quality,omitempty"`
	Action            string `json:"action,omitempty"`
	ProxyConfigured   bool   `json:"proxy_configured"`
}

func recordOpenAIImagesOAuthRequestSummary(c *gin.Context, req *http.Request, account *Account) {
	if c == nil || req == nil || req.URL == nil || req.GetBody == nil {
		return
	}
	body, err := req.GetBody()
	if err != nil {
		return
	}
	defer body.Close()
	encoded, err := io.ReadAll(body)
	if err != nil || !gjson.ValidBytes(encoded) {
		return
	}
	root := gjson.ParseBytes(encoded)
	tool, _ := firstOpenAIImageGenerationTool(encoded)
	safeField := func(value gjson.Result) string {
		if value.Type != gjson.String {
			return ""
		}
		text := []rune(sanitizeOpenAIImageErrorField(value.String()))
		if len(text) > 160 {
			text = text[:160]
		}
		return string(text)
	}
	summary := openAIImagesOAuthRequestSummary{
		Host:              req.URL.Host,
		Path:              req.URL.EscapedPath(),
		ParentModel:       safeField(root.Get("model")),
		ReasoningPresent:  root.Get("reasoning").Exists(),
		EffortPresent:     root.Get("reasoning.effort").Exists(),
		Effort:            safeField(root.Get("reasoning.effort")),
		ImageModel:        safeField(tool.Get("model")),
		ComplexityPresent: tool.Get("complexity").Exists(),
		Complexity:        safeField(tool.Get("complexity")),
		Quality:           safeField(tool.Get("quality")),
		Action:            safeField(tool.Get("action")),
		ProxyConfigured:   account != nil && account.ProxyID != nil && account.Proxy != nil,
	}
	c.Set(OpsOpenAIImagesRequestSummaryKey, summary)
}
