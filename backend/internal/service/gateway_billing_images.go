package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/tidwall/gjson"
)

func IsBillingImagesPath(path string) bool {
	path = strings.TrimPrefix(path, "/v1")
	return path == "/images/generations" || path == "/images/edits"
}
func billingImageTier(body []byte) string {
	size := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "size").String()))
	if size == "" {
		size = "auto"
	}
	quality := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "quality").String()))
	if quality == "" {
		quality = "auto"
	}
	return size + ":" + quality
}
func imageBillingQuoteRequest(path, id string, metadata, body []byte) (bc.QuoteRequest, error) {
	q := bc.QuoteRequest{OperationID: id, RequestPayloadHash: HashUsageRequestPayload(append([]byte(path+"\x00"), body...))}
	if !IsBillingImagesPath(path) || !gjson.ValidBytes(metadata) {
		return q, bc.ErrConflict
	}
	model := strings.TrimSpace(gjson.GetBytes(metadata, "model").String())
	if model == "" {
		return q, fmt.Errorf("image model is required")
	}
	count := int64(1)
	if v := gjson.GetBytes(metadata, "n"); v.Exists() {
		if json.Unmarshal([]byte(v.Raw), &count) != nil || count < 1 || count > 10 {
			return q, fmt.Errorf("image count must be an integer from 1 to 10")
		}
	}
	q.ProductKey = "ai:" + model + ":images"
	q.ServiceTier = billingImageTier(metadata)
	q.MaximumUsage = map[string]bc.Decimal{"request_count": "1", "image_count": bc.Decimal(strconv.FormatInt(count, 10))}
	return q, nil
}
func (s *GatewayBillingCoordinator) PrepareImages(ctx context.Context, route GatewayBillingRoute, principal *GatewayOIDCPrincipal, proof, path, id string, body []byte, contentType string, binding *GatewayBillingCredential) (*bc.Execution, error) {
	return s.PrepareImagesForKey(ctx, route, principal, proof, path, id, body, contentType, nil, binding)
}

func (s *GatewayBillingCoordinator) PrepareImagesForKey(ctx context.Context, route GatewayBillingRoute, principal *GatewayOIDCPrincipal, proof, path, id string, body []byte, contentType string, key *APIKey, binding *GatewayBillingCredential) (*bc.Execution, error) {
	metadata := body
	if strings.HasSuffix(path, "/edits") {
		parsed, err := ParseOpenAIImageEditRequest(body, contentType)
		if err != nil {
			return nil, err
		}
		metadata = parsed.MetadataBody
	}
	quote, err := imageBillingQuoteRequest(path, id, metadata, body)
	if err != nil {
		return nil, err
	}
	return s.prepareWithQuote(ctx, route, principal, proof, path, id, body, &quote, key, binding)
}

// Image products are explicitly priced per completed image. Token-priced model
// routes cannot accidentally stand in for these products. This check also runs
// after channel/account model mappings and before the provider's first send.
func ValidateBillingImageRequest(ctx context.Context, metadata []byte) error {
	e := bc.ExecutionFromContext(ctx)
	if e == nil || e.Mode != "central" {
		return nil
	}
	quote, err := imageBillingQuoteRequest("/images/generations", e.Key.OperationID, metadata, metadata)
	if err != nil {
		return err
	}
	tier := e.Quote.Request.ServiceTier
	count := e.Quote.Request.MaximumUsage["image_count"]
	if _, generic := e.Quote.Request.MaximumUsage[gatewayCreditMeter]; generic {
		var snap gatewayCreditPriceSnapshot
		if json.Unmarshal(e.GatewayPricingSnapshot, &snap) != nil || snap.ImageUnitPrice == nil && snap.ImageUnitPrices == nil {
			return bc.ErrState
		}
		tier = snap.ServiceTier
		count = e.OriginalMaximumUsage["image_count"]
	}
	if quote.ProductKey != e.ProductKey || quote.ServiceTier != tier || quote.MaximumUsage["image_count"] != count {
		return fmt.Errorf("image request differs from frozen billing quote")
	}
	return nil
}
