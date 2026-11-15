package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	ws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type modelCatalogPolicyStub struct {
	failure  error
	disabled string
}

func (p modelCatalogPolicyStub) IsModelEnabled(_ context.Context, model string) (bool, error) {
	disabled := p.disabled
	if disabled == "" {
		disabled = "disabled"
	}
	return !strings.EqualFold(model, disabled), p.failure
}

func TestDisabledResponsesWebSocketModelCannotReachAccountSelection(t *testing.T) {
	for _, tc := range []struct{ name, disabled, payload string }{
		{"main", "disabled", `{"type":"response.create","model":"disabled","input":"test"}`},
		{"image tool", "gpt-image-2", `{"type":"response.create","model":"enabled","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2"}]}`},
		{"default image tool", "gpt-image-2", `{"type":"response.create","model":"enabled","input":"draw","tools":[{"type":"image_generation"}]}`},
		{"native image model with disabled tool", "gpt-image-2", `{"type":"response.create","model":"gpt-image-2.5-flare","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2"}]}`},
		{"ambiguous native tools", "gpt-image-2", `{"type":"response.create","model":"enabled","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2.5-flare"}],"tools":[{"type":"image_generation","model":"gpt-image-2"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
			h.apiKeyService = &service.APIKeyService{ModelPolicies: modelCatalogPolicyStub{disabled: tc.disabled}}
			server := newOpenAIWSHandlerTestServer(t, h, middleware2.AuthSubject{UserID: 1, Concurrency: 1})
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, _, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
			require.NoError(t, err)
			defer func() { _ = conn.CloseNow() }()
			require.NoError(t, conn.Write(ctx, ws.MessageText, []byte(tc.payload)))
			_, _, err = conn.Read(ctx)
			var closed ws.CloseError
			require.ErrorAs(t, err, &closed)
			require.Equal(t, ws.StatusPolicyViolation, closed.Code)
			require.Contains(t, closed.Reason, "disabled")
		})
	}
}
func (p modelCatalogPolicyStub) FilterEnabledModels(_ context.Context, models []string) ([]string, error) {
	var result []string
	for _, m := range models {
		if !strings.EqualFold(m, "disabled") {
			result = append(result, m)
		}
	}
	return result, p.failure
}

func TestModelCatalogFiltersFreshPolicyAndPreservesProtocolMetadata(t *testing.T) {
	for _, google := range []bool{false, true} {
		t.Run(map[bool]string{false: "openai_claude", true: "gemini"}[google], func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/models", nil)
			c.Request = c.Request.WithContext(service.WithModelAvailabilityPolicy(c.Request.Context(), modelCatalogPolicyStub{}))
			key, id := "data", "id"
			if google {
				key, id = "models", "name"
			}
			prefix := ""
			if google {
				prefix = "models/"
			}
			writeModelPolicyCatalog(c, gin.H{key: []gin.H{{id: prefix + "enabled", "speech_tasks": []string{"transcription.streaming"}}, {id: prefix + "disabled"}}, "nextPageToken": "keep"}, google)
			require.Equal(t, http.StatusOK, w.Code)
			require.Contains(t, w.Body.String(), "enabled")
			require.NotContains(t, w.Body.String(), "disabled")
			require.Contains(t, w.Body.String(), "transcription.streaming")
			require.Contains(t, w.Body.String(), "keep")
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		})
	}
}

func TestModelCatalogPolicyReadFailureCannotPublishStaleModels(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/models", nil)
	c.Request = c.Request.WithContext(service.WithModelAvailabilityPolicy(c.Request.Context(), modelCatalogPolicyStub{failure: errors.New("storage failed")}))
	writeModelPolicyCatalog(c, gin.H{"data": []gin.H{{"id": "enabled"}}}, false)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.NotContains(t, w.Body.String(), `"id":"enabled"`)
}

func TestUpstreamModelCatalogFiltersWithoutChangingOriginalResult(t *testing.T) {
	for _, headers := range []http.Header{nil, {"Etag": []string{"old"}, "Cache-Control": []string{"public, max-age=60"}}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
		c.Request = c.Request.WithContext(service.WithModelAvailabilityPolicy(c.Request.Context(), modelCatalogPolicyStub{}))
		body := []byte(`{"models":[{"name":"models/enabled"},{"name":"models/disabled"}],"nextPageToken":"keep"}`)
		upstream := &service.UpstreamHTTPResult{StatusCode: http.StatusOK, Headers: headers, Body: body}
		writeModelPolicyUpstreamCatalog(c, upstream)
		require.Equal(t, http.StatusOK, w.Code)
		require.NotContains(t, w.Body.String(), "disabled")
		require.Contains(t, w.Body.String(), "nextPageToken")
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		require.Empty(t, w.Header().Get("ETag"))
		require.Equal(t, body, upstream.Body)
		if headers != nil {
			require.Equal(t, "old", headers.Get("ETag"))
		}
	}
}
