package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var errGatewayRoutingBody = errors.New("invalid model routing request")

// resolveRequestGatewayGroup runs after credential/user/IP validation and
// before any admission quote. It restores the body for billing and forwarding.
func resolveRequestGatewayGroup(c *gin.Context, keys *service.APIKeyService, key *service.APIKey) (*service.APIKey, error) {
	if key.EffectiveGroupScope() == service.APIKeyGroupScopeSingle && (key.Group == nil || key.Group.Platform != service.PlatformAll) {
		return key, nil
	}
	if c.Request.Method == http.MethodGet && strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
		c.Request = c.Request.WithContext(service.WithGatewayRoutingKey(c.Request.Context(), key))
	}
	request := service.GatewayGroupRequest{Path: c.Request.URL.Path}
	request.ForcePlatform, _ = c.Request.Context().Value(ctxkey.ForcePlatform).(string)
	request.Catalog = c.Request.Method == http.MethodGet && gatewayModelCatalogPath(request.Path)
	request.MetadataOnly = (c.Request.Method == http.MethodGet && !request.Catalog && !strings.EqualFold(c.GetHeader("Upgrade"), "websocket")) || c.Request.Method == http.MethodDelete
	if !request.Catalog && !request.MetadataOnly {
		if strings.Contains(request.Path, "/v1beta/models/") {
			request.Model = strings.SplitN(strings.TrimPrefix(strings.SplitN(request.Path, "/models/", 2)[1], "models/"), ":", 2)[0]
		} else if c.Request.Method == http.MethodPost && c.Request.Body != nil {
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, 32*1024*1024+1))
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			if err != nil || len(body) > 32*1024*1024 {
				return nil, errGatewayRoutingBody
			}
			mediaType, params, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
			if mediaType == "multipart/form-data" {
				reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
				for {
					part, partErr := reader.NextPart()
					if partErr == io.EOF {
						break
					}
					if partErr != nil {
						return nil, errGatewayRoutingBody
					}
					if part.FormName() == "model" && part.FileName() == "" {
						value, valueErr := io.ReadAll(io.LimitReader(part, 1025))
						if valueErr != nil || len(value) > 1024 {
							return nil, errGatewayRoutingBody
						}
						request.Model = string(value)
					}
					_ = part.Close()
				}
			} else {
				var input struct {
					Model string `json:"model"`
					Voice string `json:"voice"`
				}
				if json.Unmarshal(body, &input) != nil {
					return nil, errGatewayRoutingBody
				}
				request.Model, request.Voice = input.Model, input.Voice
			}
		}
	}
	if resolved, handled, err := resolveGatewayMediaResourceRequest(c, keys, key, request); handled {
		return resolved, err
	}
	effective, groups, err := keys.ResolveGatewayGroup(c.Request.Context(), key, request)
	if err != nil {
		return nil, err
	}
	if request.Catalog {
		c.Request = c.Request.WithContext(service.WithGatewayModelGroups(c.Request.Context(), groups))
	}
	setGroupContext(c, effective.Group)
	return effective, nil
}

func gatewayModelCatalogPath(path string) bool {
	return strings.HasSuffix(path, "/models") || strings.Contains(path, "/v1beta/models/")
}

func gatewayGroupRoutingError(err error) (int, string, string) {
	if errors.Is(err, service.ErrGatewayMediaResourceNotFound) {
		return http.StatusNotFound, "MEDIA_RESOURCE_NOT_FOUND", "Media resource not found"
	}
	if errors.Is(err, errGatewayRoutingBody) {
		return http.StatusBadRequest, "INVALID_REQUEST", "Request must contain valid model routing parameters"
	}
	if errors.Is(err, service.ErrGroupNotAllowed) {
		return http.StatusForbidden, "GROUP_NOT_ALLOWED", "API key does not grant an active model group"
	}
	return http.StatusServiceUnavailable, "GATEWAY_ROUTE_UNAVAILABLE", "No available account in an allowed group for this model"
}
