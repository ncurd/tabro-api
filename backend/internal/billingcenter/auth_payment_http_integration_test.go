//go:build billing_auth_integration && billing_integration

package billingcenter

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

// Auth and both database schemas are temporary; the payment provider is fake so
// this exercises the real OAuth/HTTP/outbox contract without making a charge.
func TestAuthPaymentBridgeRealHTTPContract(t *testing.T) {
	path := os.Getenv("BILLING_AUTH_TEST_FIXTURE")
	if path == "" {
		t.Skip("isolated Auth fixture is required")
	}
	path, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(path, "/private/tmp/") || strings.HasPrefix(path, "/tmp/"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var f struct {
		BaseURL       string  `json:"base_url"`
		TokenURL      string  `json:"token_url"`
		ClientID      string  `json:"payment_client_id"`
		ClientSecret  string  `json:"payment_client_secret"`
		OrderID       string  `json:"payment_order_id"`
		Amount        Decimal `json:"payment_amount"`
		Currency      string  `json:"payment_currency"`
		RefundOrderID string  `json:"refund_order_id"`
		RefundID      string  `json:"refund_id"`
		RefundAmount  Decimal `json:"refund_payment_amount"`
	}
	require.NoError(t, json.Unmarshal(data, &f))
	for _, endpoint := range []string{f.BaseURL, f.TokenURL} {
		u, e := url.Parse(endpoint)
		require.NoError(t, e)
		ip := net.ParseIP(u.Hostname())
		require.True(t, u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())
	}
	require.NotEmpty(t, f.OrderID)
	tokens, err := NewClientCredentialsTokenSource(TokenConfig{TokenURL: f.TokenURL, ClientID: f.ClientID, ClientSecret: f.ClientSecret, Scopes: []string{"billing.credit"}, InsecureLocal: true}, nil)
	require.NoError(t, err)
	authority, err := NewClient(Config{BaseURL: f.BaseURL, ProducerClientID: f.ClientID, InsecureLocal: true}, tokens, nil)
	require.NoError(t, err)
	b, provider, _ := paymentBridgeFixture(t)
	b.Store.ProducerID, b.Authority = f.ClientID, authority
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	order, err := authority.PaymentOrder(ctx, f.OrderID)
	require.NoError(t, err)
	require.Equal(t, "pending", order.State)
	require.True(t, PaymentAmountsEqual(f.Amount, order.PaymentAmount))
	require.Equal(t, f.Currency, order.PaymentCurrency)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- b.Checkout(ctx, order) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	require.EqualValues(t, 1, provider.createCalls.Load())
	require.NoError(t, b.Deliver(ctx))
	job, err := b.Store.Order(ctx, f.OrderID)
	require.NoError(t, err)
	notification := &payment.PaymentNotification{OrderID: job.OutTradeNo, TradeNo: "integration-go-payment", ExactAmount: string(f.Amount), Currency: f.Currency, Status: payment.NotificationStatusSuccess}
	for range 4 {
		require.NoError(t, b.Notify(ctx, notification, "alipay"))
	}
	// A new process replays only the persisted facts, without a browser token.
	restarted := *b
	require.NoError(t, restarted.Deliver(ctx))
	require.NoError(t, restarted.Deliver(ctx))
	paid, err := authority.PaymentOrder(ctx, f.OrderID)
	require.NoError(t, err)
	require.Equal(t, "paid", paid.State)
	require.Equal(t, notification.TradeNo, paid.ProviderTransactionID)
	replay, err := authority.ConfirmPaymentPaid(ctx, f.OrderID, PaymentPaid{EventID: "paid:" + f.OrderID, ProviderTransactionID: notification.TradeNo, PaymentCurrency: f.Currency, PaymentAmount: f.Amount})
	require.NoError(t, err)
	require.Equal(t, paid, replay)
	_, err = authority.ConfirmPaymentPaid(ctx, f.OrderID, PaymentPaid{EventID: "paid:" + f.OrderID, ProviderTransactionID: notification.TradeNo, PaymentCurrency: f.Currency, PaymentAmount: "999"})
	require.Error(t, err, "same event cannot change the payment amount")

	refund, err := authority.PaymentRefund(ctx, f.RefundID)
	require.NoError(t, err)
	require.Equal(t, f.RefundOrderID, refund.OrderID)
	require.True(t, PaymentAmountsEqual(f.RefundAmount, refund.PaymentAmount))
	previousOrder, err := authority.PaymentOrder(ctx, refund.OrderID)
	require.NoError(t, err)
	_, err = b.Store.CreateOrder(ctx, previousOrder, "1", "alipay")
	require.NoError(t, err)
	// Restore the fixture's already-paid order journal as if an earlier worker
	// had completed it. No financial claim or event is manufactured in Auth.
	_, err = b.Store.DB.ExecContext(ctx, `UPDATE billing_center_payment_jobs SET state='paid',provider_transaction_id=$3 WHERE producer_client_id=$1 AND order_id=$2`, f.ClientID, refund.OrderID, refund.ProviderTransactionID)
	require.NoError(t, err)
	for range 4 {
		require.NoError(t, restarted.Refund(ctx, refund))
	}
	require.EqualValues(t, 1, provider.refundCalls.Load())
	require.NoError(t, restarted.Deliver(ctx))
	require.NoError(t, restarted.Deliver(ctx))
	completed, err := authority.PaymentRefund(ctx, f.RefundID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", completed.State)
	var pending int
	require.NoError(t, b.Store.DB.QueryRowContext(ctx, `SELECT count(*) FROM billing_center_payment_outbox WHERE producer_client_id=$1 AND delivered_at IS NULL`, f.ClientID).Scan(&pending))
	require.Zero(t, pending)
}
