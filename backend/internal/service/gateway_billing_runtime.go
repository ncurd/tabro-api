package service

import (
	"context"
	"errors"
	"sync"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// These capabilities are optional so existing isolated coordinator fixtures
// continue to use their own immutable repositories.
type billingProducerRepository interface {
	WithBillingProducer(string) GatewayBillingRepository
}

type pendingBillingRepository interface {
	HasPendingBillingOperations(context.Context) (bool, error)
}

type gatewayBillingRuntime struct {
	mu         sync.Mutex
	admission  sync.RWMutex
	base       config.Config
	repo       GatewayBillingRepository
	pricing    *PricingService
	gateway    *GatewayService
	settings   *SettingService
	connection OIDCBillingConnectionConfig
	child      *GatewayBillingCoordinator
	cancel     context.CancelFunc
	done       chan struct{}
	closed     bool
}

func provideGatewayBillingRuntime(cfg *config.Config, repo GatewayBillingRepository, pricing *PricingService, gateway *GatewayService, settings *SettingService) (*GatewayBillingCoordinator, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if settings == nil {
		return newGatewayBillingCoordinator(cfg, repo, pricing, gateway, nil)
	}
	r := &gatewayBillingRuntime{base: *cfg, repo: repo, pricing: pricing, gateway: gateway, settings: settings, done: make(chan struct{})}
	if _, err := r.refreshLocked(context.Background()); err != nil {
		return nil, err
	}
	outer := &GatewayBillingCoordinator{repo: repo, settings: settings, pricing: pricing, creditPricer: &gatewayCreditPriceCalculator{gateway: gateway}, runtime: r}
	settings.SetOIDCBillingConnectionValidator(r.validateConnectionChange)
	settings.SetOIDCBillingConnectionUpdateGuard(func() func() {
		r.admission.Lock()
		return r.admission.Unlock
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
				_, err := outer.current(probeCtx)
				probeCancel()
				if err != nil && ctx.Err() == nil {
					logger.LegacyPrintf("billing_center", "runtime configuration unavailable")
				}
			}
		}
	}()
	return outer, nil
}

func (s *GatewayBillingCoordinator) current(ctx context.Context) (*GatewayBillingCoordinator, error) {
	if s == nil {
		return nil, bc.ErrState
	}
	if s.runtime == nil {
		return s, nil
	}
	r := s.runtime
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refreshLocked(ctx)
}

// Callers hold mu. A settings read failure never falls back to a stale enabled
// connector or to local charging.
func (r *gatewayBillingRuntime) refreshLocked(ctx context.Context) (*GatewayBillingCoordinator, error) {
	if r.closed {
		return nil, bc.ErrState
	}
	connection, err := r.settings.GetOIDCBillingConnectionConfig(ctx)
	if err != nil {
		return nil, err
	}
	if r.child != nil && connection == r.connection {
		return r.child, nil
	}
	cfg := r.base
	cfg.BillingCenter = connection.BillingCenter
	cfg.Gateway.ResourceServer = connection.ResourceServer
	repo := r.repo
	if scoped, ok := repo.(billingProducerRepository); ok {
		repo = scoped.WithBillingProducer(connection.BillingCenter.ProducerClientID)
	}
	child, err := newGatewayBillingCoordinator(&cfg, repo, r.pricing, r.gateway, r.settings)
	if err != nil {
		return nil, err
	}
	child.owner = r
	child.connection = connection
	previous := r.child
	r.connection, r.child = connection, child
	if previous != nil {
		previous.Stop()
	}
	return child, nil
}

func sameBillingAuthority(a, b OIDCBillingConnectionConfig) bool {
	return a.BillingCenter.ProducerClientID == b.BillingCenter.ProducerClientID &&
		a.BillingCenter.BaseURL == b.BillingCenter.BaseURL &&
		a.ResourceServer.IssuerURL == b.ResourceServer.IssuerURL
}

// Called under the update guard, which remains held through the settings commit.
func (r *gatewayBillingRuntime) validateConnectionChange(ctx context.Context, old, next OIDCBillingConnectionConfig) error {
	identityChanged := !sameBillingAuthority(old, next)
	if old.BillingCenter.Enabled && next.BillingCenter.Enabled && identityChanged {
		return infraerrors.BadRequest("OIDC_BILLING_AUTHORITY_CHANGE_REQUIRES_DISABLE", "Disable OIDC billing and the billing connector before changing its producer, authority URL or issuer")
	}
	if !identityChanged && !(old.BillingCenter.Enabled && !next.BillingCenter.Enabled) {
		return nil
	}
	checker, ok := r.repo.(pendingBillingRepository)
	if !ok {
		return errors.New("pending billing operations cannot be verified")
	}
	pending, err := checker.HasPendingBillingOperations(ctx)
	if err != nil {
		return err
	}
	if pending {
		return infraerrors.BadRequest("OIDC_BILLING_PENDING_OPERATIONS", "Complete pending billing operations before disabling the connector or changing its billing identity")
	}
	return nil
}

func (r *gatewayBillingRuntime) close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	cancel, child := r.cancel, r.child
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		<-r.done
	}
	if child != nil {
		child.Stop()
	}
}
