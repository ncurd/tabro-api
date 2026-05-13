//go:build unit

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
	stripe "github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/form"
	"github.com/stripe/stripe-go/v85/webhook"
)

func TestStripeCreatePaymentReturnsCheckoutSessionURL(t *testing.T) {
	t.Parallel()

	provider := newTestStripeProvider(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/checkout/sessions", r.URL.Path)
		require.NoError(t, r.ParseForm())

		require.Equal(t, "payment", r.Form.Get("mode"))
		require.Equal(t, "https://app.example.com/payment/result?order_id=42&out_trade_no=sub2_order_42", r.Form.Get("success_url"))
		require.Equal(t, "https://app.example.com/payment/result?order_id=42&out_trade_no=sub2_order_42&status=cancel", r.Form.Get("cancel_url"))
		require.Equal(t, "sub2_order_42", r.Form.Get("client_reference_id"))
		require.Equal(t, "sub2_order_42", r.Form.Get("metadata[orderId]"))
		require.Equal(t, "sub2_order_42", r.Form.Get("payment_intent_data[metadata][orderId]"))
		require.Equal(t, "card", r.Form.Get("payment_method_types[0]"))
		require.Equal(t, "wechat_pay", r.Form.Get("payment_method_types[1]"))
		require.Equal(t, "web", r.Form.Get("payment_method_options[wechat_pay][client]"))
		require.Equal(t, "cny", r.Form.Get("line_items[0][price_data][currency]"))
		require.Equal(t, "1234", r.Form.Get("line_items[0][price_data][unit_amount]"))
		require.Equal(t, "Balance Recharge", r.Form.Get("line_items[0][price_data][product_data][name]"))
		require.Equal(t, "1", r.Form.Get("line_items[0][quantity]"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"cs_test_123","object":"checkout.session","url":"https://checkout.stripe.com/c/pay/cs_test_123"}`)
	})

	resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:            "sub2_order_42",
		Amount:             "12.34",
		PaymentType:        payment.TypeStripe,
		Subject:            "Balance Recharge",
		ReturnURL:          "https://app.example.com/payment/result?order_id=42&out_trade_no=sub2_order_42",
		CancelURL:          "https://app.example.com/payment/result?order_id=42&out_trade_no=sub2_order_42&status=cancel",
		InstanceSubMethods: "card,wxpay",
	})
	require.NoError(t, err)
	require.Equal(t, "cs_test_123", resp.TradeNo)
	require.Equal(t, "https://checkout.stripe.com/c/pay/cs_test_123", resp.PayURL)
	require.Empty(t, resp.ClientSecret)
}

func TestStripeCreatePaymentUsesRequestedCurrency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		currency string
		want     string
	}{
		{currency: "USD", want: "usd"},
		{currency: "EUR", want: "eur"},
	}

	for _, tt := range tests {
		t.Run(tt.currency, func(t *testing.T) {
			t.Parallel()

			provider := newTestStripeProvider(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "/v1/checkout/sessions", r.URL.Path)
				require.NoError(t, r.ParseForm())

				require.Equal(t, tt.want, r.Form.Get("line_items[0][price_data][currency]"))
				require.Equal(t, "1000", r.Form.Get("line_items[0][price_data][unit_amount]"))
				require.Equal(t, tt.currency, r.Form.Get("metadata[currency]"))
				require.Equal(t, tt.currency, r.Form.Get("payment_intent_data[metadata][currency]"))

				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"id":"cs_test_%s","object":"checkout.session","url":"https://checkout.stripe.com/c/pay/cs_test_%s"}`, tt.want, tt.want)
			})

			resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
				OrderID:     "sub2_order_43",
				Amount:      "10.00",
				Currency:    tt.currency,
				PaymentType: payment.TypeStripe,
				Subject:     "Balance Recharge",
				ReturnURL:   "https://app.example.com/payment/result?order_id=43&out_trade_no=sub2_order_43",
			})
			require.NoError(t, err)
			require.Equal(t, "cs_test_"+tt.want, resp.TradeNo)
			require.Equal(t, "https://checkout.stripe.com/c/pay/cs_test_"+tt.want, resp.PayURL)
		})
	}
}

