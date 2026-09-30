package billingcenter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// These values are frozen by Auth. The payment adapter reports provider facts;
// it cannot choose a customer, grant amount, conversion rate or funding source.
type PaymentOrder struct {
	OrderID               string  `json:"order_id"`
	AccountID             string  `json:"account_id"`
	ProductName           string  `json:"product_name"`
	Kind                  string  `json:"kind"`
	State                 string  `json:"state"`
	PaymentCurrency       string  `json:"payment_currency"`
	PaymentAmount         Decimal `json:"payment_amount"`
	GrantUnit             string  `json:"grant_unit"`
	GrantAmount           Decimal `json:"grant_amount"`
	PaymentProducerID     string  `json:"payment_producer_id"`
	PaymentMethod         string  `json:"payment_method"`
	CheckoutState         string  `json:"checkout_state"`
	CheckoutClaimEventID  string  `json:"checkout_claim_event_id,omitempty"`
	ProviderInstanceID    string  `json:"provider_instance_id,omitempty"`
	ProviderTransactionID string  `json:"provider_transaction_id,omitempty"`
	PayURL                string  `json:"pay_url,omitempty"`
	QRCode                string  `json:"qr_code,omitempty"`
	SourceSystem          string  `json:"source_system,omitempty"`
	SourceOrderID         string  `json:"source_order_id,omitempty"`
}
type PaymentRefund struct {
	RefundID              string  `json:"refund_id"`
	OrderID               string  `json:"order_id"`
	PaymentCurrency       string  `json:"payment_currency"`
	PaymentAmount         Decimal `json:"payment_amount"`
	GrantAmount           Decimal `json:"grant_amount"`
	State                 string  `json:"state"`
	ProviderTransactionID string  `json:"provider_transaction_id"`
	SourceSystem          string  `json:"source_system,omitempty"`
	SourceOrderID         string  `json:"source_order_id,omitempty"`
}
type PaymentWork struct {
	Orders  []PaymentOrder  `json:"orders"`
	Refunds []PaymentRefund `json:"refunds"`
}
type PaymentClaim struct {
	EventID string `json:"event_id"`
}
type PaymentCancelled struct {
	EventID            string `json:"event_id"`
	ProviderReference  string `json:"provider_reference"`
	ConfirmedNoPayment bool   `json:"confirmed_no_payment"`
}
type PaymentCheckout struct {
	EventID               string `json:"event_id"`
	ProviderInstanceID    string `json:"provider_instance_id"`
	PayURL                string `json:"pay_url"`
	QRCode                string `json:"qr_code"`
	ProviderTransactionID string `json:"provider_transaction_id,omitempty"`
}
type PaymentPaid struct {
	EventID               string  `json:"event_id"`
	ProviderTransactionID string  `json:"provider_transaction_id"`
	PaymentCurrency       string  `json:"payment_currency"`
	PaymentAmount         Decimal `json:"payment_amount"`
}
type PaymentRefundComplete struct {
	EventID          string  `json:"event_id"`
	ProviderRefundID string  `json:"provider_refund_id"`
	PaymentCurrency  string  `json:"payment_currency"`
	PaymentAmount    Decimal `json:"payment_amount"`
	Succeeded        bool    `json:"succeeded"`
}

func paymentJSON[T any](ctx context.Context, c *Client, method, path string, query url.Values, request any) (T, error) {
	var result T
	var body []byte
	var err error
	if request != nil {
		body, err = json.Marshal(request)
		if err != nil {
			return result, err
		}
	}
	data, err := c.requestJSON(ctx, method, path, query, body)
	if err != nil {
		return result, err
	}
	if json.Unmarshal(data, &result) != nil {
		return result, &RemoteError{Retryable: true, UnknownOutcome: method != http.MethodGet}
	}
	return result, nil
}
func (c *Client) PaymentWork(ctx context.Context, limit int) (PaymentWork, error) {
	return paymentJSON[PaymentWork](ctx, c, http.MethodGet, "/internal/billing/v1/payment-work", url.Values{"limit": {strconv.Itoa(limit)}}, nil)
}
func (c *Client) PaymentOrder(ctx context.Context, id string) (PaymentOrder, error) {
	return paymentJSON[PaymentOrder](ctx, c, http.MethodGet, "/internal/billing/v1/payment-orders/"+url.PathEscape(id), nil, nil)
}
func (c *Client) ClaimPaymentCheckout(ctx context.Context, id string, claim PaymentClaim) (PaymentOrder, error) {
	return paymentJSON[PaymentOrder](ctx, c, http.MethodPost, "/internal/billing/v1/payment-orders/"+url.PathEscape(id)+"/checkout/claim", nil, claim)
}
func (c *Client) CompletePaymentCheckout(ctx context.Context, id string, checkout PaymentCheckout) (PaymentOrder, error) {
	return paymentJSON[PaymentOrder](ctx, c, http.MethodPost, "/internal/billing/v1/payment-orders/"+url.PathEscape(id)+"/checkout", nil, checkout)
}
func (c *Client) ConfirmPaymentPaid(ctx context.Context, id string, paid PaymentPaid) (PaymentOrder, error) {
	return paymentJSON[PaymentOrder](ctx, c, http.MethodPost, "/internal/billing/v1/payment-orders/"+url.PathEscape(id)+"/paid", nil, paid)
}
func (c *Client) ConfirmPaymentCancelled(ctx context.Context, id string, cancelled PaymentCancelled) (PaymentOrder, error) {
	return paymentJSON[PaymentOrder](ctx, c, http.MethodPost, "/internal/billing/v1/payment-orders/"+url.PathEscape(id)+"/cancelled", nil, cancelled)
}
func (c *Client) PaymentRefund(ctx context.Context, id string) (PaymentRefund, error) {
	return paymentJSON[PaymentRefund](ctx, c, http.MethodGet, "/internal/billing/v1/payment-refunds/"+url.PathEscape(id), nil, nil)
}
func (c *Client) CompletePaymentRefund(ctx context.Context, id string, completed PaymentRefundComplete) (PaymentRefund, error) {
	return paymentJSON[PaymentRefund](ctx, c, http.MethodPost, "/internal/billing/v1/payment-refunds/"+url.PathEscape(id)+"/complete", nil, completed)
}
