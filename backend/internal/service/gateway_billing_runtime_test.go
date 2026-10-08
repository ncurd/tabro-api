//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type runtimeSettingsRepository struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
	err    error
}

func (r *runtimeSettingsRepository) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	result := map[string]string{}
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}
func (r *runtimeSettingsRepository) SetMultiple(_ context.Context, values map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	if r.values == nil {
		r.values = map[string]string{}
	}
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}
func (r *runtimeSettingsRepository) GetValue(ctx context.Context, key string) (string, error) {
	values, err := r.GetMultiple(ctx, []string{key})
	if err != nil {
		return "", err
	}
	if value, ok := values[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}

type runtimeBillingRepository struct {
	*gatewayBillingStoreStub
	root     *runtimeBillingRepository
	producer string
	pending  bool
}

func (r *runtimeBillingRepository) WithBillingProducer(producer string) GatewayBillingRepository {
	root := r
	if r.root != nil {
		root = r.root
	}
	return &runtimeBillingRepository{gatewayBillingStoreStub: root.gatewayBillingStoreStub, root: root, producer: producer}
}
func (r *runtimeBillingRepository) HasPendingBillingOperations(context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending || r.op.OperationID != "" && r.op.State != bc.Settled && r.op.State != bc.Released, nil
}
func (r *runtimeBillingRepository) Claim(context.Context, time.Duration, int) ([]bc.Event, error) {
	return nil, nil
}
func (r *runtimeBillingRepository) PendingBillingShadows(context.Context, int) ([]GatewayBillingShadow, error) {
	return nil, nil
}

func runtimeBillingFixture(t *testing.T) (*GatewayBillingCoordinator, *SettingService, *runtimeSettingsRepository, *runtimeBillingRepository, OIDCBillingConnectionConfig) {
	t.Helper()
	settingsRepo := &runtimeSettingsRepository{}
	cfg := &config.Config{}
	settings := NewSettingService(settingsRepo, cfg)
	repo := &runtimeBillingRepository{gatewayBillingStoreStub: &gatewayBillingStoreStub{}}
	coordinator, err := ProvideGatewayBillingCoordinator(cfg, repo, nil, nil, settings)
	require.NoError(t, err)
	t.Cleanup(coordinator.Stop)
	connection := OIDCBillingConnectionConfig{
		BillingCenter:  config.BillingCenterConfig{Enabled: true, BaseURL: "https://auth.example", TokenURL: "https://auth.example/token", ProducerClientID: "billing-client", ClientSecret: "first-secret", TimeoutSeconds: 15},
		ResourceServer: gatewayOIDCTestConfig("https://auth.example", "https://auth.example/jwks"),
	}
	return coordinator, settings, settingsRepo, repo, connection
}

func TestGatewayBillingRuntimeEnablesFromDisabledStartupAndRotatesCredentials(t *testing.T) {
	coordinator, settings, _, _, connection := runtimeBillingFixture(t)
	ctx := context.Background()
	before, err := coordinator.current(ctx)
	require.NoError(t, err)
	require.False(t, before.enabled)
	_, err = settings.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.NoError(t, err)
	active, err := coordinator.current(ctx)
	require.NoError(t, err)
	require.True(t, active.enabled)
	require.Equal(t, "billing-client", active.repo.(*runtimeBillingRepository).producer)
	require.NotNil(t, active.authority)
	connection.BillingCenter.ClientSecret = "rotated-secret"
	connection.BillingCenter.TokenURL = "https://auth.example/new-token"
	_, err = settings.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.NoError(t, err)
	rotated, err := coordinator.current(ctx)
	require.NoError(t, err)
	require.NotSame(t, active, rotated)
	require.NotSame(t, active.authority, rotated.authority)
	require.Equal(t, "first-secret", active.connection.BillingCenter.ClientSecret, "in-flight requests keep their original immutable connector")
	require.Equal(t, "rotated-secret", rotated.connection.BillingCenter.ClientSecret)
	require.False(t, settings.cfg.BillingCenter.Enabled, "the shared startup config must remain immutable")
}

func TestGatewayBillingRuntimeProtectsBillingIdentityAndPendingReservations(t *testing.T) {
	coordinator, settings, _, repo, connection := runtimeBillingFixture(t)
	ctx := context.Background()
	_, err := settings.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.NoError(t, err)
	active, err := coordinator.current(ctx)
	require.NoError(t, err)
	changed := connection
	changed.ResourceServer.IssuerURL = "https://other.example"
	_, err = settings.UpdateOIDCBillingConnectionConfig(ctx, changed)
	require.ErrorContains(t, err, "Disable OIDC billing")
	connection.BillingCenter.Enabled = false
	repo.pending = true
	_, err = settings.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.ErrorContains(t, err, "pending billing operations")
	repo.pending = false
	_, err = settings.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.NoError(t, err)
	_, err = active.prepareWithQuote(ctx, GatewayBillingRoute{Mode: "central", runtime: active}, nil, "", "", "", nil, nil, nil)
	require.ErrorIs(t, err, bc.ErrState, "a route selected before connector shutdown cannot create a new intent afterward")
	changed.BillingCenter.Enabled = false
	_, err = settings.UpdateOIDCBillingConnectionConfig(ctx, changed)
	require.NoError(t, err, "an unused disabled connector can be corrected")
}

func TestGatewayBillingRuntimeFailsClosedOnSettingsReadFailure(t *testing.T) {
	coordinator, settings, settingsRepo, _, connection := runtimeBillingFixture(t)
	ctx := context.Background()
	_, err := settings.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.NoError(t, err)
	_, err = coordinator.current(ctx)
	require.NoError(t, err)
	settingsRepo.mu.Lock()
	settingsRepo.err = errors.New("database unavailable")
	settingsRepo.mu.Unlock()
	_, err = coordinator.RouteForPrincipal(ctx, 1, nil)
	require.ErrorContains(t, err, "database unavailable")
	server := NewGatewayResourceServer(settings.cfg, &APIKeyService{GatewayBilling: coordinator})
	require.False(t, server.Enabled())
	_, _, err = server.Authenticate(ctx, "header.payload.signature")
	require.ErrorIs(t, err, ErrGatewayOIDCUnavailable)
}

func TestGatewayResourceServerRuntimeRebuildsTrustPolicyAndScopes(t *testing.T) {
	coordinator, settings, _, _, connection := runtimeBillingFixture(t)
	ctx := context.Background()
	server := NewGatewayResourceServer(settings.cfg, &APIKeyService{GatewayBilling: coordinator})
	require.False(t, server.Enabled())
	_, err := settings.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.NoError(t, err)
	require.True(t, server.Enabled())
	first, err := server.currentResourceServer(ctx)
	require.NoError(t, err)
	connection.ResourceServer.RequiredScopes = "llm.invoke llm.images"
	connection.ResourceServer.JWKSURL = "https://auth.example/new-jwks"
	_, err = settings.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.NoError(t, err)
	second, err := server.currentResourceServer(ctx)
	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.Empty(t, second.keys, "a new trust policy starts with an independent JWKS cache")
	require.Equal(t, "llm.invoke llm.images", server.CurrentRequiredScopes(ctx))
}

type blockingRuntimeAuthority struct {
	*gatewayBillingAuthorityStub
	quoted  chan struct{}
	proceed chan struct{}
}

func (a *blockingRuntimeAuthority) Quote(ctx context.Context, request bc.ProofRequest[bc.QuoteRequest]) (bc.Quote, error) {
	close(a.quoted)
	select {
	case <-ctx.Done():
		return bc.Quote{}, ctx.Err()
	case <-a.proceed:
		return a.gatewayBillingAuthorityStub.Quote(ctx, request)
	}
}

func TestGatewayBillingRuntimeConfigurationCommitWaitsForReservationAdmission(t *testing.T) {
	coordinator, settings, _, _, connection := runtimeBillingFixture(t)
	ctx := context.Background()
	_, err := settings.UpdateOIDCBillingConnectionConfig(ctx, connection)
	require.NoError(t, err)
	active, err := coordinator.current(ctx)
	require.NoError(t, err)
	_, fixtureRepo, fixtureAuthority, principal := gatewayBillingFixture()
	active.Stop() // Install the test authority only after background readers stop.
	authority := &blockingRuntimeAuthority{gatewayBillingAuthorityStub: fixtureAuthority, quoted: make(chan struct{}), proceed: make(chan struct{})}
	active.authority = authority
	active.creditPricer = &testGatewayCreditPricer{maximum: "12.5"}
	prepareDone := make(chan error, 1)
	go func() {
		_, err := active.PrepareForKey(ctx, *fixtureRepo.route, principal, "proof", "/v1/responses", "in-flight", []byte(`{"model":"gpt-6-sol","max_output_tokens":50}`), nil)
		prepareDone <- err
	}()
	<-authority.quoted
	connection.BillingCenter.Enabled = false
	updateDone := make(chan error, 1)
	go func() { _, err := settings.UpdateOIDCBillingConnectionConfig(ctx, connection); updateDone <- err }()
	select {
	case err := <-updateDone:
		t.Fatalf("configuration commit overtook an in-flight reservation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(authority.proceed)
	require.NoError(t, <-prepareDone)
	require.ErrorContains(t, <-updateDone, "pending billing operations", "the validator must observe the admitted intent before permitting shutdown")
}
