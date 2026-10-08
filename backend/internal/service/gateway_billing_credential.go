package service

import (
	"context"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
)

type GatewayBillingCredential struct {
	APIKeyID, LocalUserID         int64
	BindingID                     string
	Version                       int64
	Issuer, ActorUserID, TenantID string
}

func (s *GatewayBillingCoordinator) Credential(ctx context.Context, apiKeyID, userID int64) (*GatewayBillingCredential, error) {
	if s == nil {
		return nil, nil
	}
	if s.runtime != nil {
		current, err := s.current(ctx)
		if err != nil {
			return nil, err
		}
		return current.Credential(ctx, apiKeyID, userID)
	}
	if s.repo == nil {
		return nil, bc.ErrState
	}
	return s.repo.GetBillingCredential(ctx, apiKeyID, userID)
}
func (c *GatewayBillingCredential) Principal() *GatewayOIDCPrincipal {
	if c == nil {
		return nil
	}
	return &GatewayOIDCPrincipal{Issuer: c.Issuer, Subject: c.ActorUserID, Tenant: c.TenantID, ClientID: "legacy-api-key"}
}
func gatewayBillingProof[T any](origin, subjectProof string, credential *GatewayBillingCredential, request T) bc.ProofRequest[T] {
	result := bc.ProofRequest[T]{OriginAppID: origin, SubjectProof: subjectProof, Request: request}
	if credential != nil {
		result.SubjectProof = ""
		result.CredentialBindingID = credential.BindingID
		result.CredentialVersion = credential.Version
	}
	return result
}
