package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type GatewayBillingRoute struct {
	LocalUserID                                                        int64
	Mode, Issuer, ActorUserID, TenantID, OriginAppID, BillingAccountID string
	OwnerEpoch                                                         int64
	// DynamicAuthority means Auth selects the payer and owner epoch from a
	// verified subject proof at Quote time. It is never persisted as a route.
	DynamicAuthority bool
}
type GatewayBillingRepository interface {
	bc.OperationStore
	bc.OutboxStore
	GetBillingRoute(context.Context, int64) (*GatewayBillingRoute, error)
	GetBillingWorkspaceRoute(context.Context, string, string) (*GatewayBillingRoute, error)
	GetBillingCredential(context.Context, int64, int64) (*GatewayBillingCredential, error)
	RecordBillingShadow(context.Context, GatewayBillingRoute, *bc.Execution) error
	Enqueue(context.Context, bc.Event) (bool, error)
	MarkReconciliation(context.Context, bc.Key, int64) error
	PendingBillingShadows(context.Context, int) ([]GatewayBillingShadow, error)
	CompleteBillingShadow(context.Context, GatewayBillingShadow, bc.Estimate) error
	RecordBillingShadowFailure(context.Context, GatewayBillingRoute, bc.Key, string, string) error
	MarkBillingShadowEstimateFailure(context.Context, GatewayBillingShadow) error
}
type GatewayBillingAuthority interface {
	bc.Authority
	Quote(context.Context, bc.ProofRequest[bc.QuoteRequest]) (bc.Quote, error)
	Estimate(context.Context, bc.Key, string, map[string]bc.Decimal) (bc.Estimate, error)
	bc.EventSender
}

type GatewayBillingShadow struct {
	Key   bc.Key
	Quote bc.Quote
	Usage map[string]bc.Decimal
}
type GatewayBillingCoordinator struct {
	repo         GatewayBillingRepository
	authority    GatewayBillingAuthority
	producer     string
	pricing      *PricingService
	creditPricer gatewayCreditPricer
	settings     *SettingService
	enabled      bool
	cancel       context.CancelFunc
	done         chan struct{}
	stop         sync.Once
}

func ProvideGatewayBillingCoordinator(cfg *config.Config, repo GatewayBillingRepository, pricing *PricingService, gateway *GatewayService, settings *SettingService) (*GatewayBillingCoordinator, error) {
	s := &GatewayBillingCoordinator{repo: repo, pricing: pricing, creditPricer: &gatewayCreditPriceCalculator{gateway: gateway}, settings: settings, producer: cfg.BillingCenter.ProducerClientID, enabled: cfg.BillingCenter.Enabled}
	if !s.enabled {
		return s, nil
	}
	if err := cfg.BillingCenter.Validate(); err != nil {
		return nil, err
	}
	timeout := time.Duration(cfg.BillingCenter.TimeoutSeconds) * time.Second
	token, err := bc.NewClientCredentialsTokenSource(bc.TokenConfig{TokenURL: cfg.BillingCenter.TokenURL, ClientID: s.producer, ClientSecret: cfg.BillingCenter.ClientSecret, Scopes: []string{"billing.reserve", "billing.dispatch", "billing.extend", "billing.settle", "billing.release", "billing.read"}, Timeout: timeout, InsecureLocal: cfg.BillingCenter.InsecureLocal}, nil)
	if err != nil {
		return nil, err
	}
	client, err := bc.NewClient(bc.Config{BaseURL: cfg.BillingCenter.BaseURL, ProducerClientID: s.producer, Timeout: timeout, InsecureLocal: cfg.BillingCenter.InsecureLocal}, token, nil)
	if err != nil {
		return nil, err
	}
	s.authority = client
	worker, err := bc.NewWorker(repo, client, bc.WorkerConfig{})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		var workers sync.WaitGroup
		workers.Add(1)
		go func() { defer workers.Done(); s.runShadowEstimates(ctx) }()
		worker.Run(ctx, func(err error) { logger.LegacyPrintf("billing_center", "outbox worker error: %v", err) })
		workers.Wait()
	}()
	return s, nil
}
func (s *GatewayBillingCoordinator) Stop() {
	if s == nil {
		return
	}
	s.stop.Do(func() {
		if s.cancel != nil {
			s.cancel()
			<-s.done
		}
	})
}

