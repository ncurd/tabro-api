//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type billingSetupFixture struct {
	server     *httptest.Server
	repo       *oidcOnlySettingRepo
	service    *SettingService
	tokenCalls int
	form       url.Values
	metadata   func(http.ResponseWriter, *http.Request)
	token      func(http.ResponseWriter, *http.Request)
}

func newBillingSetupFixture(t *testing.T) *billingSetupFixture {
	t.Helper()
	f := &billingSetupFixture{}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			if f.metadata != nil {
				f.metadata(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.server.URL, "token_endpoint": f.server.URL + "/connect/token"})
		case "/connect/token":
			f.tokenCalls++
			_ = r.ParseForm()
			f.form = r.Form
			if f.token != nil {
				f.token(w, r)
				return
			}
			_, _ = fmt.Fprint(w, `{"access_token":"ephemeral-service-token","token_type":"Bearer","expires_in":600}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	f.repo = &oidcOnlySettingRepo{values: map[string]string{
		SettingKeyOIDCOnlyEnabled: "true", SettingKeyOIDCConnectEnabled: "true",
		SettingKeyOIDCConnectIssuerURL: f.server.URL, SettingKeyOIDCConnectClientID: "tabro-llm",
		SettingKeyOIDCBillingEnabled: "false",
	}}
	f.service = NewSettingService(f.repo, &config.Config{})
	f.service.oidcBillingSetupTransport = f.server.Client().Transport
	return f
}

func TestOIDCBillingSetupValidatesDedicatedCredentialsAndPreservesLogin(t *testing.T) {
	f := newBillingSetupFixture(t)
	result, err := f.service.SetupOIDCBillingConnection(context.Background(), "billing-private-secret")
	require.NoError(t, err)
	require.Equal(t, 1, f.tokenCalls)
	require.Equal(t, "client_credentials", f.form.Get("grant_type"))
	require.Equal(t, tabroBillingProducerClientID, f.form.Get("client_id"))
	require.Equal(t, "billing-private-secret", f.form.Get("client_secret"))
	require.Equal(t, "billing.reserve billing.dispatch billing.extend billing.settle billing.release billing.read", f.form.Get("scope"))
	require.True(t, result.OIDCBillingSupported)
	require.True(t, result.ClientSecretConfigured)
	require.Empty(t, result.BillingCenter.ClientSecret)
	require.Equal(t, "tabro-llm", f.repo.values[SettingKeyOIDCConnectClientID])
	require.Equal(t, "false", f.repo.values[SettingKeyOIDCBillingEnabled])
	require.Equal(t, "tabro-llm", result.ResourceServer.Audience)
	require.Equal(t, "tabro-agent", result.ResourceServer.AllowedClientIDs)
	require.True(t, result.ResourceServer.RequireTenant)
	require.True(t, result.ResourceServer.TokenExchange.RequireActor)
	require.Equal(t, 1, result.ResourceServer.TokenExchange.MaxDelegationDepth)
	require.False(t, result.BillingCenter.InsecureLocal)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "billing-private-secret")
	require.NotContains(t, f.repo.values[SettingKeyOIDCBillingConnection], "ephemeral-service-token")
	_, err = f.service.SetupOIDCBillingConnection(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, "billing-private-secret", f.form.Get("client_secret"))
}

func TestOIDCBillingSetupPreservesExplicitModelAndActorAllowlists(t *testing.T) {
	f := newBillingSetupFixture(t)
	previous := tabroOIDCBillingConnection(f.server.URL, "", f.server.URL+"/connect/token", "old-secret")
	previous.ResourceServer.AllowedClientIDs = "tabro-agent,approved-model-app"
	previous.ResourceServer.TokenExchange.AllowedActorClientIDs = "tabro-agent,approved-actor"
	_, err := f.service.UpdateOIDCBillingConnectionConfig(context.Background(), previous)
	require.NoError(t, err)
	result, err := f.service.SetupOIDCBillingConnection(context.Background(), "new-secret")
	require.NoError(t, err)
	require.Equal(t, previous.ResourceServer.AllowedClientIDs, result.ResourceServer.AllowedClientIDs)
	require.Equal(t, previous.ResourceServer.TokenExchange.AllowedActorClientIDs, result.ResourceServer.TokenExchange.AllowedActorClientIDs)
}

func TestOIDCBillingSetupCannotReuseAnotherAuthorityOrProducerSecret(t *testing.T) {
	for _, change := range []string{"issuer", "producer"} {
		t.Run(change, func(t *testing.T) {
			f := newBillingSetupFixture(t)
			previous := tabroOIDCBillingConnection(f.server.URL, "", f.server.URL+"/connect/token", "old-secret")
			if change == "issuer" {
				previous.BillingCenter.BaseURL = "https://other.example"
				previous.ResourceServer.IssuerURL = "https://other.example"
			} else {
				previous.BillingCenter.ProducerClientID = "other-producer"
			}
			_, err := f.service.UpdateOIDCBillingConnectionConfig(context.Background(), previous)
			require.NoError(t, err)
			saved := f.repo.values[SettingKeyOIDCBillingConnection]
			_, err = f.service.SetupOIDCBillingConnection(context.Background(), "")
			require.ErrorContains(t, err, "OIDC_BILLING_SETUP_SECRET_REQUIRED")
			require.Zero(t, f.tokenCalls)
			require.Equal(t, saved, f.repo.values[SettingKeyOIDCBillingConnection])
		})
	}
}

func TestOIDCBillingSetupRejectsUnsafeMetadataBeforeSendingSecret(t *testing.T) {
	for _, mode := range []string{"other issuer", "other token host", "http token", "token userinfo", "token query", "redirect discovery", "oversize metadata", "invalid JSON", "external discovery"} {
		t.Run(mode, func(t *testing.T) {
			f := newBillingSetupFixture(t)
			f.metadata = func(w http.ResponseWriter, r *http.Request) {
				issuer, token := f.server.URL, f.server.URL+"/connect/token"
				switch mode {
				case "other issuer":
					issuer = "https://other.example"
				case "other token host":
					token = "https://other.example/token"
				case "http token":
					token = strings.Replace(token, "https:", "http:", 1)
				case "token userinfo":
					token = strings.Replace(token, "https://", "https://user:password@", 1)
				case "token query":
					token += "?secret=untrusted"
				case "redirect discovery":
					http.Redirect(w, r, f.server.URL+"/connect/token", http.StatusFound)
					return
				case "oversize metadata":
					_, _ = fmt.Fprint(w, strings.Repeat(" ", 65537))
					return
				case "invalid JSON":
					_, _ = fmt.Fprint(w, `{"issuer":`)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "token_endpoint": token})
			}
			if mode == "external discovery" {
				f.repo.values[SettingKeyOIDCConnectDiscoveryURL] = "https://other.example/discovery"
			}
			_, err := f.service.SetupOIDCBillingConnection(context.Background(), "never-send-secret")
			require.Error(t, err)
			require.NotContains(t, err.Error(), "never-send-secret")
			require.Zero(t, f.tokenCalls)
			require.Empty(t, f.repo.values[SettingKeyOIDCBillingConnection])
		})
	}
}

func TestOIDCBillingSetupRejectsCredentialsAndReducedScopesWithoutLeaks(t *testing.T) {
	for _, mode := range []string{"rejected", "redirect", "reduced scope", "empty scope", "all scopes"} {
		t.Run(mode, func(t *testing.T) {
			f := newBillingSetupFixture(t)
			f.token = func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "rejected":
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = fmt.Fprint(w, "sensitive-secret remote-debug-body")
					return
				case "redirect":
					http.Redirect(w, r, f.server.URL+"/connect/token", http.StatusTemporaryRedirect)
					return
				}
				scope := "billing.reserve billing.settle"
				if mode == "empty scope" {
					scope = ""
				}
				if mode == "all scopes" {
					scope = r.Form.Get("scope")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "ephemeral-service-token", "token_type": "Bearer", "expires_in": 600, "scope": scope})
			}
			_, err := f.service.SetupOIDCBillingConnection(context.Background(), "sensitive-secret")
			if mode == "all scopes" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "OIDC_BILLING_SETUP_CREDENTIALS_REJECTED")
			require.NotContains(t, err.Error(), "sensitive-secret")
			require.NotContains(t, err.Error(), "ephemeral-service-token")
			require.NotContains(t, err.Error(), "remote-debug-body")
			require.Empty(t, f.repo.values[SettingKeyOIDCBillingConnection])
			require.Equal(t, 1, f.tokenCalls, "token redirect must never be followed")
		})
	}
}

func TestOIDCBillingSetupRejectsUnsavedOIDCAndHTTPAuthority(t *testing.T) {
	for _, mode := range []string{"only disabled", "login disabled", "http issuer", "missing secret"} {
		t.Run(mode, func(t *testing.T) {
			f := newBillingSetupFixture(t)
			secret := "private-secret"
			switch mode {
			case "only disabled":
				f.repo.values[SettingKeyOIDCOnlyEnabled] = "false"
			case "login disabled":
				f.repo.values[SettingKeyOIDCConnectEnabled] = "false"
			case "http issuer":
				f.repo.values[SettingKeyOIDCConnectIssuerURL] = "http://auth.example"
			case "missing secret":
				secret = ""
			}
			_, err := f.service.SetupOIDCBillingConnection(context.Background(), secret)
			require.Error(t, err)
			require.Zero(t, f.tokenCalls)
			require.Empty(t, f.repo.values[SettingKeyOIDCBillingConnection])
		})
	}
}

func TestOIDCBillingSetupRejectsSettingsChangedDuringVerification(t *testing.T) {
	for _, key := range []string{SettingKeyOIDCConnectIssuerURL, SettingKeyOIDCOnlyEnabled, SettingKeyOIDCConnectDiscoveryURL, SettingKeyOIDCBillingConnection} {
		t.Run(key, func(t *testing.T) {
			f := newBillingSetupFixture(t)
			// Model a serialized admin update during the remote token request.
			f.token = func(w http.ResponseWriter, _ *http.Request) {
				f.service.oidcBillingConnectionUpdateMu.Lock()
				f.repo.values[key] = "changed-by-another-admin"
				f.service.oidcBillingConnectionUpdateMu.Unlock()
				_, _ = fmt.Fprint(w, `{"access_token":"ephemeral-service-token","token_type":"Bearer","expires_in":600}`)
			}
			_, err := f.service.SetupOIDCBillingConnection(context.Background(), "private-secret")
			require.ErrorContains(t, err, "OIDC_BILLING_SETUP_CONFIG_CHANGED")
			if key == SettingKeyOIDCBillingConnection {
				require.Equal(t, "changed-by-another-admin", f.repo.values[key])
			} else {
				require.Empty(t, f.repo.values[SettingKeyOIDCBillingConnection])
			}
		})
	}
}
