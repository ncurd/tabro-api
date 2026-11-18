package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

// Cache writes are a subset of total input tokens, with independently priced
// TTL components. Keep the aggregate for logs and old suppliers.
func applyOpenAICacheWriteDetails(usage *OpenAIUsage, details *apicompat.CacheWriteTokensDetails) {
	usage.CacheCreation5mTokens, usage.CacheCreation1hTokens = 0, 0
	usage.CacheCreation30mTokens, usage.CacheCreation12hTokens = 0, 0
	if details != nil {
		usage.CacheCreation5mTokens = details.TTL5mTokens
		usage.CacheCreation1hTokens = details.TTL1hTokens
		usage.CacheCreation30mTokens = details.TTL30mTokens
		usage.CacheCreation12hTokens = details.TTL12hTokens
	}
	if total := usage.CacheCreation5mTokens + usage.CacheCreation1hTokens + usage.CacheCreation30mTokens + usage.CacheCreation12hTokens; usage.CacheCreationInputTokens == 0 && total > 0 {
		usage.CacheCreationInputTokens = total
	}
}

func applyOpenAICacheWriteJSON(usage *OpenAIUsage, inputDetails gjson.Result) {
	usage.CacheCreationInputTokens = int(inputDetails.Get("cache_write_tokens").Int())
	details := inputDetails.Get("cache_write_tokens_details")
	applyOpenAICacheWriteDetails(usage, &apicompat.CacheWriteTokensDetails{
		TTL5mTokens:  int(details.Get("ttl_5m_tokens").Int()),
		TTL1hTokens:  int(details.Get("ttl_1h_tokens").Int()),
		TTL30mTokens: int(details.Get("ttl_30m_tokens").Int()),
		TTL12hTokens: int(details.Get("ttl_12h_tokens").Int()),
	})
}
