package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
)

func (s *PaymentService) startBillingPaymentBridge(db *sql.DB, cfg *config.Config) (func(), error) {
	if cfg == nil || !cfg.BillingCenter.Payments.Enabled {
		return func() {}, nil
	}
	if err := cfg.BillingCenter.Validate(); err != nil {
		return nil, err
	}
	c := cfg.BillingCenter
	p := c.Payments
	if p.ProducerClientID == "auth-payment-local" {
		return nil, errors.New("Auth direct-payment orders must not be processed by the legacy gateway payment bridge")
	}
	timeout := time.Duration(c.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	tokens, err := bc.NewClientCredentialsTokenSource(bc.TokenConfig{TokenURL: c.TokenURL, ClientID: p.ProducerClientID, ClientSecret: p.ClientSecret, Scopes: []string{"billing.credit"}, Timeout: timeout, InsecureLocal: c.InsecureLocal}, nil)
	if err != nil {
		return nil, err
	}
	authority, err := bc.NewClient(bc.Config{BaseURL: c.BaseURL, ProducerClientID: p.ProducerClientID, Timeout: timeout, InsecureLocal: c.InsecureLocal}, tokens, nil)
	if err != nil {
		return nil, err
	}
	store := bc.PaymentStore{DB: db, ProducerID: p.ProducerClientID}
	s.centerPayments = &bc.PaymentBridge{Store: store, Authority: authority, Providers: &centralPaymentProviders{service: s, db: db, store: store}, CallbackBaseURL: p.CallbackBaseURL, AccountCenterBaseURL: p.AccountCenterBaseURL}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.centerPayments.Run(ctx, func(error) { slog.Warn("billing center payment adapter needs retry or reconciliation") })
	}()
	return func() { cancel(); <-done }, nil
}

type centralPaymentProviders struct {
	service *PaymentService
	db      *sql.DB
	store   bc.PaymentStore
}

func (r *centralPaymentProviders) Select(ctx context.Context, order bc.PaymentOrder) (bc.PaymentProviderSelection, error) {
	minor, err := bc.PaymentMinorUnits(order.PaymentAmount)
	if err != nil {
		return bc.PaymentProviderSelection{}, err
	}
	cfg, err := r.service.configService.GetPaymentConfig(ctx)
	if err != nil {
		return bc.PaymentProviderSelection{}, err
	}
	if !cfg.Enabled {
		return bc.PaymentProviderSelection{}, errors.New("payment providers disabled")
	}
	method := payment.GetBasePaymentType(order.PaymentMethod)
	// Central orders explicitly sold as Alipay or WeChat Pay must use the
	// merchant's official provider, never an aggregate EasyPay/Stripe route.
	providerKey := centralPaymentProviderKey(method)
	sel, err := r.service.loadBalancer.SelectInstance(ctx, providerKey, method, payment.Strategy(cfg.LoadBalanceStrategy), float64(minor)/100)
	if err != nil {
		return bc.PaymentProviderSelection{}, err
	}
	if sel == nil {
		return bc.PaymentProviderSelection{}, errors.New("no available payment provider")
	}
	if err := validateCentralMerchantSelection(method, providerKey, sel); err != nil {
		return bc.PaymentProviderSelection{}, err
	}
	prov, err := provider.CreateProvider(sel.ProviderKey, sel.InstanceID, sel.Config)
	if err != nil {
		return bc.PaymentProviderSelection{}, err
	}
	return bc.PaymentProviderSelection{InstanceID: sel.InstanceID, Key: sel.ProviderKey, SupportedTypes: sel.SupportedTypes, Provider: prov}, nil
}

func centralPaymentProviderKey(method string) string {
	if method == payment.TypeAlipay || method == payment.TypeWxpay {
		return method
	}
	return ""
}

