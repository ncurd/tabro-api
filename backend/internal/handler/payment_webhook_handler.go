package handler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/tidwall/gjson"

	"github.com/gin-gonic/gin"
)

// PaymentWebhookHandler handles payment provider webhook callbacks.
type PaymentWebhookHandler struct {
	paymentService *service.PaymentService
	registry       *payment.Registry
}

// maxWebhookBodySize is the maximum allowed webhook request body size (1 MB).
const maxWebhookBodySize = 1 << 20

// webhookLogTruncateLen is the maximum length of raw body logged on verify failure.
const webhookLogTruncateLen = 200

// NewPaymentWebhookHandler creates a new PaymentWebhookHandler.
func NewPaymentWebhookHandler(paymentService *service.PaymentService, registry *payment.Registry) *PaymentWebhookHandler {
	return &PaymentWebhookHandler{
		paymentService: paymentService,
		registry:       registry,
	}
}

// EasyPayNotify handles EasyPay payment notifications.
// POST /api/v1/payment/webhook/easypay
func (h *PaymentWebhookHandler) EasyPayNotify(c *gin.Context) {
	h.handleNotify(c, payment.TypeEasyPay)
}

// AlipayNotify handles Alipay payment notifications.
// POST /api/v1/payment/webhook/alipay
func (h *PaymentWebhookHandler) AlipayNotify(c *gin.Context) {
	h.handleNotify(c, payment.TypeAlipay)
}

// WxpayNotify handles WeChat Pay payment notifications.
// POST /api/v1/payment/webhook/wxpay
func (h *PaymentWebhookHandler) WxpayNotify(c *gin.Context) {
	h.handleNotify(c, payment.TypeWxpay)
}

// StripeWebhook handles Stripe webhook events.
// POST /api/v1/payment/webhook/stripe
func (h *PaymentWebhookHandler) StripeWebhook(c *gin.Context) {
	h.handleNotify(c, payment.TypeStripe)
}

// handleNotify is the shared logic for all provider webhook handlers.
func (h *PaymentWebhookHandler) handleNotify(c *gin.Context, providerKey string) {
	var rawBody string
	if c.Request.Method == http.MethodGet {
		// GET callbacks (e.g. EasyPay) pass params as URL query string
		rawBody = c.Request.URL.RawQuery
	} else {
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxWebhookBodySize))
		if err != nil {
			slog.Error("[Payment Webhook] failed to read body", "provider", providerKey, "error", err)
			c.String(http.StatusBadRequest, "failed to read body")
			return
		}
		rawBody = string(body)
	}

	// Extract out_trade_no to look up the order's specific provider instance.
	// This is needed when multiple instances of the same provider exist (e.g. multiple EasyPay accounts).
	outTradeNo := extractOutTradeNo(rawBody, providerKey)

	headers := make(map[string]string)
	for k := range c.Request.Header {
		headers[strings.ToLower(k)] = c.GetHeader(k)
	}

	var notification *payment.PaymentNotification
	var err error
	if providerKey == payment.TypeWxpay {
		var candidates []service.WebhookProviderCandidate
		candidates, err = h.paymentService.GetWxpayWebhookProviders(c.Request.Context())
		if err == nil {
			notification, err = verifyWxpayCandidates(c.Request.Context(), candidates, rawBody, headers,
				func(orderID, instanceID string) error {
					return h.paymentService.MatchWebhookProviderInstance(c.Request.Context(), providerKey, orderID, instanceID)
				})
		}
	} else {
		var provider payment.Provider
		provider, err = h.paymentService.GetWebhookProvider(c.Request.Context(), providerKey, outTradeNo)
		if err == nil {
			notification, err = provider.VerifyNotification(c.Request.Context(), rawBody, headers)
		}
	}
	if err != nil {
		truncatedBody := rawBody
		if len(truncatedBody) > webhookLogTruncateLen {
			truncatedBody = truncatedBody[:webhookLogTruncateLen] + "...(truncated)"
		}
		slog.Error("[Payment Webhook] verify failed", "provider", providerKey, "error", err, "method", c.Request.Method, "bodyLen", len(rawBody))
		slog.Debug("[Payment Webhook] verify failed body", "provider", providerKey, "rawBody", truncatedBody)
		c.String(http.StatusBadRequest, "verify failed")
		return
	}

	// nil notification means irrelevant event (e.g. Stripe non-payment event); return success.
	if notification == nil {
		writeSuccessResponse(c, providerKey)
		return
	}

	if err := h.paymentService.HandlePaymentNotification(c.Request.Context(), notification, providerKey); err != nil {
		slog.Error("[Payment Webhook] handle notification failed", "provider", providerKey, "error", err)
		c.String(http.StatusInternalServerError, "handle failed")
		return
	}

	writeSuccessResponse(c, providerKey)
}

// The order reference is encrypted in a WeChat callback. Try each configured
// merchant's official signature verifier and API v3 decryptor, then bind the
// verified order back to the exact merchant instance frozen at checkout.
func verifyWxpayCandidates(ctx context.Context, candidates []service.WebhookProviderCandidate, rawBody string,
	headers map[string]string, match func(orderID, instanceID string) error) (*payment.PaymentNotification, error) {
	for _, candidate := range candidates {
		notification, err := candidate.Provider.VerifyNotification(ctx, rawBody, headers)
		if err != nil {
			continue
		}
		if notification == nil {
			return nil, nil // Authenticated non-payment event.
		}
		if err := match(notification.OrderID, candidate.InstanceID); err != nil {
			return nil, fmt.Errorf("wxpay verified merchant does not own order: %w", err)
		}
		return notification, nil
	}
	return nil, fmt.Errorf("no configured wxpay merchant verified the notification")
}

// extractOutTradeNo parses the webhook body to find the out_trade_no.
// This allows looking up the correct provider instance before verification.
func extractOutTradeNo(rawBody, providerKey string) string {
	switch providerKey {
	case payment.TypeEasyPay, payment.TypeAlipay:
		values, err := url.ParseQuery(rawBody)
		if err == nil {
			return values.Get("out_trade_no")
		}
	case payment.TypeStripe:
		if orderID := gjson.Get(rawBody, "data.object.metadata.orderId").String(); orderID != "" {
			return orderID
		}
		return gjson.Get(rawBody, "data.object.client_reference_id").String()
	}
	// WeChat's out_trade_no is inside the signed, encrypted resource.
	return ""
}

// writeSuccessResponse sends the provider-specific success response.
// WeChat Pay accepts an empty HTTP 204 response;
// Stripe expects an empty 200; others accept plain text "success".
func writeSuccessResponse(c *gin.Context, providerKey string) {
	switch providerKey {
	case payment.TypeWxpay:
		c.Status(http.StatusNoContent)
		c.Writer.WriteHeaderNow()
	case payment.TypeStripe:
		c.String(http.StatusOK, "")
	default:
		c.String(http.StatusOK, "success")
	}
}
