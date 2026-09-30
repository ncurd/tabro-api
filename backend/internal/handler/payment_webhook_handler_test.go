//go:build unit

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestWriteSuccessResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name            string
		providerKey     string
		wantCode        int
		wantContentType string
		wantBody        string
	}{
		{
			name:        "wxpay returns empty 204",
			providerKey: "wxpay",
			wantCode:    http.StatusNoContent,
			wantBody:    "",
		},
		{
			name:            "stripe returns empty 200",
			providerKey:     "stripe",
			wantCode:        http.StatusOK,
			wantContentType: "text/plain",
			wantBody:        "",
		},
		{
			name:            "easypay returns plain text success",
			providerKey:     "easypay",
			wantCode:        http.StatusOK,
			wantContentType: "text/plain",
			wantBody:        "success",
		},
		{
			name:            "alipay returns plain text success",
			providerKey:     "alipay",
			wantCode:        http.StatusOK,
			wantContentType: "text/plain",
			wantBody:        "success",
		},
		{
			name:            "unknown provider returns plain text success",
			providerKey:     "unknown_provider",
			wantCode:        http.StatusOK,
			wantContentType: "text/plain",
			wantBody:        "success",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			writeSuccessResponse(c, tt.providerKey)

			assert.Equal(t, tt.wantCode, w.Code)
			if tt.wantContentType != "" {
				assert.Contains(t, w.Header().Get("Content-Type"), tt.wantContentType)
			}
			assert.Equal(t, tt.wantBody, w.Body.String())
		})
	}
}

func TestWebhookConstants(t *testing.T) {
	t.Run("maxWebhookBodySize is 1MB", func(t *testing.T) {
		assert.Equal(t, int64(1<<20), int64(maxWebhookBodySize))
	})

	t.Run("webhookLogTruncateLen is 200", func(t *testing.T) {
		assert.Equal(t, 200, webhookLogTruncateLen)
	})
}

func TestExtractOutTradeNo(t *testing.T) {
	tests := []struct {
		name        string
		providerKey string
		rawBody     string
		want        string
	}{
		{
			name:        "alipay form callback reads central order reference",
			providerKey: "alipay",
			rawBody:     "out_trade_no=tbc_abc123&trade_status=TRADE_SUCCESS",
			want:        "tbc_abc123",
		},
		{
			name:        "alipay form callback decodes escaped reference",
			providerKey: "alipay",
			rawBody:     "out_trade_no=sub2_order%2B42&trade_status=TRADE_SUCCESS",
			want:        "sub2_order+42",
		},
		{
			name:        "wxpay encrypted callback has no clear-text order reference",
			providerKey: "wxpay",
			rawBody:     `{"resource":{"ciphertext":"encrypted"},"mchid":"untrusted"}`,
			want:        "",
		},
		{
			name:        "easypay reads out_trade_no from query string",
			providerKey: "easypay",
			rawBody:     "out_trade_no=sub2_order_42&trade_status=TRADE_SUCCESS",
			want:        "sub2_order_42",
		},
		{
			name:        "stripe checkout session reads metadata orderId",
			providerKey: "stripe",
			rawBody:     `{"type":"checkout.session.completed","data":{"object":{"id":"cs_test_123","metadata":{"orderId":"sub2_order_42"}}}}`,
			want:        "sub2_order_42",
		},
		{
			name:        "stripe checkout session falls back to client_reference_id",
			providerKey: "stripe",
			rawBody:     `{"type":"checkout.session.completed","data":{"object":{"id":"cs_test_123","client_reference_id":"sub2_order_42"}}}`,
			want:        "sub2_order_42",
		},
		{
			name:        "stripe legacy payment intent reads metadata orderId",
			providerKey: "stripe",
			rawBody:     `{"type":"payment_intent.succeeded","data":{"object":{"id":"pi_test_123","metadata":{"orderId":"sub2_order_42"}}}}`,
			want:        "sub2_order_42",
		},
		{
			name:        "unknown provider returns empty",
			providerKey: "alipay",
			rawBody:     `{"out_trade_no":"sub2_order_42"}`,
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractOutTradeNo(tt.rawBody, tt.providerKey)
			assert.Equal(t, tt.want, got)
		})
	}
}

type webhookCandidateProvider struct {
	verified *payment.PaymentNotification
	err      error
}

func (*webhookCandidateProvider) Name() string        { return "wxpay" }
func (*webhookCandidateProvider) ProviderKey() string { return payment.TypeWxpay }
func (*webhookCandidateProvider) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeWxpay}
}
func (*webhookCandidateProvider) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	return nil, nil
}
func (*webhookCandidateProvider) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	return nil, nil
}
func (p *webhookCandidateProvider) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	return p.verified, p.err
}
func (*webhookCandidateProvider) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	return nil, nil
}

func TestVerifyWxpayCandidatesMatchesVerifiedMerchantToOrder(t *testing.T) {
	result := &payment.PaymentNotification{OrderID: "tbc_order", Status: payment.ProviderStatusSuccess}
	candidates := []service.WebhookProviderCandidate{
		{InstanceID: "first", Provider: &webhookCandidateProvider{err: errors.New("signature mismatch")}},
		{InstanceID: "second", Provider: &webhookCandidateProvider{verified: result}},
	}
	got, err := verifyWxpayCandidates(context.Background(), candidates, `{"resource":"encrypted"}`, nil,
		func(orderID, instanceID string) error {
			assert.Equal(t, "tbc_order", orderID)
			assert.Equal(t, "second", instanceID)
			return nil
		})
	assert.NoError(t, err)
	assert.Same(t, result, got)

	_, err = verifyWxpayCandidates(context.Background(), candidates, `{"resource":"encrypted"}`, nil,
		func(string, string) error { return errors.New("frozen merchant mismatch") })
	assert.ErrorContains(t, err, "frozen merchant mismatch")

	_, err = verifyWxpayCandidates(context.Background(), []service.WebhookProviderCandidate{
		{InstanceID: "first", Provider: &webhookCandidateProvider{err: errors.New("signature mismatch")}},
	}, `{"resource":"encrypted"}`, nil, func(string, string) error { t.Fatal("unverified merchant accepted"); return nil })
	assert.ErrorContains(t, err, "no configured wxpay merchant verified")
}
