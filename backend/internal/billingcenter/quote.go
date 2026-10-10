package billingcenter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

type QuoteRequest struct {
	OperationID        string             `json:"operation_id"`
	ProductKey         string             `json:"product_key"`
	MaximumUsage       map[string]Decimal `json:"maximum_usage"`
	ServiceTier        string             `json:"service_tier"`
	RequestPayloadHash string             `json:"request_payload_hash,omitempty"`
	QuoteMode          string             `json:"quote_mode,omitempty"`
	ChargeMode         string             `json:"charge_mode,omitempty"`
}
type Quote struct {
	QuoteID         string         `json:"quote_id"`
	Request         ReserveRequest `json:"request"`
	EstimatedAmount Decimal        `json:"estimated_amount"`
	WalletUnit      string         `json:"wallet_unit"`
	ProductVersion  string         `json:"product_version"`
}

type Estimate struct {
	Amount         Decimal `json:"amount"`
	WalletUnit     string  `json:"wallet_unit"`
	PriceVersionID string  `json:"price_version_id"`
}

func (c *Client) Estimate(ctx context.Context, key Key, quoteID string, usage map[string]Decimal) (Estimate, error) {
	var result Estimate
	if key.ProducerClientID != c.producer || quoteID == "" {
		return result, ErrConflict
	}
	body, err := json.Marshal(struct {
		Usage map[string]Decimal `json:"usage"`
	}{usage})
	if err != nil {
		return result, err
	}
	data, err := c.requestJSON(ctx, http.MethodPost, "/internal/billing/v1/quotes/"+url.PathEscape(quoteID)+"/estimate", originQuery(key), body)
	if err != nil {
		return result, err
	}
	if json.Unmarshal(data, &result) != nil || result.PriceVersionID == "" || result.WalletUnit == "" {
		return result, ErrConflict
	}
	return result, nil
}

// Quote is read-only: a successful quote does not authorize supplier execution.
func (c *Client) Quote(ctx context.Context, request ProofRequest[QuoteRequest]) (Quote, error) {
	var result Quote
	if err := request.Validate(); err != nil {
		return result, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	data, err := c.requestJSON(ctx, http.MethodPost, "/internal/billing/v1/quotes", nil, body)
	if err != nil {
		return result, err
	}
	if json.Unmarshal(data, &result) != nil || result.QuoteID == "" || result.Request.OperationID != request.Request.OperationID || result.Request.BillingAccountID == "" || result.Request.PriceVersionID == "" || result.Request.BalanceID == "" {
		return result, ErrConflict
	}
	if result.Request.ChargeMode != request.Request.ChargeMode {
		return result, ErrState
	}
	return result, nil
}
