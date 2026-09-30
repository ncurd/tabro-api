package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"golang.org/x/crypto/bcrypt"
)

// AutoProvisionOIDCGatewayIdentity atomically creates a local technical user
// and its bound internal key. No email claim is accepted or used for lookup.
// A historical binding, including a deleted key, blocks recreation so a
// revoked identity cannot return as a fresh active account.
func (r *apiKeyRepository) AutoProvisionOIDCGatewayIdentity(ctx context.Context, issuer, subject, keyValue string) (*service.APIKey, error) {
	issuer, subject = strings.TrimSpace(issuer), strings.TrimSpace(subject)
	if issuer == "" || subject == "" || len(issuer) > 512 || len(subject) > 512 || keyValue == "" {
		return nil, service.ErrGatewayOIDCTokenInvalid
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// SkipSoftDelete is deliberate: a revoked key must not become eligible for
	// automatic reenrollment merely because the active lookup returned nothing.
	historical, err := tx.APIKey.Query().Where(apikey.OidcIssuerEQ(issuer), apikey.OidcSubjectEQ(subject)).Exist(mixins.SkipSoftDelete(ctx))
	if err != nil {
		return nil, err
	}
	if historical {
		_ = tx.Rollback()
		if current, lookupErr := r.GetByOIDCIdentity(ctx, issuer, subject); lookupErr == nil {
			return current, nil
		} else if !errors.Is(lookupErr, service.ErrAPIKeyNotFound) {
			return nil, lookupErr
		}
		return nil, service.ErrGatewayOIDCIdentityNotBound
	}

	// Only public standard groups with an active upstream account can be the
	// initial route. Without one, rollback the whole provision and fail closed.
	defaultGroup, err := tx.Group.Query().Where(
		group.StatusEQ(service.StatusActive),
		group.DeletedAtIsNil(),
		group.IsExclusiveEQ(false),
		group.SubscriptionTypeEQ(service.SubscriptionTypeStandard),
		group.HasAccountsWith(account.StatusEQ(service.StatusActive), account.DeletedAtIsNil()),
	).Order(dbent.Asc(group.FieldSortOrder), dbent.Asc(group.FieldID)).First(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrGatewayOIDCUnavailable
		}
		return nil, err
	}

	// The synthetic address is random and never compared with IdP claims. It
	// cannot be used to sign in because the account is API-only and its random
	// password is never exposed.
	randomSecret := make([]byte, 32)
	if _, err := rand.Read(randomSecret); err != nil {
		return nil, err
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(hex.EncodeToString(randomSecret)), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	user, err := tx.User.Create().
		SetEmail("oidc-api-" + hex.EncodeToString(randomSecret) + "@invalid.invalid").
		SetUsername("OIDC API user").
		SetPasswordHash(string(passwordHash)).
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		SetAPIOnly(true).
		Save(ctx)
	if err != nil {
		return r.resolveAutoProvisionConstraint(ctx, tx, issuer, subject, err)
	}

	_, err = tx.APIKey.Create().
		SetUserID(user.ID).
		SetKey(keyValue).
		SetName("OIDC Access Token").
		SetGroupID(defaultGroup.ID).
		SetStatus(service.StatusAPIKeyAuthBillingOnly).
		SetOidcManaged(true).
		SetAuthBillingOnly(true).
		SetOidcIssuer(issuer).
		SetOidcSubject(subject).
		Save(ctx)
	if err != nil {
		return r.resolveAutoProvisionConstraint(ctx, tx, issuer, subject, err)
	}
	if err := tx.Commit(); err != nil {
		return r.resolveAutoProvisionConstraint(ctx, tx, issuer, subject, err)
	}
	key, err := r.GetByOIDCIdentity(ctx, issuer, subject)
	if err != nil {
		return nil, fmt.Errorf("read auto-provisioned oidc identity: %w", err)
	}
	return key, nil
}

func (r *apiKeyRepository) resolveAutoProvisionConstraint(ctx context.Context, tx *dbent.Tx, issuer, subject string, cause error) (*service.APIKey, error) {
	_ = tx.Rollback()
	if !dbent.IsConstraintError(cause) {
		return nil, cause
	}
	if current, err := r.GetByOIDCIdentity(ctx, issuer, subject); err == nil {
		return current, nil
	} else if !errors.Is(err, service.ErrAPIKeyNotFound) {
		return nil, err
	}
	// This was a synthetic email or random key collision, not an identity
	// winner. The service retries with fresh randomness.
	return nil, service.ErrAPIKeyExists
}
