package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
)

// --- Order Status Constants ---

const (
	OrderStatusPending           = payment.OrderStatusPending
	OrderStatusPaid              = payment.OrderStatusPaid
	OrderStatusRecharging        = payment.OrderStatusRecharging
	OrderStatusCompleted         = payment.OrderStatusCompleted
	OrderStatusExpired           = payment.OrderStatusExpired
	OrderStatusCancelled         = payment.OrderStatusCancelled
	OrderStatusFailed            = payment.OrderStatusFailed
	OrderStatusRefundRequested   = payment.OrderStatusRefundRequested
	OrderStatusRefunding         = payment.OrderStatusRefunding
	OrderStatusPartiallyRefunded = payment.OrderStatusPartiallyRefunded
	OrderStatusRefunded          = payment.OrderStatusRefunded
	OrderStatusRefundFailed      = payment.OrderStatusRefundFailed
)

const (
	// defaultMaxPendingOrders and defaultOrderTimeoutMin are defined in
	// payment_config_service.go alongside other payment configuration defaults.
	paymentGraceMinutes = 5

	defaultPageSize    = 20
	maxPageSize        = 100
	topUsersLimit      = 10
	amountToleranceCNY = 0.01

	orderIDPrefix = "sub2_"
)

// --- Types ---

// generateOutTradeNo creates a unique external order ID for payment providers.
// Format: sub2_20250409aB3kX9mQ (prefix + date + 8-char random)
func generateOutTradeNo() string {
	date := time.Now().Format("20060102")
	rnd := generateRandomString(8)
	return orderIDPrefix + date + rnd
}

func generateRandomString(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[rand.IntN(len(charset))]
	}
	return string(b)
}

type CreateOrderRequest struct {
	UserID      int64
	Amount      float64
	PaymentType string
	Currency    string
	ClientIP    string
	IsMobile    bool
	SrcHost     string
	SrcURL      string
	OrderType   string
	PlanID      int64
}