func validateCentralMerchantSelection(method, providerKey string, sel *payment.InstanceSelection) error {
	if sel == nil || providerKey != "" && sel.ProviderKey != providerKey {
		return errors.New("central payment did not select the required official merchant")
	}
	// Alipay app_id alone does not identify the receiving seller. New central
	// orders require the Alipay user ID so signed callbacks can check seller_id.
	if method == payment.TypeAlipay && strings.TrimSpace(sel.Config["sellerId"]) == "" {
		return errors.New("central Alipay merchant requires sellerId for callback verification")
	}
	return nil
}
func (r *centralPaymentProviders) Resolve(ctx context.Context, instanceID, providerKey string) (payment.Provider, error) {
	id, err := strconv.ParseInt(instanceID, 10, 64)
	if err != nil {
		return nil, err
	}
	instance, err := r.service.entClient.PaymentProviderInstance.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if instance.ProviderKey != providerKey {
		return nil, bc.ErrConflict
	}
	cfg, err := r.service.loadBalancer.GetInstanceConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	if instance.PaymentMode != "" {
		cfg["paymentMode"] = instance.PaymentMode
	}
	return provider.CreateProvider(providerKey, instanceID, cfg)
}
func (r *centralPaymentProviders) Adopt(ctx context.Context, order bc.PaymentOrder) (bc.PaymentJob, error) {
	if order.SourceSystem != "tabro-api" || !strings.HasPrefix(order.OrderID, "adopted-order-") || order.PaymentProducerID != r.store.ProducerID {
		return bc.PaymentJob{}, bc.ErrConflict
	}
	id, err := strconv.ParseInt(order.SourceOrderID, 10, 64)
	if err != nil {
		return bc.PaymentJob{}, err
	}
	var sourceUser int64
	var amount, payAmount, status, kind, outTradeNo, trade, instanceID, accountID, mode, providerKey, paymentType string
	err = r.db.QueryRowContext(ctx, `SELECT p.user_id,p.amount::text,p.pay_amount::text,p.status,p.order_type,p.out_trade_no,p.payment_trade_no,COALESCE(p.provider_instance_id,''),a.billing_account_id,a.billing_mode,p.payment_type FROM payment_orders p JOIN billing_center_account_routes a ON a.local_user_id=p.user_id WHERE p.id=$1`, id).Scan(&sourceUser, &amount, &payAmount, &status, &kind, &outTradeNo, &trade, &instanceID, &accountID, &mode, &paymentType)
	if err != nil {
		return bc.PaymentJob{}, err
	}
	currency := defaultPaymentCurrency
	if strings.HasPrefix(paymentType, "stripe_") {
		currency = strings.ToUpper(strings.TrimPrefix(paymentType, "stripe_"))
	}
	if order.PaymentCurrency != currency {
		return bc.PaymentJob{}, bc.ErrConflict
	}
	if accountID != order.AccountID || mode != "central" || kind != "balance" || (status != OrderStatusCompleted && status != OrderStatusPartiallyRefunded && status != OrderStatusRefunded) || !bc.PaymentAmountsEqual(bc.Decimal(amount), order.GrantAmount) || !bc.PaymentAmountsEqual(bc.Decimal(payAmount), order.PaymentAmount) || trade == "" || outTradeNo == "" || instanceID == "" {
		return bc.PaymentJob{}, bc.ErrConflict
	}
	instance, err := strconv.ParseInt(instanceID, 10, 64)
	if err != nil {
		return bc.PaymentJob{}, err
	}
	row, err := r.service.entClient.PaymentProviderInstance.Get(ctx, instance)
	if err != nil {
		return bc.PaymentJob{}, err
	}
	providerKey = row.ProviderKey
	if !row.RefundEnabled {
		return bc.PaymentJob{}, errors.New("original provider has refunds disabled")
	}
	// Imported references must remain byte-identical to the frozen source.
	// Stripe's refund adapter resolves an old cs_ reference to its pi_ only
	// when executing/querying the refund; adoption never rewrites history.
	if _, err = r.Resolve(ctx, instanceID, providerKey); err != nil {
		return bc.PaymentJob{}, err
	}
	if trade != order.ProviderTransactionID {
		return bc.PaymentJob{}, bc.ErrConflict
	}
	return r.store.AdoptOrder(ctx, bc.PaymentJob{Order: order, OutTradeNo: outTradeNo, ProviderInstanceID: instanceID, ProviderKey: providerKey, State: "paid", ProviderTransactionID: trade})
}

// Legacy callbacks after cutover acknowledge only already fulfilled history.
// A late/unfulfilled payment must be reconciled explicitly; it cannot create
// fresh local money or be inferred as a new central order.
func (s *PaymentService) legacyPaymentAuthority(ctx context.Context, userID int64) (string, error) {
	if s.financialDB == nil {
		return "local", nil
	}
	var mode string
	err := s.financialDB.QueryRowContext(ctx, `SELECT billing_mode FROM billing_center_account_routes WHERE local_user_id=$1`, userID).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "local", nil
	}
	return mode, err
}
func centralPaymentFrozenError(mode string) error {
	return fmt.Errorf("payment authority is %s; use the account center", mode)
}