func TestResolveStripeCurrency(t *testing.T) {
	require.Equal(t, "cny", resolveStripeCurrency(""))
	require.Equal(t, "usd", resolveStripeCurrency("USD"))
	require.Equal(t, "gbp", resolveStripeCurrency(" gbp "))
	require.Equal(t, "eur", resolveStripeCurrency("eur"))
}

func TestStripeQueryOrderMapsCheckoutSessionStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		response   string
		wantStatus string
		wantAmount float64
	}{
		{
			name:       "paid session maps to paid",
			response:   `{"id":"cs_test_123","object":"checkout.session","status":"complete","payment_status":"paid","amount_total":1234}`,
			wantStatus: payment.ProviderStatusPaid,
			wantAmount: 12.34,
		},
		{
			name:       "expired session maps to failed",
			response:   `{"id":"cs_test_123","object":"checkout.session","status":"expired","payment_status":"unpaid","amount_total":1234}`,
			wantStatus: payment.ProviderStatusFailed,
			wantAmount: 12.34,
		},
		{
			name:       "unpaid completed session stays pending",
			response:   `{"id":"cs_test_123","object":"checkout.session","status":"complete","payment_status":"unpaid","amount_total":1234}`,
			wantStatus: payment.ProviderStatusPending,
			wantAmount: 12.34,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := newTestStripeProvider(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "/v1/checkout/sessions/cs_test_123", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tt.response)
			})

			resp, err := provider.QueryOrder(context.Background(), "cs_test_123")
			require.NoError(t, err)
			require.Equal(t, "cs_test_123", resp.TradeNo)
			require.Equal(t, tt.wantStatus, resp.Status)
			require.Equal(t, tt.wantAmount, resp.Amount)
		})
	}
}

func TestStripeCancelPaymentExpiresCheckoutSession(t *testing.T) {
	t.Parallel()

	provider := newTestStripeProvider(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/checkout/sessions/cs_test_123/expire", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"cs_test_123","object":"checkout.session","status":"expired"}`)
	})

	require.NoError(t, provider.CancelPayment(context.Background(), "cs_test_123"))
}