type CreateOrderResponse struct {
	OrderID      int64     `json:"order_id"`
	Amount       float64   `json:"amount"`
	PayAmount    float64   `json:"pay_amount"`
	FeeRate      float64   `json:"fee_rate"`
	Status       string    `json:"status"`
	PaymentType  string    `json:"payment_type"`
	PayURL       string    `json:"pay_url,omitempty"`
	QRCode       string    `json:"qr_code,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	PaymentMode  string    `json:"payment_mode,omitempty"`
	Currency     string    `json:"currency,omitempty"`
}

type OrderListParams struct {
	Page        int
	PageSize    int
	Status      string
	OrderType   string
	PaymentType string
	Keyword     string
}

type RefundPlan struct {
	OrderID         int64
	Order           *dbent.PaymentOrder
	RefundAmount    float64
	GatewayAmount   float64
	Reason          string
	Force           bool
	DeductBalance   bool
	DeductionType   string
	BalanceToDeduct float64
	SubDaysToDeduct int
	SubscriptionID  int64
}

type RefundResult struct {
	Success         bool    `json:"success"`
	Warning         string  `json:"warning,omitempty"`
	RequireForce    bool    `json:"require_force,omitempty"`
	BalanceDeducted float64 `json:"balance_deducted,omitempty"`
	SubDaysDeducted int     `json:"subscription_days_deducted,omitempty"`
}

type DashboardStats struct {
	TodayAmount   float64 `json:"today_amount"`
	TotalAmount   float64 `json:"total_amount"`
	TodayCount    int     `json:"today_count"`
	TotalCount    int     `json:"total_count"`
	AvgAmount     float64 `json:"avg_amount"`
	PendingOrders int     `json:"pending_orders"`

	DailySeries    []DailyStats        `json:"daily_series"`
	PaymentMethods []PaymentMethodStat `json:"payment_methods"`
	TopUsers       []TopUserStat       `json:"top_users"`
}

type DailyStats struct {
	Date   string  `json:"date"`
	Amount float64 `json:"amount"`
	Count  int     `json:"count"`
}

type PaymentMethodStat struct {
	Type   string  `json:"type"`
	Amount float64 `json:"amount"`
	Count  int     `json:"count"`
}

type TopUserStat struct {
	UserID int64   `json:"user_id"`
	Email  string  `json:"email"`
	Amount float64 `json:"amount"`
}

// --- Service ---

type PaymentService struct {
	financialDB        *sql.DB
	centerPayments     *bc.PaymentBridge
	stopCenterPayments func()
	centerStopOnce     sync.Once
	providerMu         sync.Mutex
	providersLoaded    bool
	entClient          *dbent.Client
	registry           *payment.Registry
	loadBalancer       payment.LoadBalancer
	redeemService      *RedeemService
	subscriptionSvc    *SubscriptionService
	configService      *PaymentConfigService
	userRepo           UserRepository
	groupRepo          GroupRepository
}

func NewPaymentService(entClient *dbent.Client, registry *payment.Registry, loadBalancer payment.LoadBalancer, redeemService *RedeemService, subscriptionSvc *SubscriptionService, configService *PaymentConfigService, userRepo UserRepository, groupRepo GroupRepository, db *sql.DB, cfg *config.Config) (*PaymentService, error) {
	s := &PaymentService{entClient: entClient, registry: registry, loadBalancer: loadBalancer, redeemService: redeemService, subscriptionSvc: subscriptionSvc, configService: configService, userRepo: userRepo, groupRepo: groupRepo, financialDB: db}
	cleanup, err := s.startBillingPaymentBridge(db, cfg)
	if err != nil {
		return nil, err
	}
	s.stopCenterPayments = cleanup
	return s, nil
}

func (s *PaymentService) Stop() {
	if s != nil {
		s.centerStopOnce.Do(func() {
			if s.stopCenterPayments != nil {
				s.stopCenterPayments()
			}
		})
	}
}

// --- Provider Registry ---

// EnsureProviders lazily initializes the provider registry on first call.
func (s *PaymentService) EnsureProviders(ctx context.Context) {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	if !s.providersLoaded {
		s.loadProviders(ctx)
		s.providersLoaded = true
	}
}

// RefreshProviders clears and re-registers all providers from the database.
func (s *PaymentService) RefreshProviders(ctx context.Context) {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	s.registry.Clear()
	s.loadProviders(ctx)
	s.providersLoaded = true
}

func (s *PaymentService) loadProviders(ctx context.Context) {
	instances, err := s.entClient.PaymentProviderInstance.Query().
		Where(paymentproviderinstance.EnabledEQ(true)).
		All(ctx)
	if err != nil {
		slog.Error("[PaymentService] failed to query provider instances", "error", err)
		return
	}
	for _, inst := range instances {
		cfg, err := s.loadBalancer.GetInstanceConfig(ctx, int64(inst.ID))
		if err != nil {
			slog.Warn("[PaymentService] failed to decrypt config for instance", "instanceID", inst.ID, "error", err)
			continue
		}
		if inst.PaymentMode != "" {
			cfg["paymentMode"] = inst.PaymentMode
		}
		instID := fmt.Sprintf("%d", inst.ID)
		p, err := provider.CreateProvider(inst.ProviderKey, instID, cfg)
		if err != nil {
			slog.Warn("[PaymentService] failed to create provider for instance", "instanceID", inst.ID, "key", inst.ProviderKey, "error", err)
			continue
		}
		s.registry.Register(p)
	}
}

// GetWebhookProvider resolves a clear-text order reference to its original
// merchant credentials. WeChat notifications have an encrypted order reference
// and must use GetWxpayWebhookProviders instead.
func (s *PaymentService) GetWebhookProvider(ctx context.Context, providerKey, outTradeNo string) (payment.Provider, error) {
	if providerKey == payment.TypeAlipay && outTradeNo == "" {
		return nil, fmt.Errorf("alipay webhook has no out_trade_no")
	}
	if providerKey == payment.TypeWxpay && outTradeNo == "" {
		return nil, fmt.Errorf("wxpay webhook requires verification against configured merchant instances")
	}
	if strings.HasPrefix(outTradeNo, "tbc_") {
		if s.centerPayments == nil {
			return nil, bc.ErrState
		}
		job, err := s.centerPayments.Store.OrderByExternalID(ctx, outTradeNo)
		if err != nil {
			return nil, err
		}
		if job.ProviderKey != providerKey {
			return nil, bc.ErrConflict
		}
		return s.centerPayments.Providers.Resolve(ctx, job.ProviderInstanceID, job.ProviderKey)
	}
	if outTradeNo != "" {
		order, err := s.entClient.PaymentOrder.Query().Where(paymentorder.OutTradeNo(outTradeNo)).Only(ctx)
		if err != nil && strings.HasPrefix(outTradeNo, orderIDPrefix) {
			if id, parseErr := strconv.ParseInt(strings.TrimPrefix(outTradeNo, orderIDPrefix), 10, 64); parseErr == nil {
				order, err = s.entClient.PaymentOrder.Get(ctx, id)
			}
		}
		if err == nil {
			if payment.GetBasePaymentType(order.PaymentType) != providerKey {
				return nil, fmt.Errorf("webhook provider does not match the order")
			}
			if order.ProviderInstanceID == nil || *order.ProviderInstanceID == "" {
				// Very old local orders did not retain a provider instance ID.
				// They can be verified only when there is exactly one configured
				// merchant of this provider type.
				return s.singleWebhookProvider(ctx, providerKey)
			}
			id, parseErr := strconv.ParseInt(*order.ProviderInstanceID, 10, 64)
			if parseErr != nil {
				return nil, parseErr
			}
			instance, getErr := s.entClient.PaymentProviderInstance.Get(ctx, id)
			if getErr != nil || instance.ProviderKey != providerKey {
				return nil, fmt.Errorf("webhook provider instance does not match the order")
			}
			cfg, configErr := s.loadBalancer.GetInstanceConfig(ctx, id)
			if configErr != nil {
				return nil, configErr
			}
			return provider.CreateProvider(providerKey, *order.ProviderInstanceID, cfg)
		}
		if providerKey == payment.TypeAlipay || providerKey == payment.TypeWxpay {
			return nil, fmt.Errorf("webhook order not found: %w", err)
		}
	}
	s.EnsureProviders(ctx)
	return s.registry.GetProviderByKey(providerKey)
}

func (s *PaymentService) singleWebhookProvider(ctx context.Context, providerKey string) (payment.Provider, error) {
	instances, err := s.entClient.PaymentProviderInstance.Query().
		Where(paymentproviderinstance.ProviderKeyEQ(providerKey)).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(instances) != 1 {
		return nil, fmt.Errorf("legacy webhook without merchant instance is ambiguous")
	}
	id := int64(instances[0].ID)
	cfg, err := s.loadBalancer.GetInstanceConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	return provider.CreateProvider(providerKey, fmt.Sprintf("%d", id), cfg)
}

// WebhookProviderCandidate carries credentials without trusting any field in
// the encrypted WeChat notification. Disabled instances remain eligible so
// payments created before disabling a merchant can still settle.
type WebhookProviderCandidate struct {
	InstanceID string
	Provider   payment.Provider
}

func (s *PaymentService) GetWxpayWebhookProviders(ctx context.Context) ([]WebhookProviderCandidate, error) {
	instances, err := s.entClient.PaymentProviderInstance.Query().
		Where(paymentproviderinstance.ProviderKeyEQ(payment.TypeWxpay)).All(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]WebhookProviderCandidate, 0, len(instances))
	for _, instance := range instances {
		cfg, err := s.loadBalancer.GetInstanceConfig(ctx, int64(instance.ID))
		if err != nil {
			slog.Warn("[Webhook] cannot load wxpay merchant credentials", "instanceID", instance.ID, "error", err)
			continue
		}
		id := fmt.Sprintf("%d", instance.ID)
		p, err := provider.CreateProvider(payment.TypeWxpay, id, cfg)
		if err != nil {
			slog.Warn("[Webhook] cannot create wxpay merchant verifier", "instanceID", instance.ID, "error", err)
			continue
		}
		candidates = append(candidates, WebhookProviderCandidate{InstanceID: id, Provider: p})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no configured wxpay merchant can verify the notification")
	}
	return candidates, nil
}

// MatchWebhookProviderInstance checks the order's frozen merchant after the
// provider signature and encrypted resource have been verified.
func (s *PaymentService) MatchWebhookProviderInstance(ctx context.Context, providerKey, outTradeNo, instanceID string) error {
	if outTradeNo == "" || instanceID == "" {
		return fmt.Errorf("webhook order or merchant instance is missing")
	}
	if strings.HasPrefix(outTradeNo, "tbc_") {
		if s.centerPayments == nil {
			return bc.ErrState
		}
		job, err := s.centerPayments.Store.OrderByExternalID(ctx, outTradeNo)
		if err != nil {
			return err
		}
		if job.ProviderKey != providerKey || job.ProviderInstanceID != instanceID {
			return bc.ErrConflict
		}
		return nil
	}
	order, err := s.entClient.PaymentOrder.Query().Where(paymentorder.OutTradeNo(outTradeNo)).Only(ctx)
	if err != nil && strings.HasPrefix(outTradeNo, orderIDPrefix) {
		if id, parseErr := strconv.ParseInt(strings.TrimPrefix(outTradeNo, orderIDPrefix), 10, 64); parseErr == nil {
			order, err = s.entClient.PaymentOrder.Get(ctx, id)
		}
	}
	if err != nil {
		return err
	}
	if payment.GetBasePaymentType(order.PaymentType) != providerKey {
		return fmt.Errorf("webhook provider type does not match the order")
	}
	if order.ProviderInstanceID == nil || *order.ProviderInstanceID == "" {
		instances, err := s.entClient.PaymentProviderInstance.Query().
			Where(paymentproviderinstance.ProviderKeyEQ(providerKey)).All(ctx)
		if err == nil && len(instances) == 1 && fmt.Sprintf("%d", instances[0].ID) == instanceID {
			return nil
		}
		return fmt.Errorf("legacy webhook without merchant instance is ambiguous")
	}
	if *order.ProviderInstanceID != instanceID {
		return fmt.Errorf("webhook merchant instance does not match the order")
	}
	return nil
}

// --- Helpers ---

func psIsRefundStatus(s string) bool {
	switch s {
	case OrderStatusRefundRequested, OrderStatusRefunding, OrderStatusPartiallyRefunded, OrderStatusRefunded, OrderStatusRefundFailed:
		return true
	}
	return false
}

func psErrMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func psNilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func psSliceContains(sl []string, s string) bool {
	for _, v := range sl {
		if v == s {
			return true
		}
	}
	return false
}

// Subscription validity period unit constants.
const (
	validityUnitWeek  = "week"
	validityUnitMonth = "month"
)

func psComputeValidityDays(days int, unit string) int {
	switch unit {
	case validityUnitWeek:
		return days * 7
	case validityUnitMonth:
		return days * 30
	default:
		return days
	}
}

func psStartOfDayUTC(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func applyPagination(pageSize, page int) (size, pg int) {
	size = pageSize
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	pg = page
	if pg < 1 {
		pg = 1
	}
	return size, pg
}
