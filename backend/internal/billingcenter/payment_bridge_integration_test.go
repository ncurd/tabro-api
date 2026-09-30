//go:build billing_integration

package billingcenter

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func paymentBridgeFixture(t *testing.T) (*PaymentBridge, *bridgeTestProvider, *bridgeTestAuthority) {
	t.Helper()
	raw := os.Getenv("BILLING_CENTER_TEST_DSN")
	if raw == "" {
		t.Skip("BILLING_CENTER_TEST_DSN required")
	}
	u, err := url.Parse(raw)
	require.NoError(t, err)
	require.Equal(t, "billing_center_test", strings.TrimPrefix(u.Path, "/"))
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, u.Hostname())
	admin, err := sql.Open("postgres", raw)
	require.NoError(t, err)
	schema := "payment_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	db.SetMaxOpenConns(30)
	t.Cleanup(func() { db.Close(); _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); admin.Close() })
	migration, err := migrations.FS.ReadFile("123_billing_center_payment_bridge.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	provider := &bridgeTestProvider{}
	authority := &bridgeTestAuthority{order: bridgeOrder(), processed: map[string]bool{}}
	bridge := &PaymentBridge{Store: PaymentStore{DB: db, ProducerID: "payments"}, Authority: authority, Providers: bridgeTestResolver{provider}, CallbackBaseURL: "https://gateway.example.test", AccountCenterBaseURL: "https://auth.example.test"}
	return bridge, provider, authority
}
func bridgeOrder() PaymentOrder {
	return PaymentOrder{OrderID: "center-order-test", AccountID: "account", ProductName: "Frozen purchase", Kind: "wallet", State: "pending", PaymentCurrency: "CNY", PaymentAmount: "7.00", GrantUnit: "legacy_credit", GrantAmount: "10.00", PaymentProducerID: "payments", PaymentMethod: "alipay", CheckoutState: "pending"}
}

type bridgeTestResolver struct{ p *bridgeTestProvider }

func (r bridgeTestResolver) Select(context.Context, PaymentOrder) (PaymentProviderSelection, error) {
	return PaymentProviderSelection{InstanceID: "1", Key: "alipay", Provider: r.p}, nil
}
func (r bridgeTestResolver) Resolve(context.Context, string, string) (payment.Provider, error) {
	return r.p, nil
}
func (r bridgeTestResolver) Adopt(context.Context, PaymentOrder) (PaymentJob, error) {
	return PaymentJob{}, ErrNotFound
}

type bridgeTestProvider struct {
	queryClosed              atomic.Bool
	createCalls, refundCalls atomic.Int32
	failCreate, failRefund   atomic.Bool
	refundPending            atomic.Bool
}

func (p *bridgeTestProvider) Name() string                          { return "test" }
func (p *bridgeTestProvider) ProviderKey() string                   { return "alipay" }
func (p *bridgeTestProvider) SupportedTypes() []payment.PaymentType { return []string{"alipay"} }
func (p *bridgeTestProvider) CreatePayment(_ context.Context, r payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	p.createCalls.Add(1)
	if p.failCreate.Load() {
		return nil, errors.New("provider response lost")
	}
	return &payment.CreatePaymentResponse{PayURL: "https://checkout.example.test/pay", TradeNo: "provider-trade"}, nil
}
func (p *bridgeTestProvider) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	return &payment.QueryOrderResponse{Status: "failed", ConfirmedUnpaidClosed: p.queryClosed.Load()}, nil
}
func (p *bridgeTestProvider) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	return nil, errors.New("test does not bypass webhook signatures")
}
func (p *bridgeTestProvider) Refund(_ context.Context, r payment.RefundRequest) (*payment.RefundResponse, error) {
	p.refundCalls.Add(1)
	if r.RefundID == "" {
		return nil, ErrConflict
	}
	if p.failRefund.Load() {
		return nil, errors.New("provider refund response lost")
	}
	state := "success"
	if p.refundPending.Load() {
		state = "pending"
	}
	return &payment.RefundResponse{RefundID: "provider-refund", Status: state}, nil
}
func (p *bridgeTestProvider) QueryRefund(context.Context, string) (*payment.RefundResponse, error) {
	return &payment.RefundResponse{RefundID: "provider-refund", Status: "success"}, nil
}

type bridgeTestAuthority struct {
	mu                         sync.Mutex
	order                      PaymentOrder
	processed                  map[string]bool
	cashCredits, refundEffects int
	losePaidResponse           bool
}

