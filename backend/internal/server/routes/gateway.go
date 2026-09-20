package routes

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterGatewayRoutes 注册 API 网关路由（Claude/OpenAI/Gemini 兼容）
func RegisterGatewayRoutes(
	r *gin.Engine,
	h *handler.Handlers,
	apiKeyAuth middleware.APIKeyAuthMiddleware,
	authService *service.AuthService,
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	opsService *service.OpsService,
	settingService *service.SettingService,
	cfg *config.Config,
) {
	bodyLimit := middleware.RequestBodyLimit(cfg.Gateway.MaxBodySize)
	clientRequestID := middleware.ClientRequestID()
	opsErrorLogger := handler.OpsErrorLoggerMiddleware(opsService)
	endpointNorm := handler.InboundEndpointMiddleware()

	// 未分组 Key 拦截中间件（按协议格式区分错误响应）
	requireGroupAnthropic := middleware.RequireGroupAssignment(settingService, middleware.AnthropicErrorWriter)
	requireGroupGoogle := middleware.RequireGroupAssignment(settingService, middleware.GoogleErrorWriter)
	imagesEditsHandler := func(c *gin.Context) {
		if platform := getGroupPlatform(c); platform == "" || platform == service.PlatformOpenAI {
			h.OpenAIGateway.ImagesEdits(c)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
			"type": "not_found_error", "message": "Image editing is not supported for this platform",
		}})
	}

	// API网关（Claude API兼容）
	gateway := r.Group("/v1")
	gateway.Use(bodyLimit)
	gateway.Use(clientRequestID)
	gateway.Use(opsErrorLogger)
	gateway.Use(endpointNorm)
	gateway.Use(gin.HandlerFunc(apiKeyAuth))
	gateway.Use(middleware.GatewayInvocationIdempotency(middleware.AnthropicErrorWriter))
	gateway.Use(requireGroupAnthropic)
	{
		// /v1/messages: auto-route based on group platform
		gateway.POST("/messages", func(c *gin.Context) {
			if getGroupPlatform(c) == service.PlatformOpenAI {
				h.OpenAIGateway.Messages(c)
				return
			}
			h.Gateway.Messages(c)
		})
		// /v1/messages/count_tokens: OpenAI groups get 404
		gateway.POST("/messages/count_tokens", func(c *gin.Context) {
			if getGroupPlatform(c) == service.PlatformOpenAI {
				c.JSON(http.StatusNotFound, gin.H{
					"type": "error",
					"error": gin.H{
						"type":    "not_found_error",
						"message": "Token counting is not supported for this platform",
					},
				})
				return
			}
			h.Gateway.CountTokens(c)
		})
		gateway.GET("/models", h.Gateway.Models)
		gateway.GET("/usage", h.Gateway.Usage)
		// OpenAI Responses API: auto-route based on group platform
		gateway.POST("/responses", func(c *gin.Context) {
			if getGroupPlatform(c) == service.PlatformOpenAI {
				h.OpenAIGateway.Responses(c)
				return
			}
			h.Gateway.Responses(c)
		})
		gateway.POST("/responses/*subpath", func(c *gin.Context) {
			if getGroupPlatform(c) == service.PlatformOpenAI {
				h.OpenAIGateway.Responses(c)
				return
			}
			h.Gateway.Responses(c)
		})
		gateway.GET("/responses", h.OpenAIGateway.ResponsesWebSocket)
		// OpenAI Chat Completions API: auto-route based on group platform
		gateway.POST("/chat/completions", func(c *gin.Context) {
			if getGroupPlatform(c) == service.PlatformOpenAI {
				h.OpenAIGateway.ChatCompletions(c)
				return
			}
			h.Gateway.ChatCompletions(c)
		})
		gateway.POST("/images/generations", func(c *gin.Context) {
			if platform := getGroupPlatform(c); platform == "" || platform == service.PlatformOpenAI {
				h.OpenAIGateway.ImagesGenerations(c)
				return
			}
			c.JSON(http.StatusNotFound, gin.H{
				"type": "error",
				"error": gin.H{
					"type":    "not_found_error",
					"message": "Images generation is not supported for this platform",
				},
			})
		})
		gateway.POST("/images/edits", imagesEditsHandler)
		gateway.POST("/audio/transcriptions", h.MediaGeneration.AudioTranscriptions)
		gateway.POST("/audio/voices", h.MediaGeneration.CreateClonedVoice)
		gateway.GET("/audio/voices", h.MediaGeneration.ListClonedVoices)
		gateway.GET("/audio/voices/:id", h.MediaGeneration.GetClonedVoice)
		gateway.DELETE("/audio/voices/:id", h.MediaGeneration.DeleteClonedVoice)
		gateway.POST("/audio/speech", h.MediaGeneration.AudioSpeech)
		gateway.POST("/audio/speech/jobs", h.MediaGeneration.CreateAudioSpeechJob)
		gateway.GET("/audio/speech/jobs/:id", h.MediaGeneration.GetAudioSpeechJob)
		gateway.POST("/videos/generations", h.MediaGeneration.CreateVideoGeneration)
		gateway.GET("/videos/generations/:id", h.MediaGeneration.GetVideoGeneration)
	}

	// Gemini 原生 API 兼容层（Gemini SDK/CLI 直连）
	gemini := r.Group("/v1beta")
	gemini.Use(bodyLimit)
	gemini.Use(clientRequestID)
	gemini.Use(opsErrorLogger)
	gemini.Use(endpointNorm)
	gemini.Use(middleware.GatewayAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, authService, cfg))
	gemini.Use(middleware.GatewayInvocationIdempotency(middleware.GoogleErrorWriter))
	gemini.Use(requireGroupGoogle)
	{
		gemini.GET("/models", h.Gateway.GeminiV1BetaListModels)
		gemini.GET("/models/:model", h.Gateway.GeminiV1BetaGetModel)
		// Gin treats ":" as a param marker, but Gemini uses "{model}:{action}" in the same segment.
		gemini.POST("/models/*modelAction", h.Gateway.GeminiV1BetaModels)
	}

	// OpenAI Responses API（不带v1前缀的别名）— auto-route based on group platform
	responsesHandler := func(c *gin.Context) {
		if getGroupPlatform(c) == service.PlatformOpenAI {
			h.OpenAIGateway.Responses(c)
			return
		}
		h.Gateway.Responses(c)
	}
	r.POST("/responses", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), middleware.GatewayInvocationIdempotency(middleware.AnthropicErrorWriter), requireGroupAnthropic, responsesHandler)
	r.POST("/responses/*subpath", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), middleware.GatewayInvocationIdempotency(middleware.AnthropicErrorWriter), requireGroupAnthropic, responsesHandler)
	r.GET("/responses", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.OpenAIGateway.ResponsesWebSocket)
	// OpenAI Chat Completions API（不带v1前缀的别名）— auto-route based on group platform
	r.POST("/chat/completions", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), middleware.GatewayInvocationIdempotency(middleware.AnthropicErrorWriter), requireGroupAnthropic, func(c *gin.Context) {
		if getGroupPlatform(c) == service.PlatformOpenAI {
			h.OpenAIGateway.ChatCompletions(c)
			return
		}
		h.Gateway.ChatCompletions(c)
	})
	r.POST("/images/generations", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), middleware.GatewayInvocationIdempotency(middleware.AnthropicErrorWriter), requireGroupAnthropic, func(c *gin.Context) {
		if platform := getGroupPlatform(c); platform == "" || platform == service.PlatformOpenAI {
			h.OpenAIGateway.ImagesGenerations(c)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "not_found_error",
				"message": "Images generation is not supported for this platform",
			},
		})
	})
	r.POST("/images/edits", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), middleware.GatewayInvocationIdempotency(middleware.AnthropicErrorWriter), requireGroupAnthropic, imagesEditsHandler)
	r.POST("/audio/transcriptions", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), middleware.GatewayInvocationIdempotency(middleware.AnthropicErrorWriter), requireGroupAnthropic, h.MediaGeneration.AudioTranscriptions)
	r.POST("/audio/voices", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), middleware.GatewayInvocationIdempotency(middleware.AnthropicErrorWriter), requireGroupAnthropic, h.MediaGeneration.CreateClonedVoice)
	r.GET("/audio/voices", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.MediaGeneration.ListClonedVoices)
	r.GET("/audio/voices/:id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.MediaGeneration.GetClonedVoice)
	r.DELETE("/audio/voices/:id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.MediaGeneration.DeleteClonedVoice)
	r.POST("/audio/speech", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), middleware.GatewayInvocationIdempotency(middleware.AnthropicErrorWriter), requireGroupAnthropic, h.MediaGeneration.AudioSpeech)
	r.POST("/audio/speech/jobs", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), middleware.GatewayInvocationIdempotency(middleware.AnthropicErrorWriter), requireGroupAnthropic, h.MediaGeneration.CreateAudioSpeechJob)
	r.GET("/audio/speech/jobs/:id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.MediaGeneration.GetAudioSpeechJob)
	r.POST("/videos/generations", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), middleware.GatewayInvocationIdempotency(middleware.AnthropicErrorWriter), requireGroupAnthropic, h.MediaGeneration.CreateVideoGeneration)
	r.GET("/videos/generations/:id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.MediaGeneration.GetVideoGeneration)

	// Antigravity 模型列表
	r.GET("/antigravity/models", gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.Gateway.AntigravityModels)

	// Antigravity 专用路由（仅使用 antigravity 账户，不混合调度）
	antigravityV1 := r.Group("/antigravity/v1")
	antigravityV1.Use(bodyLimit)
	antigravityV1.Use(clientRequestID)
	antigravityV1.Use(opsErrorLogger)
	antigravityV1.Use(endpointNorm)
	antigravityV1.Use(middleware.ForcePlatform(service.PlatformAntigravity))
	antigravityV1.Use(gin.HandlerFunc(apiKeyAuth))
	antigravityV1.Use(middleware.GatewayInvocationIdempotency(middleware.AnthropicErrorWriter))
	antigravityV1.Use(requireGroupAnthropic)
	{
		antigravityV1.POST("/messages", h.Gateway.Messages)
		antigravityV1.POST("/messages/count_tokens", h.Gateway.CountTokens)
		antigravityV1.GET("/models", h.Gateway.AntigravityModels)
		antigravityV1.GET("/usage", h.Gateway.Usage)
	}

	antigravityV1Beta := r.Group("/antigravity/v1beta")
	antigravityV1Beta.Use(bodyLimit)
	antigravityV1Beta.Use(clientRequestID)
	antigravityV1Beta.Use(opsErrorLogger)
	antigravityV1Beta.Use(endpointNorm)
	antigravityV1Beta.Use(middleware.ForcePlatform(service.PlatformAntigravity))
	antigravityV1Beta.Use(middleware.GatewayAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, authService, cfg))
	antigravityV1Beta.Use(middleware.GatewayInvocationIdempotency(middleware.GoogleErrorWriter))
	antigravityV1Beta.Use(requireGroupGoogle)
	{
		antigravityV1Beta.GET("/models", h.Gateway.GeminiV1BetaListModels)
		antigravityV1Beta.GET("/models/:model", h.Gateway.GeminiV1BetaGetModel)
		antigravityV1Beta.POST("/models/*modelAction", h.Gateway.GeminiV1BetaModels)
	}

}

// getGroupPlatform extracts the group platform from the API Key stored in context.
func getGroupPlatform(c *gin.Context) string {
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil {
		return ""
	}
	return apiKey.Group.Platform
}
