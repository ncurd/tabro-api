//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"os"
	"sort"
	"strconv"
	"testing"
)

// TestExportBillingCenterGolden exports the Go engine's actual results for the
// Auth decimal-engine regression test. It writes only to an explicit opt-in path.
func TestExportBillingCenterGolden(t *testing.T) {
	dest := os.Getenv("TABRO_BILLING_GOLDEN_OUTPUT")
	if dest == "" {
		t.Skip("set TABRO_BILLING_GOLDEN_OUTPUT to regenerate cross-language fixtures")
	}
	raw, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	data := map[string]*LiteLLMModelPricing{}
	require.NoError(t, json.Unmarshal(raw, &data))
	svc := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	dec := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	snapshot := func(p *ModelPricing) map[string]any {
		return map[string]any{
			"input": dec(p.InputPricePerToken), "output": dec(p.OutputPricePerToken), "cache_write": dec(p.CacheCreationPricePerToken), "cache_read": dec(p.CacheReadPricePerToken),
			"input_priority": dec(p.InputPricePerTokenPriority), "output_priority": dec(p.OutputPricePerTokenPriority), "cache_write_priority": dec(p.CacheCreationPricePerTokenPriority), "cache_read_priority": dec(p.CacheReadPricePerTokenPriority),
			"cache_write_5m": dec(p.CacheCreation5mPrice), "cache_write_1h": dec(p.CacheCreation1hPrice), "supports_cache_breakdown": p.SupportsCacheBreakdown, "image_output": dec(p.ImageOutputPricePerToken),
			"long_context_threshold": strconv.Itoa(p.LongContextInputThreshold), "long_context_input_multiplier": dec(p.LongContextInputMultiplier), "long_context_output_multiplier": dec(p.LongContextOutputMultiplier)}
	}
	cases := []map[string]any{}
	add := func(name, model, tier string, u UsageTokens, p *ModelPricing, options map[string]any, cost *CostBreakdown, count int) {
		if options == nil {
			options = map[string]any{"customer_rate_multiplier": "1.75"}
		}
		row := map[string]any{"name": name, "canonical_model": model, "tier": tier, "options": options, "expected_total": fmt.Sprintf("%.10f", cost.TotalCost), "expected_actual": fmt.Sprintf("%.10f", cost.ActualCost), "expected_total_raw": dec(cost.TotalCost), "expected_actual_raw": dec(cost.ActualCost), "usage": map[string]any{
			"input_tokens": strconv.Itoa(u.InputTokens), "output_tokens": strconv.Itoa(u.OutputTokens), "cache_creation_tokens": strconv.Itoa(u.CacheCreationTokens), "cache_read_tokens": strconv.Itoa(u.CacheReadTokens), "cache_creation5m_tokens": strconv.Itoa(u.CacheCreation5mTokens), "cache_creation1h_tokens": strconv.Itoa(u.CacheCreation1hTokens), "image_output_tokens": strconv.Itoa(u.ImageOutputTokens), "request_count": strconv.Itoa(count)}}
		if p != nil {
			row["snapshot"] = snapshot(p)
		}
		cases = append(cases, row)
	}
	models := make([]string, 0, len(data))
	for model := range data {
		models = append(models, model)
	}
	sort.Strings(models)
	for _, model := range models {
		p, err := svc.GetModelPricing(model)
		require.NoError(t, err)
		u := UsageTokens{InputTokens: 1234, OutputTokens: 567, CacheCreationTokens: 432, CacheReadTokens: 765, ImageOutputTokens: 17}
		if p.SupportsCacheBreakdown {
			u.CacheCreation5mTokens = 321
			u.CacheCreation1hTokens = 111
		}
		cost, err := svc.CalculateCostWithServiceTier(model, u, 1.75, "default")
		require.NoError(t, err)
		add("catalog:"+model, model, "default", u, nil, nil, cost, 1)
		if p.LongContextInputThreshold > 0 {
			for _, input := range []int{p.LongContextInputThreshold, p.LongContextInputThreshold + 1} {
				for _, tier := range []string{"default", "priority", "flex"} {
					long := UsageTokens{InputTokens: input - 3000, CacheCreationTokens: 1000, CacheReadTokens: 2000, OutputTokens: 500}
					longCost, err := svc.CalculateCostWithServiceTier(model, long, 1.75, tier)
					require.NoError(t, err)
					add(fmt.Sprintf("catalog-context:%s:%s:%d", model, tier, input), model, tier, long, nil, nil, longCost, 1)
				}
			}
		}
	}
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna", "claude-opus-5-5"} {
		for _, tier := range []string{"default", "priority", "fast", "flex", "anthropic_fast"} {
			for _, context := range []int{272000, 272001} {
				u := UsageTokens{InputTokens: context - 7000, OutputTokens: 1700, CacheCreationTokens: 2000, CacheReadTokens: 5000}
				if model == "claude-opus-5-5" {
					u.CacheCreation5mTokens = 500
					u.CacheCreation1hTokens = 1500
				}
				cost, err := svc.CalculateCostWithServiceTier(model, u, 1.75, tier)
				require.NoError(t, err)
				add(fmt.Sprintf("new:%s:%s:%d", model, tier, context), model, tier, u, nil, nil, cost, 1)
			}
		}
	}
	custom := &ModelPricing{InputPricePerToken: 2e-6, OutputPricePerToken: 10e-6, InputPricePerTokenPriority: 3e-6, CacheCreationPricePerToken: 2.5e-6, CacheReadPricePerToken: .2e-6, ImageOutputPricePerToken: 40e-6}
	u := UsageTokens{InputTokens: 1000, OutputTokens: 700, CacheCreationTokens: 400, CacheReadTokens: 800, ImageOutputTokens: 200}
	for _, tier := range []string{"priority", "fast", "flex"} {
		add("partial-priority:"+tier, "custom", tier, u, custom, nil, svc.computeTokenBreakdown(custom, u, 1.75, tier, true), 1)
	}
	resolver := NewModelPricingResolver(nil, svc)
	upper := 1000
	in1, in2, out1, out2 := .00001, .000001, .00004, .000004
	intervals := []PricingInterval{{MinTokens: 0, MaxTokens: &upper, InputPrice: &in1, OutputPrice: &out1}, {MinTokens: 1000, InputPrice: &in2, OutputPrice: &out2}}
	options := map[string]any{"customer_rate_multiplier": "1.75", "intervals": []map[string]any{{"min_tokens": "0", "max_tokens": "1000", "input": dec(in1), "output": dec(out1)}, {"min_tokens": "1000", "input": dec(in2), "output": dec(out2)}}}
	for _, context := range []int{0, 1000, 1001} {
		u := UsageTokens{InputTokens: context, OutputTokens: 100}
		cost, err := svc.CalculateCostUnified(CostInput{Model: "custom", Tokens: u, RateMultiplier: 1.75, ServiceTier: "priority", Resolver: resolver, Resolved: &ResolvedPricing{Mode: BillingModeToken, BasePricing: custom, Intervals: intervals}})
		require.NoError(t, err)
		add(fmt.Sprintf("interval:%d", context), "custom", "priority", u, custom, options, cost, 1)
	}
	req, tierPrice := .8, 1.2
	requestTiers := []PricingInterval{{MinTokens: 0, MaxTokens: &upper, PerRequestPrice: &tierPrice, TierLabel: "HD"}}
	for _, size := range []string{"HD", "unknown"} {
		u := UsageTokens{InputTokens: 2000}
		cost, err := svc.CalculateCostUnified(CostInput{Model: "custom", Tokens: u, RateMultiplier: 1.75, RequestCount: 3, SizeTier: size, Resolver: resolver, Resolved: &ResolvedPricing{Mode: BillingModeImage, RequestTiers: requestTiers, DefaultPerRequestPrice: req}})
		require.NoError(t, err)
		add("request-tier:"+size, "custom", "default", u, custom, map[string]any{"billing_mode": "image", "customer_rate_multiplier": "1.75", "default_per_request_price": "0.8", "size_tier": size, "intervals": []map[string]any{{"min_tokens": "0", "max_tokens": "1000", "per_request_price": "1.2", "tier_label": "HD"}}}, cost, 3)
	}
	marginal := UsageTokens{InputTokens: 10000, OutputTokens: 700, CacheReadTokens: 210000, CacheCreationTokens: 2000, CacheCreation5mTokens: 500, CacheCreation1hTokens: 1500}
	cost, err := svc.CalculateCostWithLongContext("claude-opus-5-5", marginal, 1.75, 200000, 2)
	require.NoError(t, err)
	add("legacy-marginal-context", "claude-opus-5-5", "default", marginal, nil, map[string]any{"customer_rate_multiplier": "1.75", "marginal_context_threshold": "200000", "marginal_context_multiplier": "2"}, cost, 1)
	encoded, err := json.MarshalIndent(map[string]any{"source": "tabro-api BillingService + ModelPricingResolver; TestExportBillingCenterGolden", "cases": cases}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dest, append(encoded, '\n'), 0644))
	t.Logf("exported %d cases", len(cases))
}
