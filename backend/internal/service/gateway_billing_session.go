package service

import (
	"context"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

type gatewayBillingSessionKey struct{}

// GatewayBillingSession is request-scoped only. Its proof is captured in a
// transient closure, never copied into turn metadata, a job or an outbox row.
type GatewayBillingSession struct {
	Mode          string
	Prepare       func(context.Context, string, []byte) (*bc.Execution, []byte, error)
	Finish        func(context.Context, *bc.Execution) error
	ValidateModel func(*bc.Execution, string) error
}

func WithGatewayBillingSession(ctx context.Context, s *GatewayBillingSession) context.Context {
	return context.WithValue(ctx, gatewayBillingSessionKey{}, s)
}
func GatewayBillingSessionFromContext(ctx context.Context) *GatewayBillingSession {
	s, _ := ctx.Value(gatewayBillingSessionKey{}).(*GatewayBillingSession)
	return s
}

func (s *GatewayBillingCoordinator) Session(route GatewayBillingRoute, principal *GatewayOIDCPrincipal, proof string, binding *GatewayBillingCredential, key *APIKey) *GatewayBillingSession {
	return &GatewayBillingSession{Mode: route.Mode, Finish: s.Finish, ValidateModel: s.ValidateBillingModelCaps, Prepare: func(ctx context.Context, id string, body []byte) (*bc.Execution, []byte, error) {
		bounded := body
		var err error
		if route.Mode == "central" {
			bounded, err = s.BoundRequest("/ws/responses", body)
			if err != nil {
				return nil, nil, err
			}
		}
		execution, err := s.PrepareForKey(ctx, route, principal, proof, "/ws/responses", id, bounded, key, binding)
		if err != nil && route.Mode == "shadow" {
			if auditErr := s.ShadowFailure(ctx, route, id, HashUsageRequestPayload(body), "quote_failed"); auditErr != nil {
				logger.LegacyPrintf("billing_center", "shadow websocket failure audit unavailable: %v", auditErr)
			}
			return nil, body, nil
		}
		return execution, bounded, err
	}}
}

// BeforeSupplierWrite permits a missing execution only for shadow auditing;
// central sessions must have a durable reservation for every billable turn.
func (s *GatewayBillingSession) BeforeSupplierWrite(ctx context.Context, execution *bc.Execution) error {
	if execution == nil {
		if s.Mode == "shadow" {
			return nil
		}
		return bc.ErrState
	}
	return bc.BeforeSupplierRequest(bc.WithExecution(ctx, execution))
}
