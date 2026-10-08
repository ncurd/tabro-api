package service

import (
	"context"
	"net/http"
	"time"
)

// Every immutable verifier owns its own JWKS cache and redirect trust policy.
// Updating issuer/JWKS never reuses keys fetched under the former authority.
func (s *GatewayResourceServer) currentResourceServer(ctx context.Context) (*GatewayResourceServer, error) {
	if s == nil {
		return nil, ErrGatewayOIDCUnavailable
	}
	if !s.live || s.settings == nil {
		return s, nil
	}
	connection, err := s.settings.GetOIDCBillingConnectionConfig(ctx)
	if err != nil {
		return nil, err
	}
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if s.liveServer != nil && s.liveConnection == connection {
		return s.liveServer, nil
	}
	server := newGatewayResourceServerWithClient(connection.ResourceServer, s.apiKeyService, &http.Client{Timeout: 10 * time.Second})
	server.billingCenterEnabled = connection.BillingCenter.Enabled
	server.settings = s.settings
	s.liveConnection, s.liveServer = connection, server
	return server, nil
}

func (s *GatewayResourceServer) CurrentRequiredScopes(ctx context.Context) string {
	server, err := s.currentResourceServer(ctx)
	if err != nil {
		return ""
	}
	return server.cfg.RequiredScopes
}