func (s *GatewayBillingCoordinator) ShadowFailure(ctx context.Context, route GatewayBillingRoute, operationID, payloadHash, stage string) error {
	return s.repo.RecordBillingShadowFailure(ctx, route, bc.Key{ProducerClientID: s.producer, OriginAppID: route.OriginAppID, OperationID: operationID}, payloadHash, stage)
}

// Route is checked for every authenticated account, including when the remote
// connector is disabled; disabling configuration must never debit locally for
// an account whose ownership already moved to the center.
func (s *GatewayBillingCoordinator) Route(ctx context.Context, userID int64) (*GatewayBillingRoute, error) {
	if s == nil {
		return nil, nil
	}
	if s.repo == nil {
		return nil, bc.ErrState
	}
	route, err := s.repo.GetBillingRoute(ctx, userID)
	if err != nil {
		return nil, err
	}
	if route != nil && (route.Mode == "fenced" || route.Mode == "draining" || route.Mode == "frozen" || route.Mode == "central" && !s.enabled) {
		return nil, bc.ErrState
	}
	return route, nil
}

// Personal legacy money and team money have different ownership. A registered
// workspace always uses its explicit central account, while the verified
// subject remains the individual member performing the work.
func (s *GatewayBillingCoordinator) RouteForPrincipal(ctx context.Context, userID int64, p *GatewayOIDCPrincipal) (*GatewayBillingRoute, error) {
	if s == nil {
		if p != nil && p.AuthBilled {
			return nil, bc.ErrState
		}
		return nil, nil
	}
	policy, err := s.oidcBillingPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if policy.Enabled {
		if !s.enabled || s.repo == nil || p == nil {
			return nil, bc.ErrState
		}
		// Only a verified subject or a registered credential binding can
		// select Auth funds. Never fall back to local money in OIDC mode.
		if p.ClientID != "legacy-api-key" {
			p.AuthBilled = true
		}
	}
	if p != nil && p.AuthBilled && (!s.enabled || s.repo == nil) {
		return nil, bc.ErrState
	}
	if p != nil {
		workspace, err := s.repo.GetBillingWorkspaceRoute(ctx, p.Issuer, p.Tenant)
		if err != nil {
			return nil, err
		}
		if workspace != nil {
			if !s.enabled || workspace.Mode != "central" {
				return nil, bc.ErrState
			}
			workspace.LocalUserID = userID
			workspace.ActorUserID = p.Subject
			workspace.OriginAppID = p.ClientID
			return workspace, nil
		}
		if p.AuthBilled {
			// A pre-existing route represents an explicit financial migration.
			// Never override a local or fenced owner just because the caller has
			// a valid Auth token.
			route, routeErr := s.Route(ctx, userID)
			if routeErr != nil {
				return nil, routeErr
			}
			if route != nil {
				if route.Mode != "central" {
					return nil, bc.ErrState
				}
				if route.Issuer != p.Issuer || route.ActorUserID != p.Subject || route.TenantID != p.Tenant || route.OriginAppID != p.ClientID {
					return nil, bc.ErrConflict
				}
				return route, nil
			}
			return &GatewayBillingRoute{
				LocalUserID: userID, Mode: "central", Issuer: p.Issuer,
				ActorUserID: p.Subject, TenantID: p.Tenant,
				OriginAppID: p.ClientID, DynamicAuthority: true,
			}, nil
		}
	}
	route, err := s.Route(ctx, userID)
	if policy.Enabled && (route == nil || route.Mode != "central") {
		return nil, bc.ErrState
	}
	if route != nil && p != nil {
		route.OriginAppID = p.ClientID
	}
	return route, err
}