func TestStripeRefundUsesCheckoutSessionPaymentIntent(t *testing.T) {
	t.Parallel()

	provider := newTestStripeProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/checkout/sessions/cs_test_123":
			require.Equal(t, "payment_intent", r.URL.Query().Get("expand[0]"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"cs_test_123","object":"checkout.session","payment_intent":{"id":"pi_test_123","object":"payment_intent"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/refunds":
			require.NoError(t, r.ParseForm())
			require.Equal(t, "pi_test_123", r.Form.Get("payment_intent"))
			require.Equal(t, "1234", r.Form.Get("amount"))
			require.Equal(t, "requested_by_customer", r.Form.Get("reason"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"re_test_123","object":"refund","status":"succeeded"}`)
		default:
			t.Fatalf("unexpected Stripe request: %s %s", r.Method, r.URL.String())
		}
	})

	resp, err := provider.Refund(context.Background(), payment.RefundRequest{
		TradeNo: "cs_test_123",
		OrderID: "sub2_order_42",
		Amount:  "12.34",
	})
	require.NoError(t, err)
	require.Equal(t, "re_test_123", resp.RefundID)
	require.Equal(t, payment.ProviderStatusSuccess, resp.Status)
}

func TestStripeVerifyNotificationParsesCheckoutSessionEvents(t *testing.T) {
	t.Parallel()

	provider := mustNewStripeProvider(t, map[string]string{
		"secretKey":      "sk_test_123",
		"publishableKey": "pk_test_123",
		"webhookSecret":  "whsec_test_123",
	})

	tests := []struct {
		name      string
		eventType string
		object    string
		want      *payment.PaymentNotification
	}{
		{
			name:      "completed paid session returns success notification",
			eventType: "checkout.session.completed",
			object:    `{"id":"cs_test_123","object":"checkout.session","status":"complete","payment_status":"paid","amount_total":1234,"metadata":{"orderId":"sub2_order_42"}}`,
			want: &payment.PaymentNotification{
				TradeNo: "cs_test_123",
				OrderID: "sub2_order_42",
				Amount:  12.34,
				Status:  payment.ProviderStatusSuccess,
			},
		},
		{
			name:      "completed unpaid session is ignored",
			eventType: "checkout.session.completed",
			object:    `{"id":"cs_test_123","object":"checkout.session","status":"complete","payment_status":"unpaid","amount_total":1234,"metadata":{"orderId":"sub2_order_42"}}`,
			want:      nil,
		},
		{
			name:      "async payment succeeded returns success notification",
			eventType: "checkout.session.async_payment_succeeded",
			object:    `{"id":"cs_test_123","object":"checkout.session","status":"complete","payment_status":"paid","amount_total":1234,"metadata":{"orderId":"sub2_order_42"}}`,
			want: &payment.PaymentNotification{
				TradeNo: "cs_test_123",
				OrderID: "sub2_order_42",
				Amount:  12.34,
				Status:  payment.ProviderStatusSuccess,
			},
		},
		{
			name:      "async payment failed returns failed notification",
			eventType: "checkout.session.async_payment_failed",
			object:    `{"id":"cs_test_123","object":"checkout.session","status":"complete","payment_status":"unpaid","amount_total":1234,"metadata":{"orderId":"sub2_order_42"}}`,
			want: &payment.PaymentNotification{
				TradeNo: "cs_test_123",
				OrderID: "sub2_order_42",
				Amount:  12.34,
				Status:  payment.ProviderStatusFailed,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rawBody, header := signedStripeEvent(t, provider.config["webhookSecret"], stripe.APIVersion, tt.eventType, tt.object)
			got, err := provider.VerifyNotification(context.Background(), rawBody, map[string]string{"stripe-signature": header})
			require.NoError(t, err)

			if tt.want == nil {
				require.Nil(t, got)
				return
			}

			require.NotNil(t, got)
			require.Equal(t, tt.want.TradeNo, got.TradeNo)
			require.Equal(t, tt.want.OrderID, got.OrderID)
			require.Equal(t, tt.want.Amount, got.Amount)
			require.Equal(t, tt.want.Status, got.Status)
			require.Equal(t, rawBody, got.RawData)
		})
	}
}

func TestStripeVerifyNotificationAcceptsLegacyPaymentIntentWebhook(t *testing.T) {
	t.Parallel()

	provider := mustNewStripeProvider(t, map[string]string{
		"secretKey":      "sk_test_123",
		"publishableKey": "pk_test_123",
		"webhookSecret":  "whsec_test_123",
	})

	rawBody, header := signedStripeEvent(
		t,
		provider.config["webhookSecret"],
		"2018-11-08",
		"payment_intent.succeeded",
		`{"id":"pi_test_123","object":"payment_intent","amount":1234,"metadata":{"orderId":"sub2_order_42"}}`,
	)

	got, err := provider.VerifyNotification(context.Background(), rawBody, map[string]string{"stripe-signature": header})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "pi_test_123", got.TradeNo)
	require.Equal(t, "sub2_order_42", got.OrderID)
	require.Equal(t, 12.34, got.Amount)
	require.Equal(t, payment.ProviderStatusSuccess, got.Status)
	require.Equal(t, rawBody, got.RawData)
}

func mustNewStripeProvider(t *testing.T, cfg map[string]string) *Stripe {
	t.Helper()

	provider, err := NewStripe("test-instance", cfg)
	require.NoError(t, err)
	return provider
}

func newTestStripeProvider(t *testing.T, handler http.HandlerFunc) *Stripe {
	t.Helper()

	provider := mustNewStripeProvider(t, map[string]string{
		"secretKey":      "sk_test_123",
		"publishableKey": "pk_test_123",
		"webhookSecret":  "whsec_test_123",
	})
	backend := &stripeMockBackend{
		t: t,
		handler: func(method, path string, values url.Values) string {
			reqURL := &url.URL{Path: path}
			var body io.ReadCloser
			if method == http.MethodGet {
				reqURL.RawQuery = values.Encode()
			} else {
				body = io.NopCloser(strings.NewReader(values.Encode()))
			}
			req := &http.Request{
				Method: method,
				URL:    reqURL,
				Body:   body,
				Header: http.Header{},
				Form:   values,
			}
			if method != http.MethodGet {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			recorder := newStripeTestResponseWriter()
			handler(recorder, req)
			return recorder.body
		},
	}
	provider.sc = stripe.NewClient("sk_test_123", stripe.WithBackends(&stripe.Backends{
		API:         backend,
		Connect:     backend,
		Uploads:     backend,
		MeterEvents: backend,
	}))
	provider.initialized = true
	return provider
}

func signedStripeEvent(t *testing.T, secret, apiVersion, eventType, objectJSON string) (string, string) {
	t.Helper()

	payload := fmt.Sprintf(
		`{"id":"evt_test_123","object":"event","api_version":"%s","type":"%s","data":{"object":%s}}`,
		apiVersion,
		eventType,
		objectJSON,
	)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: []byte(payload),
		Secret:  secret,
	})
	return payload, signed.Header
}

type stripeMockBackend struct {
	t       *testing.T
	handler func(method, path string, values url.Values) string
}

func (b *stripeMockBackend) Call(method, path, _ string, params stripe.ParamsContainer, v stripe.LastResponseSetter) error {
	b.t.Helper()

	values := stripeParamsToValues(params)
	body := b.handler(method, path, values)
	v.SetLastResponse(&stripe.APIResponse{
		Header:     http.Header{},
		RawJSON:    []byte(body),
		Status:     "200 OK",
		StatusCode: http.StatusOK,
	})
	return json.Unmarshal([]byte(body), v)
}

func (b *stripeMockBackend) CallStreaming(string, string, string, stripe.ParamsContainer, stripe.StreamingLastResponseSetter) error {
	b.t.Helper()
	return fmt.Errorf("streaming calls are not expected in Stripe provider tests")
}

func (b *stripeMockBackend) CallRaw(string, string, string, []byte, *stripe.Params, stripe.LastResponseSetter) error {
	b.t.Helper()
	return fmt.Errorf("raw calls are not expected in Stripe provider tests")
}

func (b *stripeMockBackend) CallMultipart(string, string, string, string, *bytes.Buffer, *stripe.Params, stripe.LastResponseSetter) error {
	b.t.Helper()
	return fmt.Errorf("multipart calls are not expected in Stripe provider tests")
}

func (b *stripeMockBackend) SetMaxNetworkRetries(int64) {}

func stripeParamsToValues(params stripe.ParamsContainer) url.Values {
	if params == nil {
		return url.Values{}
	}
	formValues := &form.Values{}
	form.AppendTo(formValues, params)
	return formValues.ToValues()
}

type stripeTestResponseWriter struct {
	header http.Header
	body   string
	status int
}

func newStripeTestResponseWriter() *stripeTestResponseWriter {
	return &stripeTestResponseWriter{header: http.Header{}}
}

func (w *stripeTestResponseWriter) Header() http.Header {
	return w.header
}

func (w *stripeTestResponseWriter) Write(body []byte) (int, error) {
	w.body += string(body)
	return len(body), nil
}

func (w *stripeTestResponseWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}
