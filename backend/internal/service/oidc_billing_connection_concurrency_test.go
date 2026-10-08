//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type billingConnectionConcurrentRepo struct {
	mu                sync.Mutex
	values            map[string]string
	policyCommitReady chan struct{}
	allowPolicyCommit chan struct{}
}

func (r *billingConnectionConcurrentRepo) Get(ctx context.Context, key string) (*Setting, error) {
	value, err := r.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *billingConnectionConcurrentRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, exists := r.values[key]
	if !exists {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *billingConnectionConcurrentRepo) Set(ctx context.Context, key, value string) error {
	return r.SetMultiple(ctx, map[string]string{key: value})
}

func (r *billingConnectionConcurrentRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, exists := r.values[key]; exists {
			values[key] = value
		}
	}
	return values, nil
}

func (r *billingConnectionConcurrentRepo) SetMultiple(ctx context.Context, values map[string]string) error {
	if values[SettingKeyOIDCBillingEnabled] == "true" {
		// Pause after policy validation but before commit. The repository's mutex
		// is deliberately free, so only the service guard can block an edit.
		close(r.policyCommitReady)
		select {
		case <-r.allowPolicyCommit:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func (r *billingConnectionConcurrentRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make(map[string]string, len(r.values))
	for key, value := range r.values {
		values[key] = value
	}
	return values, nil
}

func (r *billingConnectionConcurrentRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

func TestOIDCBillingPolicyEnableSerializesWithConnectionDisable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection := validAdminBillingConnection()
	encoded, err := json.Marshal(connection)
	require.NoError(t, err)
	repo := &billingConnectionConcurrentRepo{
		values: map[string]string{
			SettingKeyOIDCBillingConnection: string(encoded),
			SettingKeyOIDCBillingEnabled:    "false",
			SettingKeyOIDCOnlyEnabled:       "true",
			SettingKeyOIDCConnectEnabled:    "true",
			SettingKeyOIDCConnectIssuerURL:  connection.ResourceServer.IssuerURL,
		},
		policyCommitReady: make(chan struct{}),
		allowPolicyCommit: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releasePolicyCommit := func() { releaseOnce.Do(func() { close(repo.allowPolicyCommit) }) }
	defer releasePolicyCommit()
	svc := NewSettingService(repo, &config.Config{})
	policyResult := make(chan error, 1)
	go func() {
		policyResult <- svc.UpdateSettings(ctx, &SystemSettings{
			OIDCBillingEnabled:        true,
			OIDCBillingRateMultiplier: 1,
			OIDCOnlyEnabled:           true,
			OIDCConnectEnabled:        true,
			OIDCConnectIssuerURL:      connection.ResourceServer.IssuerURL,
		})
	}()
	select {
	case <-repo.policyCommitReady:
	case <-ctx.Done():
		t.Fatal("policy update did not reach its commit barrier")
	}
	// This assertion makes the paused interleaving deterministic: connector
	// updates use this same mutex and cannot validate against billing=false.
	if svc.oidcBillingConnectionUpdateMu.TryLock() {
		svc.oidcBillingConnectionUpdateMu.Unlock()
		t.Fatal("policy validation and commit must hold the connection update guard")
	}
	disableStarted := make(chan struct{})
	disableResult := make(chan error, 1)
	go func() {
		close(disableStarted)
		connection.BillingCenter.Enabled = false
		_, err := svc.UpdateOIDCBillingConnectionConfig(ctx, connection)
		disableResult <- err
	}()
	<-disableStarted
	select {
	case err := <-disableResult:
		t.Fatalf("connector edit completed before the guarded policy commit: %v", err)
	default:
	}
	releasePolicyCommit()
	select {
	case err := <-policyResult:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("policy update did not finish")
	}
	select {
	case err := <-disableResult:
		status, detail := infraerrors.ToHTTP(err)
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, "OIDC_BILLING_CONNECTION_IN_USE", detail.Reason)
	case <-ctx.Done():
		t.Fatal("connector edit did not finish after the policy commit")
	}
	policy, err := svc.GetOIDCBillingPolicy(ctx)
	require.NoError(t, err)
	require.True(t, policy.Enabled)
	saved, err := svc.GetOIDCBillingConnectionConfig(ctx)
	require.NoError(t, err)
	require.True(t, saved.BillingCenter.Enabled)
	require.True(t, saved.ResourceServer.Enabled)
}