// Prepare receives a validated principal and transient proof directly from
// authentication. Proof is never retained on Execution, in logs or in storage.
func (s *GatewayBillingCoordinator) Prepare(ctx context.Context, route GatewayBillingRoute, principal *GatewayOIDCPrincipal, proof, path, operationID string, body []byte, credentials ...*GatewayBillingCredential) (*bc.Execution, error) {
	return s.prepareWithQuote(ctx, route, principal, proof, path, operationID, body, nil, nil, credentials...)
}

// PrepareForKey receives the authenticated key only in memory. Its group and
// user-specific rate are required to bound gateway-priced credits before work.
func (s *GatewayBillingCoordinator) PrepareForKey(ctx context.Context, route GatewayBillingRoute, principal *GatewayOIDCPrincipal, proof, path, operationID string, body []byte, apiKey *APIKey, credentials ...*GatewayBillingCredential) (*bc.Execution, error) {
	return s.prepareWithQuote(ctx, route, principal, proof, path, operationID, body, nil, apiKey, credentials...)
}

func (s *GatewayBillingCoordinator) prepareWithQuote(ctx context.Context, route GatewayBillingRoute, principal *GatewayOIDCPrincipal, proof, path, operationID string, body []byte, providedQuote *bc.QuoteRequest, apiKey *APIKey, credentials ...*GatewayBillingCredential) (*bc.Execution, error) {
	policy, err := s.oidcBillingPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if policy.Enabled && (route.Mode != "central" || s.creditPricer == nil) {
		return nil, bc.ErrState
	}
	if route.DynamicAuthority && route.Mode != "central" {
		return nil, bc.ErrConflict
	}
	if route.Mode == "local" {
		return nil, nil
	}
	if !s.enabled || s.authority == nil {
		return nil, bc.ErrState
	}
	var credential *GatewayBillingCredential
	if len(credentials) > 0 {
		credential = credentials[0]
	}
	if route.DynamicAuthority && (route.Mode != "central" || principal == nil || !principal.AuthBilled || credential != nil || route.BillingAccountID != "" || route.OwnerEpoch != 0) {
		return nil, bc.ErrConflict
	}
	if credential != nil {
		if credential.BindingID == "" || credential.Version <= 0 || credential.LocalUserID != route.LocalUserID || principal == nil || principal.ClientID != "legacy-api-key" || principal.Subject != credential.ActorUserID || principal.Tenant != credential.TenantID || principal.Issuer != credential.Issuer {
			return nil, bc.ErrConflict
		}
		proof = ""
	}
	if principal == nil || principal.Issuer != route.Issuer || principal.Subject != route.ActorUserID || principal.Tenant != route.TenantID || principal.ClientID == "" || (proof == "" && credential == nil) {
		return nil, bc.ErrConflict
	}
	route.OriginAppID = principal.ClientID
	if operationID == "" {
		operationID = uuid.NewString()
	}
	quoteReq, media, err := mediaBillingQuoteRequest(path, operationID, body)
	if providedQuote != nil {
		quoteReq, err = *providedQuote, nil
	} else if !media {
		var gemini bool
		quoteReq, gemini, err = geminiBillingQuoteRequest(path, operationID, body, s.pricing)
		if !gemini {
			quoteReq, err = textBillingQuoteRequest(path, operationID, body, s.pricing)
		}
	}
	if err != nil {
		return nil, err
	}
	businessProductKey := quoteReq.ProductKey
	businessBounds := quoteReq.MaximumUsage
	key := bc.Key{ProducerClientID: s.producer, OriginAppID: route.OriginAppID, OperationID: operationID}
	quoteReq.QuoteMode = "admission"
	if route.Mode == "shadow" {
		quoteReq.QuoteMode = "shadow"
	}
	var quote bc.Quote
	var pricingSnapshot json.RawMessage
	var previousOperation *bc.Operation
	var settlementNotBefore time.Time
	if policy.Enabled {
		settlementNotBefore, err = policy.NextSettlementAt(time.Now())
		if err != nil {
			return nil, err
		}
	}
	if route.Mode == "central" {
		previous, getErr := s.repo.Get(ctx, key)
		if getErr == nil {
			if previous.ActorUserID != route.ActorUserID || previous.TenantID != route.TenantID || previous.Mode != route.Mode || json.Unmarshal(previous.RequestPayload, &quote.Request) != nil || quote.Request.RequestPayloadHash != quoteReq.RequestPayloadHash {
				return nil, bc.ErrConflict
			}
			settlementNotBefore = previous.SettlementNotBefore
			pricingSnapshot = previous.GatewayPricingSnapshot
			previousOperation = &previous
		} else if !errors.Is(getErr, bc.ErrNotFound) {
			return nil, getErr
		}
	}
	if route.Mode == "central" && s.creditPricer != nil {
		maximum := quote.Request.MaximumUsage[gatewayCreditMeter]
		if len(pricingSnapshot) == 0 {
			priceCtx := ctx
			if policy.Enabled {
				priceCtx = context.WithValue(ctx, oidcBillingMultiplierContextKey{}, policy.RateMultiplier)
			}
			var priceErr error
			maximum, pricingSnapshot, priceErr = s.creditPricer.MaximumCredit(priceCtx, apiKey, path, quoteReq)
			if priceErr != nil {
				return nil, priceErr
			}
		}
		if quote.Request.OperationID != "" {
			reserved, parseErr := decimal.NewFromString(string(quote.Request.MaximumUsage[gatewayCreditMeter]))
			needed, neededErr := decimal.NewFromString(string(maximum))
			if parseErr != nil || neededErr != nil || needed.GreaterThan(reserved) || len(quote.Request.MaximumUsage) != 1 {
				return nil, bc.ErrConflict
			}
		}
		quoteReq.ProductKey = gatewayCreditProductKey
		quoteReq.MaximumUsage = map[string]bc.Decimal{gatewayCreditMeter: maximum}
		quoteReq.ServiceTier = "default"
	}
	if quote.Request.OperationID == "" {
		quote, err = s.authority.Quote(ctx, gatewayBillingProof(route.OriginAppID, proof, credential, quoteReq))
		if err != nil {
			return nil, err
		}
	}
	if route.DynamicAuthority {
		// Auth's Quote resolves the actual payer from the validated subject
		// proof. Freeze that returned account/epoch in the local intent and
		// subsequent Reserve; never infer a payer from token claims or email.
		if quote.Request.BillingAccountID == "" || quote.Request.OwnerEpoch < 0 {
			return nil, bc.ErrConflict
		}
		route.BillingAccountID = quote.Request.BillingAccountID
		route.OwnerEpoch = quote.Request.OwnerEpoch
	}
	if quote.Request.BillingAccountID != route.BillingAccountID || quote.Request.OwnerEpoch != route.OwnerEpoch || quote.Request.RequestPayloadHash != quoteReq.RequestPayloadHash {
		return nil, bc.ErrConflict
	}
	execution := &bc.Execution{Key: key, Mode: route.Mode, ProductKey: businessProductKey,
		SettlementNotBefore:  settlementNotBefore,
		OriginalMaximumUsage: businessBounds, GatewayPricingSnapshot: pricingSnapshot, RequestPayloadHash: quoteReq.RequestPayloadHash,
		Quote: quote, Coordinator: bc.Coordinator{Store: s.repo, Authority: s.authority}}
	if route.Mode == "shadow" {
		return execution, s.repo.RecordBillingShadow(ctx, route, execution)
	}
	if route.Mode != "central" {
		return nil, bc.ErrState
	}
	intentPricing := pricingSnapshot
	if previousOperation != nil && len(previousOperation.GatewayPricingSnapshot) == 0 {
		// Pre-upgrade operations did not include the local price in their
		// identity. Preserve that fingerprint when recovering a reservation.
		intentPricing = nil
	}
	intent, err := bc.NewScheduledIntent(key, route.ActorUserID, route.TenantID, route.Mode, quote.Request, settlementNotBefore, intentPricing)
	if err != nil {
		return nil, err
	}
	op, err := execution.Coordinator.Reserve(ctx, intent, gatewayBillingProof(route.OriginAppID, proof, credential, quote.Request))
	if err != nil {
		return nil, err
	}
	if op.State != bc.Reserved {
		return nil, bc.ErrState
	}
	execution.SettlementNotBefore = op.SettlementNotBefore
	return execution, nil
}

