package middleware

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAuthBillingOnlyKeyRequiresVerifiedAuthBillingPrincipal(t *testing.T) {
	key := &service.APIKey{Status: service.StatusAPIKeyAuthBillingOnly, OIDCManaged: true, AuthBillingOnly: true}
	require.False(t, gatewayKeyStatusPermitsInitialAuth(key, nil))
	require.False(t, gatewayKeyStatusPermitsInitialAuth(key, &service.GatewayOIDCPrincipal{}))
	require.True(t, gatewayKeyStatusPermitsInitialAuth(key, &service.GatewayOIDCPrincipal{AuthBilled: true}))

	key.Status = service.StatusAPIKeyDisabled
	require.False(t, gatewayKeyStatusPermitsInitialAuth(key, &service.GatewayOIDCPrincipal{AuthBilled: true}))
	key.Status = service.StatusAPIKeyActive
	require.False(t, gatewayKeyStatusPermitsInitialAuth(key, &service.GatewayOIDCPrincipal{AuthBilled: true}))
}
