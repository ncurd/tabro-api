package billingcenter

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// AccessKeyValidation is transient authorization from Auth. Neither the opaque
// key nor its short subject proof belongs in gateway storage or auth caches.
type AccessKeyValidation struct {
	KeyID              string     `json:"key_id"`
	Version            int64      `json:"version"`
	UserID             string     `json:"user_id"`
	TenantID           string     `json:"tenant_id"`
	OriginAppID        string     `json:"origin_app_id"`
	Mode               string     `json:"mode"`
	TargetAppID        string     `json:"target_app_id"`
	Permissions        []string   `json:"permissions"`
	SubjectAccessToken string     `json:"subject_access_token"`
	ExpiresAt          *time.Time `json:"expires_at"`
}

func (c *Client) ValidateAccessKey(ctx context.Context, key, permission string) (AccessKeyValidation, error) {
	var result AccessKeyValidation
	if !strings.HasPrefix(key, "tbrk_") || len(key) > 512 || (permission != "api.invoke" && permission != "api.read") {
		return result, ErrConflict
	}
	body, _ := json.Marshal(struct {
		Key                string `json:"key"`
		RequiredPermission string `json:"required_permission"`
		Mode               string `json:"mode"`
		TargetAppID        string `json:"target_app_id"`
	}{key, permission, "direct_api", "tabro-api"})
	data, err := c.requestJSON(ctx, http.MethodPost, "/internal/access-keys/v1/validate", nil, body)
	if err != nil {
		return result, err
	}
	if json.Unmarshal(data, &result) != nil || result.KeyID == "" || result.Version <= 0 || result.UserID == "" || result.TenantID == "" || result.OriginAppID != "tabro-api" || result.Mode != "direct_api" || result.TargetAppID != "tabro-api" || result.SubjectAccessToken == "" || len(result.SubjectAccessToken) > 16*1024 {
		return AccessKeyValidation{}, &RemoteError{Retryable: true}
	}
	if result.ExpiresAt != nil && !result.ExpiresAt.After(time.Now()) {
		return AccessKeyValidation{}, &RemoteError{Status: http.StatusForbidden}
	}
	for _, granted := range result.Permissions {
		if granted == permission {
			return result, nil
		}
	}
	return AccessKeyValidation{}, &RemoteError{Status: http.StatusForbidden}
}
