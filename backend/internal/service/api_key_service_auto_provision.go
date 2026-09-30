package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// AutoProvisionOIDCGatewayIdentity creates a technical API-only user and its
// Auth-billed gateway key on the first fully verified resource-server request.
// A repository transaction owns the user/key pair and the unique identity
// constraint converges requests arriving at different gateway processes.
func (s *APIKeyService) AutoProvisionOIDCGatewayIdentity(ctx context.Context, issuer, subject string) (*APIKey, error) {
	if s == nil || s.apiKeyRepo == nil {
		return nil, ErrGatewayOIDCUnavailable
	}
	var err error
	issuer, subject, err = normalizeOIDCGatewayIdentity(issuer, subject)
	if err != nil {
		return nil, ErrGatewayOIDCTokenInvalid
	}
	autoRepo, ok := s.apiKeyRepo.(oidcGatewayAutoProvisionRepository)
	if !ok {
		return nil, ErrGatewayOIDCUnavailable
	}

	identity := issuer + "\x00" + subject
	value, err, _ := s.oidcIdentityGroup.Do(identity, func() (any, error) {
		for attempt := 0; attempt < oidcGatewayKeyMaxCreateAttempts; attempt++ {
			keyValue, generateErr := generateOIDCGatewayKeyValue()
			if generateErr != nil {
				return nil, generateErr
			}
			key, createErr := autoRepo.AutoProvisionOIDCGatewayIdentity(ctx, issuer, subject, keyValue)
			if errors.Is(createErr, ErrAPIKeyExists) {
				continue
			}
			if createErr != nil {
				return nil, createErr
			}
			return key, nil
		}
		return nil, fmt.Errorf("auto-provision oidc identity: exhausted %d key generation attempts", oidcGatewayKeyMaxCreateAttempts)
	})
	if err != nil {
		return nil, err
	}
	key, ok := value.(*APIKey)
	if !ok || key == nil || strings.TrimSpace(key.OIDCIssuer) != issuer || strings.TrimSpace(key.OIDCSubject) != subject {
		return nil, ErrGatewayOIDCUnavailable
	}
	s.compileAPIKeyIPRules(key)
	return key, nil
}
