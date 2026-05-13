package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	stripe "github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/webhook"
)

// Stripe constants.
const (
	stripeDefaultCurrency          = "cny"
	stripeCheckoutSessionPrefix    = "cs_"
	stripePaymentIntentPrefix      = "pi_"
	stripeEventCheckoutCompleted   = "checkout.session.completed"
	stripeEventCheckoutAsyncPaid   = "checkout.session.async_payment_succeeded"
	stripeEventCheckoutAsyncFailed = "checkout.session.async_payment_failed"
	stripeEventPaymentSuccess      = "payment_intent.succeeded"
	stripeEventPaymentFailed       = "payment_intent.payment_failed"
)

// Stripe implements the payment.CancelableProvider interface for Stripe payments.
type Stripe struct {
	instanceID string
	config     map[string]string

	mu          sync.Mutex
	initialized bool
	sc          *stripe.Client
}

// NewStripe creates a new Stripe provider instance.
func NewStripe(instanceID string, config map[string]string) (*Stripe, error) {
	if config["secretKey"] == "" {
		return nil, fmt.Errorf("stripe config missing required key: secretKey")
	}
	return &Stripe{
		instanceID: instanceID,
		config:     config,
	}, nil
}

func (s *Stripe) ensureInit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initialized {
		s.sc = stripe.NewClient(s.config["secretKey"])
		s.initialized = true
	}
}

// GetPublishableKey returns the publishable key for frontend use.
func (s *Stripe) GetPublishableKey() string {
	return s.config["publishableKey"]
}

func (s *Stripe) Name() string        { return "Stripe" }
func (s *Stripe) ProviderKey() string { return payment.TypeStripe }
func (s *Stripe) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeStripe}
}

// stripePaymentMethodTypes maps our PaymentType to Stripe payment_method_types.
var stripePaymentMethodTypes = map[string][]string{
	payment.TypeCard:   {"card"},
	payment.TypeAlipay: {"alipay"},
	payment.TypeWxpay:  {"wechat_pay"},
	payment.TypeLink:   {"link"},
}

// CreatePayment creates a Stripe Checkout Session and returns its hosted payment URL.
func (s *Stripe) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	s.ensureInit()

	if strings.TrimSpace(req.ReturnURL) == "" {
		return nil, fmt.Errorf("stripe create payment: return url is required")
	}

	cancelURL := strings.TrimSpace(req.CancelURL)
	if cancelURL == "" {
		cancelURL = req.ReturnURL
	}

	amountInMinorUnits, err := payment.DecimalAmountToMinorUnits(req.Amount)
	if err != nil {
		return nil, fmt.Errorf("stripe create payment: %w", err)
	}
	currency := resolveStripeCurrency(req.Currency)
	metadataCurrency := strings.ToUpper(currency)

	methods := resolveStripeMethodTypes(req.InstanceSubMethods)
	pmTypes := make([]*string, len(methods))
	for i, m := range methods {
		pmTypes[i] = stripe.String(m)
	}

	params := &stripe.CheckoutSessionCreateParams{
		SuccessURL:         stripe.String(req.ReturnURL),
		CancelURL:          stripe.String(cancelURL),
		ClientReferenceID:  stripe.String(req.OrderID),
		Mode:               stripe.String(string(stripe.CheckoutSessionModePayment)),
		PaymentMethodTypes: pmTypes,
		Metadata: map[string]string{
			"orderId":  req.OrderID,
			"currency": metadataCurrency,
		},
		PaymentIntentData: &stripe.CheckoutSessionCreatePaymentIntentDataParams{
			Description: stripe.String(req.Subject),
			Metadata: map[string]string{
				"orderId":  req.OrderID,
				"currency": metadataCurrency,
			},
		},
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{
			{
				Quantity: stripe.Int64(1),
				PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
					Currency:   stripe.String(currency),
					UnitAmount: stripe.Int64(amountInMinorUnits),
					ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{
						Name: stripe.String(req.Subject),
					},
				},
			},
		},
	}

	if hasStripeMethod(methods, "wechat_pay") {
		params.PaymentMethodOptions = &stripe.CheckoutSessionCreatePaymentMethodOptionsParams{
			WeChatPay: &stripe.CheckoutSessionCreatePaymentMethodOptionsWeChatPayParams{
				Client: stripe.String("web"),
			},
		}
	}

	params.SetIdempotencyKey(fmt.Sprintf("cs-%s", req.OrderID))

	session, err := s.sc.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("stripe create payment: %w", err)
	}
	if strings.TrimSpace(session.URL) == "" {
		return nil, fmt.Errorf("stripe create payment: checkout session missing url")
	}

	return &payment.CreatePaymentResponse{
		TradeNo: session.ID,
		PayURL:  session.URL,
	}, nil
}

