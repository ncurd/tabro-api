package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	ws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type mediaRealtimeSpeechAPI interface {
	StreamAudioTranscription(context.Context, service.MediaRequestMeta, *service.Account, string, []byte, service.SpeechSocket) error
	ForwardQwenRealtimeSpeech(context.Context, service.MediaRequestMeta, *service.Account, service.AzureSpeechRequest) (*service.MediaSyncAudioResult, []byte, http.Header, error)
}

func (h *MediaGenerationHandler) AudioTranscriptionStream(c *gin.Context) {
	key, subject, ok := mediaAuthContext(c)
	if !ok {
		return
	}
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		mediaError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	realtime, ok := h.audioService.(mediaRealtimeSpeechAPI)
	if !ok {
		mediaError(c, http.StatusServiceUnavailable, "api_error", "Realtime speech is not configured")
		return
	}
	account, release, ok := h.selectAccount(c, key, subject, model, service.PlatformDashScope)
	if !ok {
		return
	}
	defer release()
	setOpsRequestContext(c, model, true, nil)
	c.Set(opsAccountIDKey, account.ID)
	setOpsEndpointContext(c, "", int16(service.RequestTypeWSV2))
	conn, err := ws.Accept(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(256 * 1024)
	startCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	kind, start, err := conn.Read(startCtx)
	cancel()
	if err == nil && kind != ws.MessageText {
		err = &service.InvalidMediaRequestError{Message: "first frame must be a run-task JSON event"}
	}
	if err == nil {
		meta := mediaMeta(c, key, subject, nil)
		meta.OperationID = c.GetHeader("Idempotency-Key")
		err = realtime.StreamAudioTranscription(c.Request.Context(), meta, account, model, start, conn)
	}
	if err != nil {
		failure := service.DescribeRealtimeSpeechFailure(err)
		recordRealtimeSpeechFailure(c, key, account, model, failure)
		body, _ := json.Marshal(map[string]any{"header": map[string]any{
			"event": "task-failed", "task_id": gjson.GetBytes(start, "header.task_id").String(),
			"error_code": failure.Code, "error_message": failure.Message, "error_stage": failure.Stage,
			"upstream_status": failure.UpstreamStatusCode, "provider_code": failure.ProviderCode,
		}, "payload": map[string]any{}})
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Second)
		_ = conn.Write(writeCtx, ws.MessageText, body)
		cancel()
	}
	_ = conn.Close(ws.StatusNormalClosure, "speech task ended")
}
