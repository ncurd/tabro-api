package middleware

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Check before routing, reservations and supplier dispatch. Do not apply this
// gate to fetching completed jobs or deleting resources: those remain usable.
func enforceGatewayModelPolicy(c *gin.Context, keys *service.APIKeyService) error {
	if keys == nil || keys.ModelPolicies == nil {
		return nil
	}
	c.Request = c.Request.WithContext(service.WithModelAvailabilityPolicy(c.Request.Context(), keys.ModelPolicies))
	model, err := gatewayInvocationModel(c)
	if err != nil {
		return err
	}
	if err = service.RequireModelEnabled(c.Request.Context(), keys.ModelPolicies, model); err != nil {
		return err
	}
	imageModels, err := gatewayInvocationImageModels(c)
	if err != nil {
		return err
	}
	for _, imageModel := range imageModels {
		if err = service.RequireModelEnabled(c.Request.Context(), keys.ModelPolicies, imageModel); err != nil {
			return err
		}
	}
	return nil
}

// Image edits can supply an implicit default model, and Responses may invoke a
// second image model through a tool. Check those effective models before either
// routing or a billing reservation, using the same parsers as supplier dispatch.
func gatewayInvocationImageModels(c *gin.Context) ([]string, error) {
	if c.Request.Method != http.MethodPost || c.Request.Body == nil {
		return nil, nil
	}
	path := c.Request.URL.Path
	edit := strings.HasSuffix(path, "/images/edits")
	responses := strings.Contains(path+"/", "/responses/")
	if !edit && !responses {
		return nil, nil
	}
	body, err := readGatewayRequestBody(c)
	if err != nil {
		return nil, errors.Join(errGatewayRoutingBody, err)
	}
	if edit {
		parsed, err := service.ParseOpenAIImageEditRequest(body, c.GetHeader("Content-Type"))
		if err != nil {
			return nil, errors.Join(errGatewayRoutingBody, err)
		}
		return []string{parsed.Model}, nil
	}
	models, err := service.GatewayResponseImageModels(body)
	if err != nil {
		return nil, errors.Join(errGatewayRoutingBody, err)
	}
	return models, nil
}

func gatewayInvocationModel(c *gin.Context) (string, error) {
	path := c.Request.URL.Path
	if strings.Contains(path, "/v1beta/models/") && (c.Request.Method == http.MethodPost || c.Request.Method == http.MethodGet) {
		model := strings.SplitN(path, "/models/", 2)[1]
		return strings.SplitN(model, ":", 2)[0], nil
	}
	if c.Request.Method == http.MethodGet && strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
		// ASR selects a model in the query. Responses WS selects it in each
		// response.create frame and is checked separately before each turn.
		return strings.TrimSpace(c.Query("model")), nil
	}
	if c.Request.Method != http.MethodPost || c.Request.Body == nil {
		return "", nil
	}
	body, err := readGatewayRequestBody(c)
	if err != nil {
		return "", errors.Join(errGatewayRoutingBody, err)
	}
	mediaType, params, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if mediaType == "multipart/form-data" {
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		model, seen := "", false
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				return model, nil
			}
			if err != nil {
				return "", errGatewayRoutingBody
			}
			if part.FormName() == "model" && part.FileName() == "" {
				if seen {
					_ = part.Close()
					return "", errGatewayRoutingBody
				}
				seen = true
				value, err := io.ReadAll(io.LimitReader(part, 1025))
				_ = part.Close()
				if err != nil || len(value) > 1024 {
					return "", errGatewayRoutingBody
				}
				model = strings.TrimSpace(string(value))
				continue
			}
			_ = part.Close()
		}
	}
	if !gjson.ValidBytes(body) {
		return "", errGatewayRoutingBody
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return "", errGatewayRoutingBody
	}
	count, model, valid := 0, "", true
	root.ForEach(func(key, value gjson.Result) bool {
		// Go's typed JSON decoders also accept Model/MODEL. Check the same
		// spelling variants and reject duplicates before any decoder can choose
		// a different model from the one used for admission.
		if strings.EqualFold(key.String(), "model") {
			count++
			if value.Type != gjson.String {
				valid = false
			}
			model = strings.TrimSpace(value.String())
		}
		return true
	})
	if count > 1 || !valid {
		return "", errGatewayRoutingBody
	}
	return model, nil
}

func modelPolicyError(err error) (int, string, string) {
	if errors.Is(err, service.ErrModelDisabled) {
		return http.StatusForbidden, "MODEL_DISABLED", "This model is disabled by the administrator"
	}
	if errors.Is(err, errGatewayRoutingBody) {
		return gatewayGroupRoutingError(err)
	}
	return http.StatusServiceUnavailable, "MODEL_POLICY_UNAVAILABLE", "Model availability settings are temporarily unavailable"
}