func (a *bridgeTestAuthority) PaymentWork(context.Context, int) (PaymentWork, error) {
	return PaymentWork{}, nil
}
func (a *bridgeTestAuthority) PaymentOrder(context.Context, string) (PaymentOrder, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.order, nil
}
func (a *bridgeTestAuthority) ClaimPaymentCheckout(_ context.Context, id string, c PaymentClaim) (PaymentOrder, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.order.CheckoutState = "dispatched"
	a.order.CheckoutClaimEventID = c.EventID
	return a.order, nil
}
func (a *bridgeTestAuthority) CompletePaymentCheckout(_ context.Context, id string, c PaymentCheckout) (PaymentOrder, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.order.CheckoutState = "ready"
	return a.order, nil
}
func (a *bridgeTestAuthority) ConfirmPaymentPaid(_ context.Context, id string, p PaymentPaid) (PaymentOrder, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.processed[p.EventID] {
		a.processed[p.EventID] = true
		a.cashCredits++
		a.order.ProviderTransactionID = p.ProviderTransactionID
		a.order.State = "paid"
	}
	if a.losePaidResponse {
		a.losePaidResponse = false
		return PaymentOrder{}, errors.New("Auth committed but response lost")
	}
	return a.order, nil
}
func (a *bridgeTestAuthority) CompletePaymentRefund(_ context.Context, id string, p PaymentRefundComplete) (PaymentRefund, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.processed[p.EventID] {
		a.processed[p.EventID] = true
		a.refundEffects++
	}
	state := "failed"
	if p.Succeeded {
		state = "succeeded"
	}
	return PaymentRefund{RefundID: id, State: state}, nil
}

func TestPaymentBridgeConcurrentCheckoutAndCallbackProduceOneProviderCallAndOneCredit(t *testing.T) {
	b, p, a := paymentBridgeFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); require.NoError(t, b.Checkout(ctx, bridgeOrder())) }()
	}
	wg.Wait()
	require.EqualValues(t, 1, p.createCalls.Load())
	job, err := b.Store.Order(ctx, bridgeOrder().OrderID)
	require.NoError(t, err)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, b.Notify(ctx, &payment.PaymentNotification{OrderID: job.OutTradeNo, TradeNo: "payment-1", ExactAmount: "7", Currency: "CNY", Status: "success"}, "alipay"))
		}()
	}
	wg.Wait()
	var events int
	require.NoError(t, b.Store.DB.QueryRow(`SELECT count(*) FROM billing_center_payment_outbox`).Scan(&events))
	require.Equal(t, 2, events)
	a.losePaidResponse = true
	require.Error(t, b.Deliver(ctx))
	require.Equal(t, 1, a.cashCredits)
	require.NoError(t, b.Deliver(ctx))
	require.NoError(t, b.Deliver(ctx))
	require.Equal(t, 1, a.cashCredits)
	for _, n := range []payment.PaymentNotification{{ExactAmount: "7", Currency: "USD"}, {ExactAmount: "8", Currency: "CNY"}, {Amount: 7, Currency: "CNY"}} {
		n.OrderID = job.OutTradeNo
		n.TradeNo = "payment-1"
		n.Status = "success"
		require.Error(t, b.Notify(ctx, &n, "alipay"))
	}
	changed := bridgeOrder()
	changed.PaymentAmount = "8"
	_, err = b.Store.CreateOrder(ctx, changed, "1", "alipay")
	require.Error(t, err)
}