// Finish retains uncertain work for reconciliation. A release is only queued
// when the local durable state proves dispatch never started.
func (s *GatewayBillingCoordinator) Finish(ctx context.Context, e *bc.Execution) error {
	if e == nil || e.Mode != "central" {
		return nil
	}
	if e.HandedOff() {
		return nil
	}
	op, err := s.repo.Get(ctx, e.Key)
	if err != nil {
		return err
	}
	switch op.State {
	case bc.Reserved:
		event, err := bc.NewRelease(e.Key, op.ReservationID, bc.ReleaseRequest{TransitionRequest: bc.TransitionRequest{EventID: "release:" + e.Key.OperationID, ExpectedVersion: op.RemoteVersion}, Reason: "request_ended_before_dispatch", ConfirmedNoBillableWork: true})
		if err != nil {
			return err
		}
		_, err = s.repo.Enqueue(ctx, event)
		return err
	case bc.Dispatching, bc.Dispatched:
		if !e.DispatchGranted() {
			return nil
		}
		return s.repo.MarkReconciliation(ctx, e.Key, op.Version)
	default:
		return nil
	}
}

// Restore reconstructs a worker's non-secret execution identity from the
// durable job snapshot and verifies it against the original operation.
func (s *GatewayBillingCoordinator) Restore(ctx context.Context, snapshot *bc.ExecutionSnapshot) (*bc.Execution, error) {
	if s == nil || snapshot == nil || snapshot.Key.ProducerClientID != s.producer {
		return nil, bc.ErrConflict
	}
	if snapshot.Mode == "central" {
		op, err := s.repo.Get(ctx, snapshot.Key)
		if err != nil {
			return nil, err
		}
		var request bc.ReserveRequest
		if json.Unmarshal(op.RequestPayload, &request) != nil || op.Mode != "central" || request.PriceVersionID != snapshot.Quote.Request.PriceVersionID || request.RequestPayloadHash != snapshot.RequestPayloadHash {
			return nil, bc.ErrConflict
		}
		if !op.SettlementNotBefore.Equal(snapshot.SettlementNotBefore) {
			return nil, bc.ErrConflict
		}
		if len(op.GatewayPricingSnapshot) > 0 {
			storedPrice, storedErr := bc.Fingerprint(op.GatewayPricingSnapshot)
			jobPrice, jobErr := bc.Fingerprint(snapshot.GatewayPricingSnapshot)
			if storedErr != nil || jobErr != nil || storedPrice != jobPrice {
				return nil, bc.ErrConflict
			}
		}
	} else if snapshot.Mode != "shadow" {
		return nil, bc.ErrConflict
	}
	return &bc.Execution{Key: snapshot.Key, Mode: snapshot.Mode, ProductKey: snapshot.ProductKey,
		SettlementNotBefore:  snapshot.SettlementNotBefore,
		OriginalMaximumUsage: snapshot.OriginalMaximumUsage, GatewayPricingSnapshot: snapshot.GatewayPricingSnapshot, RequestPayloadHash: snapshot.RequestPayloadHash,
		Quote: snapshot.Quote, Coordinator: bc.Coordinator{Store: s.repo, Authority: s.authority}}, nil
}

func (s *GatewayBillingCoordinator) oidcBillingPolicy(ctx context.Context) (OIDCBillingPolicy, error) {
	if s.settings == nil {
		return OIDCBillingPolicy{}, nil
	}
	return s.settings.GetOIDCBillingPolicy(ctx)
}
