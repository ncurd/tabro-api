package middleware

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayModelPolicyStub struct {
	disabled string
	failure  error
}

func (p *gatewayModelPolicyStub) IsModelEnabled(_ context.Context, model string) (bool, error) {
	return !strings.EqualFold(model, p.disabled), p.failure
}
func (p *gatewayModelPolicyStub) FilterEnabledModels(_ context.Context, models []string) ([]string, error) {
	return models, p.failure
}

func TestDisabledModelIsRejectedForAuthenticatedTokenBeforeRoutingOrBilling(t *testing.T) {
	fixture := newGatewayMiddlewareOAuthFixture(t)
	policy := &gatewayModelPolicyStub{disabled: "paraformer-realtime-v2"}
	fixture.apiKeys.ModelPolicies = policy
	token := fixture.sign(t, fixture.claims())
	for _, google := range []bool{false, true} {
		t.Run(map[bool]string{false: "standard", true: "google"}[google], func(t *testing.T) {
			router := gin.New()
			if google {
				router.Use(GatewayAuthWithSubscriptionGoogle(fixture.apiKeys, nil, nil, fixture.cfg))
			} else {
				router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(fixture.apiKeys, nil, nil, fixture.cfg)))
			}
			calls := 0
			router.POST("/v1/audio/speech", func(c *gin.Context) { calls++; c.Status(http.StatusNoContent) })
			request := func(field string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(`{"`+field+`":"paraformer-realtime-v2"}`))
				req.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				return w
			}
			policy.disabled, policy.failure = "paraformer-realtime-v2", nil
			for _, field := range []string{"model", "Model", "MODEL"} {
				require.Equal(t, http.StatusForbidden, request(field).Code)
			}
			require.Zero(t, calls)
			// Existing key/JWKS/auth caches cannot keep a newly enabled model blocked.
			policy.disabled = "another-model"
			require.Equal(t, http.StatusNoContent, request("model").Code)
			require.Equal(t, 1, calls)
			policy.failure = errors.New("storage unavailable")
			require.Equal(t, http.StatusServiceUnavailable, request("model").Code)
			require.Equal(t, 1, calls, "a failed policy read must not invoke the supplier")
			policy.failure = nil
		})
	}
}

func TestModelPolicyFindsModelsWithoutConsumingBodiesOrBlockingExistingJobs(t *testing.T) {
	for _, test := range []struct {
		method, path, body, want string
		websocket                bool
	}{
		{http.MethodPost, "/v1/chat/completions", `{"model":"text","messages":[]}`, "text", false},
		{http.MethodPost, "/v1/audio/speech", `{"Model":"speech","input":"test"}`, "speech", false},
		{http.MethodGet, "/v1/audio/transcriptions/stream?model=asr", "", "asr", true},
		{http.MethodPost, "/v1beta/models/gemini-test:generateContent", `{"contents":[]}`, "gemini-test", false},
		{http.MethodGet, "/v1beta/models/gemini-test", "", "gemini-test", false},
		{http.MethodGet, "/v1/models", "", "", false},
		{http.MethodGet, "/v1/audio/speech/jobs/already-paid", "", "", false},
		{http.MethodDelete, "/v1/audio/voices/existing", "", "", false},
	} {
		t.Run(test.path, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.websocket {
				c.Request.Header.Set("Upgrade", "websocket")
			}
			model, err := gatewayInvocationModel(c)
			require.NoError(t, err)
			require.Equal(t, test.want, model)
			if test.method == http.MethodPost && !strings.Contains(test.path, "/v1beta/") {
				body, err := readGatewayRequestBody(c)
				require.NoError(t, err)
				require.Equal(t, test.body, string(body))
			}
		})
	}
	for _, body := range []string{`{"model":"on","model":"off"}`, `{"model":"on","Model":"off"}`, `{"MODEL":"on","Model":"off"}`, `{"model":123}`, `{"Model":123}`} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		_, err := gatewayInvocationModel(c)
		require.Error(t, err, "ambiguous models must not bypass a disabled rule")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	require.NoError(t, form.WriteField("model", "audio"))
	require.NoError(t, form.Close())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body.Bytes()))
	c.Request.Header.Set("Content-Type", form.FormDataContentType())
	model, err := gatewayInvocationModel(c)
	require.NoError(t, err)
	require.Equal(t, "audio", model)
	retained, err := readGatewayRequestBody(c)
	require.NoError(t, err)
	require.Equal(t, body.Bytes(), retained)
}

