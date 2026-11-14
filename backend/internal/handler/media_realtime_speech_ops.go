package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func recordRealtimeSpeechFailure(c *gin.Context, key *service.APIKey, account *service.Account, model string, failure service.RealtimeSpeechFailure) {
	requestID := c.Writer.Header().Get("X-Request-Id")
	clientID, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
	phase, owner := "internal", "gateway"
	if strings.HasPrefix(failure.Stage, "upstream") || failure.Stage == "protocol" {
		phase, owner = "upstream", "provider"
	} else if failure.Stage == "billing" {
		phase = "billing"
	}
	detail, _ := json.Marshal(map[string]any{
		"transport": "websocket", "handshake_status": http.StatusSwitchingProtocols,
		"code": failure.Code, "stage": failure.Stage,
		"upstream_status": failure.UpstreamStatusCode, "provider_code": failure.ProviderCode,
	})
	detailText, message := string(detail), failure.Code+": "+failure.Message
	requestType := int16(service.RequestTypeWSV2)
	entry := &service.OpsInsertErrorLogInput{
		RequestID: requestID, ClientRequestID: clientID,
		AccountID: &account.ID, Platform: account.Platform, Model: model, RequestedModel: model,
		RequestPath: c.Request.URL.Path, InboundEndpoint: GetInboundEndpoint(c),
		UpstreamEndpoint: "/api-ws/v1/inference", Stream: true, RequestType: &requestType,
		UserAgent: c.GetHeader("User-Agent"), ErrorPhase: phase, ErrorType: "websocket_task_error",
		Severity: classifyOpsSeverity("upstream_error", failure.StatusCode),
		// Keep the actual HTTP result. The task failure is represented by its
		// explicit type and safe detail rather than inventing an HTTP 5xx.
		StatusCode:   http.StatusSwitchingProtocols,
		ErrorMessage: message, ErrorBody: detailText, ErrorSource: "websocket", ErrorOwner: owner,
		UpstreamErrorDetail: &detailText, IsRetryable: false, CreatedAt: time.Now(),
	}
	if failure.UpstreamStatusCode > 0 {
		status := failure.UpstreamStatusCode
		entry.UpstreamStatusCode = &status
	}
	if phase == "upstream" {
		entry.UpstreamErrorMessage = &message
	}
	if key != nil {
		entry.UserID, entry.APIKeyID, entry.GroupID = &key.UserID, &key.ID, key.GroupID
	}
	// No run-task body, text, audio, credentials or provider response is captured.
	c.Set(opsWebSocketFailureKey, entry)
	log.Printf("[Speech] realtime task failed request=%q code=%q stage=%q model=%q account=%d upstream_http=%d provider_code=%q",
		requestID, failure.Code, failure.Stage, model, account.ID, failure.UpstreamStatusCode, failure.ProviderCode)
}