func TestPaymentBridgeUnknownProviderCreateOrRefundNeverRetriesExternalEffect(t *testing.T) {
	b, p, _ := paymentBridgeFixture(t)
	ctx := context.Background()
	p.failCreate.Store(true)
	require.Error(t, b.Checkout(ctx, bridgeOrder()))
	p.failCreate.Store(false)
	for range 4 {
		require.NoError(t, b.Checkout(ctx, bridgeOrder()))
	}
	require.EqualValues(t, 1, p.createCalls.Load())
	job, err := b.Store.Order(ctx, bridgeOrder().OrderID)
	require.NoError(t, err)
	require.Equal(t, "dispatching", job.State)
	// A later signed callback may resolve the unknown creation without Create.
	require.NoError(t, b.Notify(ctx, &payment.PaymentNotification{OrderID: job.OutTradeNo, TradeNo: "payment-1", ExactAmount: "7", Currency: "CNY", Status: "success"}, "alipay"))
	refund := PaymentRefund{RefundID: "refund-1", OrderID: job.Order.OrderID, PaymentCurrency: "CNY", PaymentAmount: "3.50", GrantAmount: "5", State: "pending", ProviderTransactionID: "payment-1"}
	p.failRefund.Store(true)
	require.Error(t, b.Refund(ctx, refund))
	p.failRefund.Store(false)
	for range 4 {
		require.NoError(t, b.Refund(ctx, refund))
	}
	require.EqualValues(t, 1, p.refundCalls.Load())
	stored, err := b.Store.Refund(ctx, refund.RefundID)
	require.NoError(t, err)
	require.Equal(t, "dispatching", stored.State)
	var count int
	require.NoError(t, b.Store.DB.QueryRow(`SELECT count(*) FROM billing_center_payment_outbox WHERE kind='refund_complete'`).Scan(&count))
	require.Zero(t, count)
}

func TestPaymentBridgePendingRefundIsQueriedAndCompletesWithoutSecondRefund(t *testing.T) {
	b, p, a := paymentBridgeFixture(t)
	ctx := context.Background()
	require.NoError(t, b.Checkout(ctx, bridgeOrder()))
	job, err := b.Store.Order(ctx, bridgeOrder().OrderID)
	require.NoError(t, err)
	require.NoError(t, b.Notify(ctx, &payment.PaymentNotification{OrderID: job.OutTradeNo, TradeNo: "payment-1", ExactAmount: "7", Currency: "CNY", Status: "success"}, "alipay"))
	require.NoError(t, b.Deliver(ctx))
	refund := PaymentRefund{RefundID: "refund-pending", OrderID: job.Order.OrderID, PaymentCurrency: "CNY", PaymentAmount: "3.50", GrantAmount: "5", State: "pending", ProviderTransactionID: "payment-1"}
	p.refundPending.Store(true)
	require.NoError(t, b.Refund(ctx, refund))
	require.NoError(t, b.Refund(ctx, refund))
	require.NoError(t, b.Deliver(ctx))
	require.NoError(t, b.Refund(ctx, refund))
	require.EqualValues(t, 1, p.refundCalls.Load())
	require.Equal(t, 1, a.refundEffects)
}

func (a *bridgeTestAuthority) ConfirmPaymentCancelled(_ context.Context, id string, c PaymentCancelled) (PaymentOrder, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !c.ConfirmedNoPayment {
		return PaymentOrder{}, ErrConflict
	}
	a.order.State = "cancelled"
	return a.order, nil
}

func TestPaymentBridgeOnlyProvenUnpaidClosureCancelsAndMoneyOutboxRollbackIsAtomic(t *testing.T) {
	b, p, a := paymentBridgeFixture(t)
	ctx := context.Background()
	require.NoError(t, b.Checkout(ctx, bridgeOrder()))
	require.NoError(t, b.Deliver(ctx))
	require.NoError(t, b.PollPayments(ctx))
	job, err := b.Store.Order(ctx, bridgeOrder().OrderID)
	require.NoError(t, err)
	require.Equal(t, "ready", job.State)
	_, err = b.Store.DB.Exec(`CREATE FUNCTION reject_paid_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='paid' THEN RAISE EXCEPTION 'injected storage failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_paid BEFORE INSERT ON billing_center_payment_outbox FOR EACH ROW EXECUTE FUNCTION reject_paid_outbox();`)
	require.NoError(t, err)
	notification := &payment.PaymentNotification{OrderID: job.OutTradeNo, TradeNo: "payment", ExactAmount: "7.00", Currency: "CNY", Status: "success"}
	require.Error(t, b.Notify(ctx, notification, "alipay"))
	job, err = b.Store.Order(ctx, bridgeOrder().OrderID)
	require.NoError(t, err)
	require.Equal(t, "ready", job.State)
	require.Zero(t, a.cashCredits)
	_, err = b.Store.DB.Exec(`DROP TRIGGER reject_paid ON billing_center_payment_outbox`)
	require.NoError(t, err)
	p.queryClosed.Store(true)
	require.NoError(t, b.PollPayments(ctx))
	require.NoError(t, b.Deliver(ctx))
	require.Equal(t, "cancelled", a.order.State)
	require.Error(t, b.Notify(ctx, notification, "alipay"))
	require.Zero(t, a.cashCredits)
}