func TestDisabledImageModelsAreCheckedBeforeRoutingOrBilling(t *testing.T) {
	fixture := newGatewayMiddlewareOAuthFixture(t)
	policy := &gatewayModelPolicyStub{}
	fixture.apiKeys.ModelPolicies = policy
	token := fixture.sign(t, fixture.claims())
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	require.NoError(t, form.WriteField("prompt", "edit"))
	image, err := form.CreateFormFile("image", "image.png")
	require.NoError(t, err)
	_, err = image.Write([]byte("image-data"))
	require.NoError(t, err)
	require.NoError(t, form.Close())
	for _, test := range []struct {
		name, path, body, contentType, disabled string
		status                                  int
	}{
		{"edit_json_default", "/v1/images/edits", `{"prompt":"edit","images":[{"file_id":"file-1"}]}`, "application/json", "gpt-image-2", http.StatusForbidden},
		{"edit_json_ignored_uppercase_uses_default", "/v1/images/edits", `{"Model":"text-enabled","prompt":"edit","images":[{"file_id":"file-1"}]}`, "application/json", "gpt-image-2", http.StatusForbidden},
		{"edit_multipart_default", "/images/edits", upload.String(), form.FormDataContentType(), "gpt-image-2", http.StatusForbidden},
		{"edit_explicit_model", "/v1/images/edits", `{"model":"custom-image","prompt":"edit","images":[{"file_id":"file-1"}]}`, "application/json", "custom-image", http.StatusForbidden},
		{"edit_enabled_override", "/v1/images/edits", `{"model":"custom-image","prompt":"edit","images":[{"file_id":"file-1"}]}`, "application/json", "gpt-image-2", http.StatusNoContent},
		{"bridge_selected_tool", "/v1/responses", `{"model":"text-enabled","input":"draw","tools":[{"type":"web_search"},{"type":"image_generation","model":"custom-image"}]}`, "application/json", "custom-image", http.StatusForbidden},
		{"bridge_default_tool", "/responses", `{"model":"text-enabled","input":"draw","tools":[{"type":"image_generation"}]}`, "application/json", "gpt-image-2", http.StatusForbidden},
		{"bridge_blank_tool_default", "/v1/responses", `{"model":"text-enabled","input":"draw","tools":[{"type":"image_generation","model":" "}]}`, "application/json", "gpt-image-2", http.StatusForbidden},
		{"bridge_direct_image_wins", "/v1/responses", `{"model":"gpt-image-custom","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2"}]}`, "application/json", "gpt-image-2", http.StatusNoContent},
		{"native_responses_tool", "/v1/responses", `{"model":"text-enabled","previous_response_id":"existing","input":"edit","tools":[{"type":"image_generation","model":"custom-image"}]}`, "application/json", "custom-image", http.StatusForbidden},
		{"native_responses_default_tool", "/v1/responses", `{"model":"text-enabled","previous_response_id":"existing","input":"edit","tools":[{"type":"image_generation"}]}`, "application/json", "gpt-image-2", http.StatusForbidden},
		{"native_responses_cased_tool", "/v1/responses", `{"model":"text-enabled","previous_response_id":"existing","input":"edit","Tools":[{"Type":"image_generation","Model":"custom-image"}]}`, "application/json", "custom-image", http.StatusForbidden},
		{"text_only_still_allowed", "/v1/responses", `{"model":"text-enabled","input":"hello"}`, "application/json", "gpt-image-2", http.StatusNoContent},
	} {
		for _, google := range []bool{false, true} {
			t.Run(test.name+map[bool]string{false: "/standard", true: "/google"}[google], func(t *testing.T) {
				policy.disabled = test.disabled
				router := gin.New()
				if google {
					router.Use(GatewayAuthWithSubscriptionGoogle(fixture.apiKeys, nil, nil, fixture.cfg))
				} else {
					router.Use(gin.HandlerFunc(NewGatewayAuthMiddleware(fixture.apiKeys, nil, nil, fixture.cfg)))
				}
				calls := 0
				router.POST(test.path, func(c *gin.Context) {
					calls++
					retained, err := readGatewayRequestBody(c)
					require.NoError(t, err)
					require.Equal(t, test.body, string(retained), "admission must retain the supplier body")
					c.Status(http.StatusNoContent)
				})
				req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
				req.Header.Set("Content-Type", test.contentType)
				req.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				require.Equal(t, test.status, w.Code, w.Body.String())
				if test.status == http.StatusForbidden {
					require.Zero(t, calls, "disabled image models must be blocked before routing or billing")
				} else {
					require.Equal(t, 1, calls)
				}
			})
		}
	}
}

func TestImageModelPolicyRejectsAmbiguousNativeResponseTools(t *testing.T) {
	for _, body := range []string{
		`{"model":"text","input":"edit","previous_response_id":"existing","tools":[],"tools":[{"type":"image_generation","model":"off"}]}`,
		`{"model":"text","input":"edit","previous_response_id":"existing","tools":[],"Tools":[{"type":"image_generation","model":"off"}]}`,
		`{"model":"text","input":"edit","previous_response_id":"existing","tools":[{"type":"web_search","type":"image_generation","model":"off"}]}`,
		`{"model":"text","input":"edit","previous_response_id":"existing","tools":[{"type":"image_generation","model":"on","model":"off"}]}`,
		`{"model":"text","input":"edit","previous_response_id":"existing","tools":[{"type":"image_generation","model":"on","Model":"off"}]}`,
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		_, err := gatewayInvocationImageModels(c)
		require.ErrorIs(t, err, errGatewayRoutingBody)
	}
}
