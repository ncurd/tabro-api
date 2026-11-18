//go:build unit

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestQwen4PreviewOpenAICompatibilityAdmissionAndSettlement(t *testing.T) {
	for _, model := range []string{"qwen4-max-preview", "qwen4.0-max-preview"} {
		for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
			t.Run(model+path, func(t *testing.T) {
				coordinator, store, _, principal := gatewayBillingFixture()
				pricer, key := noChannelCreditPricer()
				policies, _ := newAdminPolicyFixture()
				enabled := true
				// A configured gateway price is used, never a guessed USD price
				// for this subscription-only provider model.
				_, err := policies.Update(context.Background(), AdminModelPolicyUpdate{Model: model, Enabled: &enabled,
					Pricing: &AdminModelPrice{BillingMode: BillingModeToken, PriceUnit: "million_tokens", InputPricePerMillion: testPtrFloat64(10), OutputPricePerMillion: testPtrFloat64(20)}})
				require.NoError(t, err)
				pricer.gateway.resolver.SetAdminModelPolicies(policies)
				coordinator.creditPricer = pricer
				body := []byte(`{"model":"` + model + `","max_output_tokens":64,"input":"hello"}`)
				if strings.HasSuffix(path, "/chat/completions") {
					body = []byte(`{"model":"` + model + `","max_tokens":64,"max_completion_tokens":null,"n":null,"background":null,"messages":[{"role":"user","content":"hello"}]}`)
				}
				bounded, err := coordinator.BoundRequest(path, body)
				require.NoError(t, err)
				execution, err := coordinator.PrepareForKey(context.Background(), *store.route, principal, "proof", path, "preview-compat", bounded, key)
				require.NoError(t, err)
				require.Equal(t, bc.Decimal("1000000"), execution.OriginalMaximumUsage["input_tokens"])
				require.Equal(t, bc.Decimal("64"), execution.OriginalMaximumUsage["output_tokens"])
				ctx := bc.WithExecution(context.Background(), execution)
				upstreamBody := bounded
				if strings.HasSuffix(path, "/chat/completions") {
					var chat apicompat.ChatCompletionsRequest
					require.NoError(t, json.Unmarshal(bounded, &chat))
					response, err := apicompat.ChatCompletionsToResponses(&chat)
					require.NoError(t, err)
					require.Equal(t, 64, *response.MaxOutputTokens, "conversion must not exceed the reserved user limit")
					upstreamBody, err = json.Marshal(response)
					require.NoError(t, err)
				}
				gateway := &OpenAIGatewayService{billingService: pricer.gateway.billingService}
				require.NoError(t, gateway.validateCentralOutputBound(ctx, &Account{Type: AccountTypeAPIKey}, upstreamBody))
				require.NoError(t, bc.BeforeSupplierRequest(ctx))
				usage := ClaudeUsage{InputTokens: 100, OutputTokens: 20}
				cost, _, _, err := pricer.gateway.frozenGatewayCreditCost(ctx, execution, &ForwardResult{Model: model, Usage: usage}, &recordUsageOpts{})
				require.NoError(t, err)
				require.InDelta(t, (100*10e-6+20*20e-6)*key.Group.RateMultiplier, cost.ActualCost, 1e-12)
				command := &UsageBillingCommand{RequestID: "preview-compat", InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, ActualCost: cost.ActualCost}
				require.NoError(t, attachCentralUsage(ctx, command))
				require.NotNil(t, command.CentralEvent)
			})
		}
	}
}

func TestQwen4PreviewUsesItsOwnDocumentedCaps(t *testing.T) {
	for _, model := range []string{"qwen4-max-preview", "qwen4.0-max-preview"} {
		caps, known := (*PricingService)(nil).GetTextModelCapabilities(model)
		require.True(t, known)
		require.EqualValues(t, 1000000, caps.ContextWindow)
		require.EqualValues(t, 1000000, caps.MaxInputTokens, "official QwenPaw configuration supplies the execution input budget")
		require.EqualValues(t, 393216, caps.MaxOutputTokens, "preview capacity differs from the released Qwen4 model")
		_, reserved, err := boundTextRequest("/v1/chat/completions", []byte(`{"model":"`+model+`","messages":[]}`), nil)
		require.NoError(t, err)
		require.EqualValues(t, 393216, reserved)
	}
}

