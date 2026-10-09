package middleware

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// Resolve owned pinned resources before reserving funds or choosing any new
// pool. Resource IDs alone never grant access; the service checks owner, key,
// current group permissions and, for synthesis, the original account.
func resolveGatewayMediaResourceRequest(c *gin.Context, keys *service.APIKeyService, key *service.APIKey, request service.GatewayGroupRequest) (*service.APIKey, bool, error) {
	path := strings.TrimPrefix(request.Path, "/v1")
	id, kind, model := "", "", ""
	if request.MetadataOnly {
		switch {
		case strings.HasPrefix(path, "/audio/voices/"):
			id, kind = strings.TrimPrefix(path, "/audio/voices/"), service.MediaJobKindVoiceClone
		case c.Request.Method == http.MethodGet && strings.HasPrefix(path, "/audio/speech/jobs/"):
			id, kind = strings.TrimPrefix(path, "/audio/speech/jobs/"), service.MediaJobKindAudioSpeech
		case c.Request.Method == http.MethodGet && strings.HasPrefix(path, "/videos/generations/"):
			id, kind = strings.TrimPrefix(path, "/videos/generations/"), service.MediaJobKindVideoGeneration
		}
	} else if c.Request.Method == http.MethodPost && path == "/audio/speech" && strings.HasPrefix(request.Voice, "voice_") {
		id, kind, model = request.Voice, service.MediaJobKindVoiceClone, request.Model
		if model == "" {
			return nil, true, errGatewayRoutingBody
		}
	}
	if id == "" {
		return nil, false, nil
	}
	if strings.Contains(id, "/") {
		return nil, true, service.ErrGatewayMediaResourceNotFound
	}
	resolved, _, err := keys.ResolveGatewayMediaResource(c.Request.Context(), key, id, kind, model)
	if err == nil {
		setGroupContext(c, resolved.Group)
	}
	return resolved, true, err
}
