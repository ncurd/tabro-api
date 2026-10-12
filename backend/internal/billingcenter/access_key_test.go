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

func validAccessKeyResponse() AccessKeyValidation {
	return AccessKeyValidation{KeyID: "access-key", Version: 3, UserID: "member", TenantID: "team", OriginAppID: "tabro-api", Mode: "direct_api", TargetAppID: "tabro-api", Permissions: []string{"api.read", "api.invoke"}, SubjectAccessToken: "short-subject-proof"}
}

func TestAccessKeyValidationUsesServiceBearerAndNeverCachesAuthorization(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/internal/access-keys/v1/validate", r.URL.Path)
		require.Equal(t, "Bearer producer-service-token", r.Header.Get("Authorization"))
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, map[string]string{"key": "tbrk_opaque-user-key", "required_permission": "api.read", "mode": "direct_api", "target_app_id": "tabro-api"}, body)
		if calls.Add(1) == 3 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"private tbrk_opaque-user-key revoked"}`))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(validAccessKeyResponse()))
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{ProducerClientID: "gateway", BaseURL: server.URL, InsecureLocal: true}, tokenFunc(func(context.Context) (string, error) { return "producer-service-token", nil }), nil)
	require.NoError(t, err)
	for range 2 {
		result, err := client.ValidateAccessKey(context.Background(), "tbrk_opaque-user-key", "api.read")
		require.NoError(t, err)
		require.Equal(t, "access-key", result.KeyID)
	}
	_, err = client.ValidateAccessKey(context.Background(), "tbrk_opaque-user-key", "api.read")
	require.Error(t, err)
	require.EqualValues(t, 3, calls.Load())
	require.NotContains(t, err.Error(), "private")
	require.NotContains(t, err.Error(), "tbrk_")
}

func TestAccessKeyValidationRejectsWrongPurposeAndMalformedAuthorityResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*AccessKeyValidation)
	}{
		{"wrong mode", func(v *AccessKeyValidation) { v.Mode = "business_app" }},
		{"wrong target", func(v *AccessKeyValidation) { v.TargetAppID = "tabro-agent" }},
		{"wrong origin", func(v *AccessKeyValidation) { v.OriginAppID = "tabro-agent" }},
		{"no permission", func(v *AccessKeyValidation) { v.Permissions = []string{"api.invoke"} }},
		{"no tenant", func(v *AccessKeyValidation) { v.TenantID = "" }},
		{"no version", func(v *AccessKeyValidation) { v.Version = 0 }},
		{"no proof", func(v *AccessKeyValidation) { v.SubjectAccessToken = "" }},
		{"expired", func(v *AccessKeyValidation) { expired := time.Now().Add(-time.Minute); v.ExpiresAt = &expired }},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				v := validAccessKeyResponse()
				test.edit(&v)
				require.NoError(t, json.NewEncoder(w).Encode(v))
			}))
			t.Cleanup(server.Close)
			client, err := NewClient(Config{ProducerClientID: "gateway", BaseURL: server.URL, InsecureLocal: true}, tokenFunc(func(context.Context) (string, error) { return "producer", nil }), nil)
			require.NoError(t, err)
			_, err = client.ValidateAccessKey(context.Background(), "tbrk_opaque", "api.read")
			require.Error(t, err)
		})
	}
	client, err := NewClient(Config{ProducerClientID: "gateway", BaseURL: "https://auth.invalid"}, tokenFunc(func(context.Context) (string, error) { t.Fatal("invalid keys must not fetch a token"); return "", nil }), nil)
	require.NoError(t, err)
	for _, key := range []string{"local-key", "tbrk_" + strings.Repeat("x", 512)} {
		_, err := client.ValidateAccessKey(context.Background(), key, "api.read")
		require.Error(t, err)
	}
	_, err = client.ValidateAccessKey(context.Background(), "tbrk_opaque", "billing.credit")
	require.Error(t, err)
}