func resolveStripeCurrency(currency string) string {
	switch strings.ToLower(strings.TrimSpace(currency)) {
	case "usd":
		return "usd"
	case "gbp":
		return "gbp"
	case "eur":
		return "eur"
	case "cny":
		return "cny"
	default:
		return stripeDefaultCurrency
	}
}

// QueryOrder retrieves the upstream status for either a Checkout Session or a legacy PaymentIntent.
func (s *Stripe) QueryOrder(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	s.ensureInit()

	if isStripePaymentIntentID(tradeNo) {
		return s.queryPaymentIntent(ctx, tradeNo)
	}
	return s.queryCheckoutSession(ctx, tradeNo)
}

func (s *Stripe) queryCheckoutSession(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	session, err := s.getCheckoutSession(ctx, tradeNo, false)
	if err != nil {
		return nil, fmt.Errorf("stripe query order: %w", err)
	}

	status := payment.ProviderStatusPending
	switch {
	case session.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid:
		status = payment.ProviderStatusPaid
	case session.Status == stripe.CheckoutSessionStatusExpired:
		status = payment.ProviderStatusFailed
	}

	return &payment.QueryOrderResponse{
		TradeNo:  session.ID,
		Status:   status,
		Amount:   payment.MinorUnitsToDecimal(session.AmountTotal),
		Currency: strings.ToUpper(string(session.Currency)),
	}, nil
}

func (s *Stripe) queryPaymentIntent(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	pi, err := s.sc.V1PaymentIntents.Retrieve(ctx, tradeNo, nil)
	if err != nil {
		return nil, fmt.Errorf("stripe query order: %w", err)
	}

	return &payment.QueryOrderResponse{
		TradeNo:  pi.ID,
		Status:   mapStripePaymentIntentStatus(pi.Status),
		Amount:   payment.MinorUnitsToDecimal(pi.Amount),
		Currency: strings.ToUpper(string(pi.Currency)),
	}, nil
}

// VerifyNotification verifies a Stripe webhook event.
func (s *Stripe) VerifyNotification(_ context.Context, rawBody string, headers map[string]string) (*payment.PaymentNotification, error) {
	s.ensureInit()

	webhookSecret := s.config["webhookSecret"]
	if webhookSecret == "" {
		return nil, fmt.Errorf("stripe webhookSecret not configured")
	}

	sig := headers["stripe-signature"]
	if sig == "" {
		return nil, fmt.Errorf("stripe notification missing stripe-signature header")
	}

	event, err := webhook.ConstructEventWithOptions(
		[]byte(rawBody),
		sig,
		webhookSecret,
		webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true},
	)
	if err != nil {
		return nil, fmt.Errorf("stripe verify notification: %w", err)
	}

	switch event.Type {
	case stripeEventCheckoutCompleted:
		return parseStripeCheckoutSession(&event, rawBody)
	case stripeEventCheckoutAsyncPaid:
		return parseStripeCheckoutSessionWithStatus(&event, payment.ProviderStatusSuccess, rawBody)
	case stripeEventCheckoutAsyncFailed:
		return parseStripeCheckoutSessionWithStatus(&event, payment.ProviderStatusFailed, rawBody)
	case stripeEventPaymentSuccess:
		return parseStripePaymentIntent(&event, payment.ProviderStatusSuccess, rawBody)
	case stripeEventPaymentFailed:
		return parseStripePaymentIntent(&event, payment.ProviderStatusFailed, rawBody)
	}

	return nil, nil
}

func parseStripeCheckoutSession(event *stripe.Event, rawBody string) (*payment.PaymentNotification, error) {
	var session stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
		return nil, fmt.Errorf("stripe parse checkout.session: %w", err)
	}
	if session.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
		return nil, nil
	}
	return stripeCheckoutSessionNotification(&session, payment.ProviderStatusSuccess, rawBody), nil
}

func parseStripeCheckoutSessionWithStatus(event *stripe.Event, status string, rawBody string) (*payment.PaymentNotification, error) {
	var session stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
		return nil, fmt.Errorf("stripe parse checkout.session: %w", err)
	}
	return stripeCheckoutSessionNotification(&session, status, rawBody), nil
}

func stripeCheckoutSessionNotification(session *stripe.CheckoutSession, status string, rawBody string) *payment.PaymentNotification {
	orderID := session.Metadata["orderId"]
	if orderID == "" {
		orderID = session.ClientReferenceID
	}

	return &payment.PaymentNotification{
		TradeNo:  session.ID,
		OrderID:  orderID,
		Amount:   payment.MinorUnitsToDecimal(session.AmountTotal),
		Currency: strings.ToUpper(string(session.Currency)),
		Status:   status,
		RawData:  rawBody,
	}
}

