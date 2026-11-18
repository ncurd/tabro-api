package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

// Cache writes are a subset of total input tokens, with independently priced
// 30-minute and 12-hour components. Keep the aggregate for logs and old suppliers.
func applyOpenAICacheWriteDetails(usage *OpenAIUsage, details *apicompat.CacheWriteTokensDetails) {
	usage.CacheCreation30mTokens, usage.CacheCreation12hTokens = 0, 0
	if details != nil {
		usage.CacheCreation30mTokens = details.TTL30mTokens
		usage.CacheCreation12hTokens = details.TTL12hTokens
	}
	if total := usage.CacheCreation30mTokens + usage.CacheCreation12hTokens; usage.CacheCreationInputTokens == 0 && total > 0 {
		usage.CacheCreationInputTokens = total
	}
}

func applyOpenAICacheWriteJSON(usage *OpenAIUsage, inputDetails gjson.Result) {
	usage.CacheCreationInputTokens = int(inputDetails.Get("cache_write_tokens").Int())
	details := inputDetails.Get("cache_write_tokens_details")
	applyOpenAICacheWriteDetails(usage, &apicompat.CacheWriteTokensDetails{
		TTL30mTokens: int(details.Get("ttl_30m_tokens").Int()),
		TTL12hTokens: int(details.Get("ttl_12h_tokens").Int()),
	})
}
