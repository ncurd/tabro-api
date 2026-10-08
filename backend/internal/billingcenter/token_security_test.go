package billingcenter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const tokenSecurityTestSecret = "test-client-secret-never-echo"

func tokenSecurityRequestedScopes() []string {
	return []string{"billing.reserve", "billing.dispatch", "billing.extend", "billing.settle", "billing.release", "billing.read"}
}

func newTokenSecuritySource(t *testing.T, endpoint string) *ClientCredentialsTokenSource {
	t.Helper()
	source, err := NewClientCredentialsTokenSource(TokenConfig{
		TokenURL: endpoint, ClientID: "test-billing-producer", ClientSecret: tokenSecurityTestSecret,
		Scopes: tokenSecurityRequestedScopes(), Timeout: 2 * time.Second, InsecureLocal: true,
	}, nil)
	require.NoError(t, err)
	return source
}

func TestTokenSourceAcceptsOmittedAndCompleteGrantedScopes(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "omitted scope"
		if explicit {
			name = "complete scope"
		}
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				require.NoError(t, r.ParseForm())
				require.Equal(t, strings.Join(tokenSecurityRequestedScopes(), " "), r.Form.Get("scope"))
				body := map[string]any{"access_token": "authorized-service-token", "token_type": "Bearer", "expires_in": 600}
				if explicit {
					// Order and additional scopes do not reduce the requested grant.
					body["scope"] = "billing.read billing.release billing.settle billing.extend billing.dispatch billing.reserve extra.permission"
				}
				require.NoError(t, json.NewEncoder(w).Encode(body))
			}))
			defer server.Close()
			source := newTokenSecuritySource(t, server.URL)
			for i := 0; i < 2; i++ {
				token, err := source.Token(context.Background())
				require.NoError(t, err)
				require.Equal(t, "authorized-service-token", token)
			}
			require.EqualValues(t, 1, requests.Load(), "a valid token is cached")
		})
	}
}

func TestTokenSourceRejectsInvalidResponsesWithoutLeaksOrCaching(t *testing.T) {
	tests := []struct {
		name       string
		scope      any
		oversize   bool
		trailing   bool
		httpStatus int
	}{
		{name: "reduced scope", scope: "billing.reserve billing.settle"},
		{name: "empty scope", scope: ""},
		{name: "blank scope", scope: "   "},
		{name: "null scope", scope: nil},
		{name: "numeric scope", scope: 7},
		{name: "array scope", scope: tokenSecurityRequestedScopes()},
		{name: "object scope", scope: map[string]string{"scope": strings.Join(tokenSecurityRequestedScopes(), " ")}},
		{name: "oversized response", scope: strings.Join(tokenSecurityRequestedScopes(), " "), oversize: true},
		{name: "trailing response garbage", scope: strings.Join(tokenSecurityRequestedScopes(), " "), trailing: true},
		{name: "provider error body", httpStatus: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) > 1 {
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
						"access_token": "newly-authorized-service-token", "token_type": "Bearer", "expires_in": 600,
						"scope": strings.Join(tokenSecurityRequestedScopes(), " "),
					}))
					return
				}
				body := map[string]any{
					"access_token": "rejected-service-token-never-echo", "token_type": "Bearer", "expires_in": 600,
					"scope": tt.scope, "provider_detail": tokenSecurityTestSecret,
				}
				if tt.oversize {
					body["padding"] = strings.Repeat("x", 65536)
				}
				encoded, err := json.Marshal(body)
				require.NoError(t, err)
				if tt.trailing {
					encoded = append(encoded, []byte(" trailing garbage "+tokenSecurityTestSecret)...)
				}
				if tt.httpStatus != 0 {
					w.WriteHeader(tt.httpStatus)
				}
				_, err = w.Write(encoded)
				require.NoError(t, err)
			}))
			defer server.Close()
			source := newTokenSecuritySource(t, server.URL)
			token, err := source.Token(context.Background())
			require.Error(t, err)
			require.Empty(t, token)
			require.NotContains(t, err.Error(), tokenSecurityTestSecret)
			require.NotContains(t, err.Error(), "rejected-service-token-never-echo")
			token, err = source.Token(context.Background())
			require.NoError(t, err)
			require.Equal(t, "newly-authorized-service-token", token)
			require.EqualValues(t, 2, requests.Load(), "the rejected token must not be cached")
			token, err = source.Token(context.Background())
			require.NoError(t, err)
			require.Equal(t, "newly-authorized-service-token", token)
			require.EqualValues(t, 2, requests.Load(), "the subsequent valid token is cached")
		})
	}
}