func TestQwen4MaxDefaultPricingOpenAIAdmissionAndSettlement(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, actualUsage := range []bool{false, true} {
			name := path + "/reserved"
			if actualUsage {
				name = path + "/actual_usage"
			}
			t.Run(name, func(t *testing.T) {
				coordinator, store, authority, principal := gatewayBillingFixture()
				pricer, key := noChannelCreditPricer()
				// Production always has a PricingService, even if the remote
				// catalogue lacks this newly released model. Configure neither
				// manual nor channel prices: use the shipped official USD price.
				pricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}
				pricer.gateway.billingService = NewBillingService(nil, pricing)
				pricer.gateway.resolver = NewModelPricingResolver(pricer.gateway.channelService, pricer.gateway.billingService)
				coordinator.pricing, coordinator.creditPricer = pricing, pricer
				expectedRate := key.Group.RateMultiplier
				if actualUsage {
					coordinator.settings, _ = gatewayOIDCBillingSettings()
					expectedRate *= 2.5
				}
				body := []byte(`{"model":"qwen4-max","max_output_tokens":64,"input":"hello"}`)
				if strings.HasSuffix(path, "/chat/completions") {
					body = []byte(`{"model":"qwen4-max","max_tokens":64,"max_completion_tokens":null,"n":null,"background":null,"messages":[{"role":"user","content":"hello"}]}`)
				}
				bounded, err := coordinator.BoundRequest(path, body)
				require.NoError(t, err)
				execution, err := coordinator.PrepareForKey(context.Background(), *store.route, principal, "proof", path, "qwen4-default", bounded, key)
				require.NoError(t, err)
				require.Equal(t, 1, authority.quoteCalls)
				require.Equal(t, 1, authority.reserves)
				require.Equal(t, gatewayCreditProductKey, authority.quoted.ProductKey)
				if actualUsage {
					require.Equal(t, bc.ChargeModeActualUsage, execution.Quote.Request.ChargeMode)
					require.Equal(t, bc.Decimal("0"), authority.quoted.MaximumUsage[gatewayCreditMeter])
				} else {
					require.NotEqual(t, bc.Decimal("0"), authority.quoted.MaximumUsage[gatewayCreditMeter])
				}
				require.Equal(t, bc.Decimal("991808"), execution.OriginalMaximumUsage["input_tokens"])
				require.Equal(t, bc.Decimal("64"), execution.OriginalMaximumUsage["output_tokens"])
				var frozen gatewayCreditPriceSnapshot
				require.NoError(t, json.Unmarshal(execution.GatewayPricingSnapshot, &frozen))
				require.NotEqual(t, PricingSourceManual, frozen.Resolved.Source)
				require.NotEqual(t, PricingSourceChannel, frozen.Resolved.Source)
				require.InDelta(t, 1.5e-6, frozen.Resolved.BasePricing.InputPricePerToken, 1e-15)
				require.InDelta(t, 4.5e-6, frozen.Resolved.BasePricing.OutputPricePerToken, 1e-15)
				ctx := bc.WithExecution(context.Background(), execution)
				require.NoError(t, bc.BeforeSupplierRequest(ctx))
				usage := ClaudeUsage{InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 40,
					CacheCreationInputTokens: 40, CacheCreation5mTokens: 30, CacheCreation1hTokens: 10}
				cost, model, rate, err := pricer.gateway.frozenGatewayCreditCost(ctx, execution, &ForwardResult{Model: "qwen4-max", Usage: usage}, &recordUsageOpts{})
				require.NoError(t, err)
				require.Equal(t, "qwen4-max", model)
				require.Equal(t, expectedRate, rate)
				expectedTotal := 100*1.5e-6 + 20*4.5e-6 + 40*.075e-6 + 30*1.875e-6 + 10*3e-6
				require.InDelta(t, expectedTotal, cost.TotalCost, 1e-12)
				require.InDelta(t, expectedTotal*expectedRate, cost.ActualCost, 1e-12)
				command := &UsageBillingCommand{RequestID: "qwen4-default", InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
					CacheReadTokens: usage.CacheReadInputTokens, CacheCreationTokens: usage.CacheCreationInputTokens,
					CacheCreation5mTokens: usage.CacheCreation5mTokens, CacheCreation1hTokens: usage.CacheCreation1hTokens, ActualCost: cost.ActualCost}
				require.NoError(t, attachCentralUsage(ctx, command))
				var settlement bc.SettleRequest
				require.NoError(t, json.Unmarshal(command.CentralEvent.Body, &settlement))
				credits, err := gatewayCreditDecimal(cost.ActualCost, false)
				require.NoError(t, err)
				require.Equal(t, map[string]bc.Decimal{gatewayCreditMeter: credits}, settlement.Usage)
				require.True(t, settlement.UsageComplete)
			})
		}
	}
}

func TestOpenAICompatibilityLocalAdmissionFailuresAreDiagnosable(t *testing.T) {
	for _, test := range []struct {
		body string
		code BillingRequestErrorCode
	}{
		{`{"model":"qwen4-max-preview-unknown","messages":[]}`, BillingModelCapabilitiesUnavailable},
		{`{"model":"qwen4-max-preview","max_tokens":0}`, BillingRequestInvalid},
		{`{"model":"qwen4-max-preview","max_tokens":64,"max_completion_tokens":65}`, BillingRequestInvalid},
	} {
		_, err := (&GatewayBillingCoordinator{}).BoundRequest("/v1/chat/completions", []byte(test.body))
		var local *BillingRequestError
		require.ErrorAs(t, err, &local)
		require.Equal(t, test.code, local.Code)
	}
	coordinator, store, authority, principal := gatewayBillingFixture()
	pricer, key := noChannelCreditPricer()
	coordinator.creditPricer = pricer
	body, err := coordinator.BoundRequest("/v1/chat/completions", []byte(`{"model":"qwen4-max-preview","max_tokens":64,"messages":[]}`))
	require.NoError(t, err)
	_, err = coordinator.PrepareForKey(context.Background(), *store.route, principal, "proof", "/v1/chat/completions", "unpriced-preview", body, key)
	var local *BillingRequestError
	require.ErrorAs(t, err, &local)
	require.Equal(t, BillingModelPricingNotConfigured, local.Code)
	require.ErrorIs(t, err, bc.ErrState, "the underlying failed admission remains recognizable")
	require.Zero(t, authority.quoteCalls, "missing local prices fail before remote billing authorization")
}
