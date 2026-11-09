package repository

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestClaudeOAuthCurrentTokenEndpointAndProtocol(t *testing.T) {
	for _, operation := range []string{"exchange", "setup_exchange", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			var captured *http.Request
			var payload map[string]any
			transport := newInProcessTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":28800,"token_type":"bearer"}`))
			}), func(r *http.Request, body []byte) {
				captured = r.Clone(r.Context())
				require.NoError(t, json.Unmarshal(body, &payload))
			})
			client := NewClaudeOAuthClient().(*claudeOAuthService)
			client.clientFactory = func(proxy string) (*req.Client, error) {
				require.Equal(t, "http://proxy.example:8080", proxy)
				return newTestReqClient(transport), nil
			}
			var result *oauth.TokenResponse
			var err error
			if operation == "refresh" {
				result, err = client.RefreshToken(context.Background(), "old-refresh", "http://proxy.example:8080")
			} else {
				result, err = client.ExchangeCodeForToken(context.Background(), "AUTH#STATE2", "verifier", "STATE1", "http://proxy.example:8080", operation == "setup_exchange")
			}
			require.NoError(t, err)
			require.Equal(t, "new-access", result.AccessToken)
			require.Equal(t, "new-refresh", result.RefreshToken)
			require.Equal(t, "https://platform.claude.com/v1/oauth/token", captured.URL.String())
			require.Equal(t, http.MethodPost, captured.Method)
			require.Equal(t, "application/json", captured.Header.Get("Content-Type"))
			require.Equal(t, "application/json, text/plain, */*", captured.Header.Get("Accept"))
			require.Equal(t, "axios/1.15.2", captured.Header.Get("User-Agent"))
			require.Empty(t, captured.Header.Get("Authorization"))
			require.Equal(t, oauth.ClientID, payload["client_id"])
			if operation == "refresh" {
				require.Equal(t, "refresh_token", payload["grant_type"])
				require.Equal(t, "old-refresh", payload["refresh_token"])
				require.NotContains(t, payload, "code")
			} else {
				require.Equal(t, "authorization_code", payload["grant_type"])
				require.Equal(t, "AUTH", payload["code"])
				require.Equal(t, "STATE2", payload["state"])
				require.Equal(t, "verifier", payload["code_verifier"])
				require.Equal(t, oauth.RedirectURI, payload["redirect_uri"])
				if operation == "setup_exchange" {
					require.Equal(t, float64(31536000), payload["expires_in"])
				} else {
					require.NotContains(t, payload, "expires_in")
				}
			}
		})
	}
}
