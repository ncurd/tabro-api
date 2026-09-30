package service

import (
	"bytes"
	"context"
	"mime/multipart"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGatewayBillingGeminiEnforcesCatalogAndDisjointMeterBounds(t *testing.T) {
	pricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{"gemini-3-pro": {MaxInputTokens: 1048576, MaxOutputTokens: 65536}}}
	s := &GatewayBillingCoordinator{pricing: pricing}
	path := "/v1beta/models/gemini-3-pro:streamGenerateContent"
	body, err := s.BoundGeminiRequest(path, []byte(`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGk="}}]}],"generationConfig":{"candidateCount":2,"responseModalities":["TEXT","IMAGE"]}}`))
	require.NoError(t, err)
	require.EqualValues(t, 65536, gjson.GetBytes(body, "generationConfig.maxOutputTokens").Int())
	quote, ok, err := geminiBillingQuoteRequest(path, "op", body, pricing)
	require.True(t, ok)
	require.NoError(t, err)
	require.Equal(t, bc.Decimal("1048576"), quote.MaximumUsage["input_tokens"])
	require.Equal(t, bc.Decimal("131072"), quote.MaximumUsage["image_output_tokens"])
	require.Equal(t, "ai:gemini-3-pro", quote.ProductKey)
	_, err = s.BoundGeminiRequest(path, []byte(`{"generationConfig":{"maxOutputTokens":65537}}`))
	require.Error(t, err)
	_, err = s.BoundGeminiRequest("/v1beta/models/unknown:generateContent", []byte(`{}`))
	require.Error(t, err)
	_, _, err = geminiBillingQuoteRequest(path, "op", []byte(`{"generationConfig":{"candidateCount":1.5}}`), pricing)
	require.Error(t, err)
}

func TestGatewayBillingImageMultipartBindsUploadedBytesAndImageProduct(t *testing.T) {
	makeBody := func(contents string) ([]byte, string) {
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		require.NoError(t, w.WriteField("model", "gpt-image-2"))
		require.NoError(t, w.WriteField("prompt", "edit the image"))
		require.NoError(t, w.WriteField("n", "2"))
		require.NoError(t, w.WriteField("size", "1024x1024"))
		require.NoError(t, w.WriteField("quality", "high"))
		part, err := w.CreateFormFile("image[]", "example.png")
		require.NoError(t, err)
		_, err = part.Write([]byte(contents))
		require.NoError(t, err)
		require.NoError(t, w.Close())
		return body.Bytes(), w.FormDataContentType()
	}
	coordinator, store, authority, principal := gatewayBillingFixture()
	authority.quote.Request.ServiceTier = "1024x1024:high"
	body, contentType := makeBody("first-upload")
	execution, err := coordinator.PrepareImages(context.Background(), *store.route, principal, "proof", "/v1/images/edits", "edit", body, contentType, nil)
	require.NoError(t, err)
	require.Equal(t, "ai:gpt-image-2:images", execution.ProductKey)
	require.Equal(t, bc.Decimal("2"), execution.Quote.Request.MaximumUsage["image_count"])
	require.Len(t, execution.Quote.Request.MaximumUsage, 2)
	require.NotContains(t, string(store.op.RequestPayload), "first-upload")
	require.NotContains(t, string(store.op.RequestPayload), "proof")
	changed, changedType := makeBody("different-upload")
	_, err = coordinator.PrepareImages(context.Background(), *store.route, principal, "proof", "/v1/images/edits", "edit", changed, changedType, nil)
	require.ErrorIs(t, err, bc.ErrConflict)
	parsed, err := ParseOpenAIImageEditRequest(body, contentType)
	require.NoError(t, err)
	ctx := bc.WithExecution(context.Background(), execution)
	require.NoError(t, ValidateBillingImageRequest(ctx, parsed.MetadataBody))
	require.Error(t, ValidateBillingImageRequest(ctx, []byte(`{"model":"gpt-image-2","n":3,"size":"1024x1024","quality":"high"}`)))
	require.NoError(t, bc.BeforeSupplierRequest(ctx))
	cmd := &UsageBillingCommand{RequestID: "images-evidence", ImageCount: 2, InputTokens: 70, OutputTokens: 90, ImageOutputTokens: 90, BalanceCost: 7, AccountQuotaCost: 2}
	require.NoError(t, attachCentralUsage(ctx, cmd))
	require.Len(t, cmd.ExactUsage, 2)
	require.Zero(t, cmd.BalanceCost)
	require.EqualValues(t, 2, cmd.AccountQuotaCost)
}

func TestGatewayBillingGeminiAllowsClientFunctionsAndRejectsUnmeteredTools(t *testing.T) {
	pricing := &PricingService{pricingData: map[string]*LiteLLMModelPricing{"gemini-3-pro": {MaxInputTokens: 1048576, MaxOutputTokens: 65536}}}
	for _, tools := range []string{`[]`, `[{"functionDeclarations":[{"name":"lookup"}]}]`, `[{"function_declarations":[{"name":"lookup"}]}]`} {
		_, _, err := geminiBillingQuoteRequest("/v1beta/models/gemini-3-pro:generateContent", "op", []byte(`{"tools":`+tools+`}`), pricing)
		require.NoError(t, err, tools)
	}
	for _, tools := range []string{`{}`, `[null]`, `[{"googleSearch":{}}]`, `[{"google_search":{}}]`, `[{"codeExecution":{}}]`, `[{"code_execution":{}}]`, `[{"urlContext":{}}]`, `[{"functionDeclarations":[],"googleSearchRetrieval":{}}]`} {
		_, _, err := geminiBillingQuoteRequest("/v1beta/models/gemini-3-pro:generateContent", "op", []byte(`{"tools":`+tools+`}`), pricing)
		require.Error(t, err, tools)
	}
}

func TestGatewayBillingWebSocketMissingReservationOnlyAllowedForShadow(t *testing.T) {
	for _, mode := range []string{"central", ""} {
		session := &GatewayBillingSession{Mode: mode}
		require.ErrorIs(t, session.BeforeSupplierWrite(context.Background(), nil), bc.ErrState)
	}
	require.NoError(t, (&GatewayBillingSession{Mode: "shadow"}).BeforeSupplierWrite(context.Background(), nil))
}