func parseStripePaymentIntent(event *stripe.Event, status string, rawBody string) (*payment.PaymentNotification, error) {
	var pi stripe.PaymentIntent
	if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
		return nil, fmt.Errorf("stripe parse payment_intent: %w", err)
	}
	return &payment.PaymentNotification{
		TradeNo:  pi.ID,
		OrderID:  pi.Metadata["orderId"],
		Amount:   payment.MinorUnitsToDecimal(pi.Amount),
		Currency: strings.ToUpper(string(pi.Currency)),
		Status:   status,
		RawData:  rawBody,
	}, nil
}

// Refund creates a Stripe refund.
func (s *Stripe) Refund(ctx context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	s.ensureInit()

	amountInCents, err := payment.YuanToFen(req.Amount)
	if err != nil {
		return nil, fmt.Errorf("stripe refund: %w", err)
	}

	paymentIntentID, err := s.resolveRefundPaymentIntent(ctx, req.TradeNo)
	if err != nil {
		return nil, fmt.Errorf("stripe refund: %w", err)
	}

	params := &stripe.RefundCreateParams{
		PaymentIntent: stripe.String(paymentIntentID),
		Amount:        stripe.Int64(amountInCents),
		Reason:        stripe.String(string(stripe.RefundReasonRequestedByCustomer)),
	}

	refund, err := s.sc.V1Refunds.Create(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("stripe refund: %w", err)
	}

	refundStatus := payment.ProviderStatusPending
	if refund.Status == stripe.RefundStatusSucceeded {
		refundStatus = payment.ProviderStatusSuccess
	}

	return &payment.RefundResponse{
		RefundID: refund.ID,
		Status:   refundStatus,
	}, nil
}

func (s *Stripe) resolveRefundPaymentIntent(ctx context.Context, tradeNo string) (string, error) {
	if isStripePaymentIntentID(tradeNo) {
		return tradeNo, nil
	}

	session, err := s.getCheckoutSession(ctx, tradeNo, true)
	if err != nil {
		return "", fmt.Errorf("retrieve checkout session: %w", err)
	}
	if session.PaymentIntent == nil || strings.TrimSpace(session.PaymentIntent.ID) == "" {
		return "", fmt.Errorf("checkout session %s missing payment_intent", tradeNo)
	}
	return session.PaymentIntent.ID, nil
}

func (s *Stripe) getCheckoutSession(ctx context.Context, tradeNo string, expandPaymentIntent bool) (*stripe.CheckoutSession, error) {
	params := &stripe.CheckoutSessionRetrieveParams{}
	if expandPaymentIntent {
		params.AddExpand("payment_intent")
	}
	session, err := s.sc.V1CheckoutSessions.Retrieve(ctx, tradeNo, params)
	if err != nil {
		return nil, err
	}
	return session, nil
}

// resolveStripeMethodTypes converts instance supported_types (comma-separated)
// into Stripe API payment_method_types. Falls back to ["card"] if empty.
func resolveStripeMethodTypes(instanceSubMethods string) []string {
	if instanceSubMethods == "" {
		return []string{"card"}
	}
	var methods []string
	for _, t := range strings.Split(instanceSubMethods, ",") {
		t = strings.TrimSpace(t)
		if mapped, ok := stripePaymentMethodTypes[t]; ok {
			methods = append(methods, mapped...)
		}
	}
	if len(methods) == 0 {
		return []string{"card"}
	}
	return methods
}

// hasStripeMethod checks if the given Stripe method list contains the target method.
func hasStripeMethod(methods []string, target string) bool {
	for _, m := range methods {
		if m == target {
			return true
		}
	}
	return false
}

func mapStripePaymentIntentStatus(status stripe.PaymentIntentStatus) string {
	switch status {
	case stripe.PaymentIntentStatusSucceeded:
		return payment.ProviderStatusPaid
	case stripe.PaymentIntentStatusCanceled:
		return payment.ProviderStatusFailed
	default:
		return payment.ProviderStatusPending
	}
}

func isStripePaymentIntentID(tradeNo string) bool {
	return strings.HasPrefix(strings.TrimSpace(tradeNo), stripePaymentIntentPrefix)
}

// CancelPayment expires a pending Checkout Session or cancels a legacy PaymentIntent.
func (s *Stripe) CancelPayment(ctx context.Context, tradeNo string) error {
	s.ensureInit()

	var err error
	if isStripePaymentIntentID(tradeNo) {
		_, err = s.sc.V1PaymentIntents.Cancel(ctx, tradeNo, nil)
	} else {
		_, err = s.sc.V1CheckoutSessions.Expire(ctx, tradeNo, &stripe.CheckoutSessionExpireParams{})
	}
	if err != nil {
		return fmt.Errorf("stripe cancel payment: %w", err)
	}
	return nil
}

// Ensure interface compliance.
var (
	_ payment.Provider           = (*Stripe)(nil)
	_ payment.CancelableProvider = (*Stripe)(nil)
)
