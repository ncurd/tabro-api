package service

import (
	"errors"
	"strings"

	"github.com/tidwall/gjson"
)

// GatewayResponseImageModels returns the effective image models invoked by a
// HTTP Responses body, preserving the supplier dispatcher's image-bridge
// precedence before falling back to native image-tool selection.
func GatewayResponseImageModels(body []byte) ([]string, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, errors.New("Responses body must be a JSON object")
	}
	imagesBody, bridged, err := BuildOpenAICodexImageGenerationRequest(body)
	if err != nil {
		return nil, err
	}
	if bridged {
		return []string{strings.TrimSpace(gjson.GetBytes(imagesBody, "model").String())}, nil
	}
	return GatewayNativeResponseImageModels(body)
}

// GatewayNativeResponseImageModels checks every image tool on a native
// Responses invocation. WebSocket forwarding does not use the HTTP image
// bridge and must use this function even for text-only generation requests.
func GatewayNativeResponseImageModels(body []byte) ([]string, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, errors.New("Responses body must be a JSON object")
	}
	// Reject ambiguous tool selections so admission and a native supplier
	// decoder cannot select different JSON values.
	var tools gjson.Result
	toolFields := 0
	gjson.ParseBytes(body).ForEach(func(key, value gjson.Result) bool {
		if strings.EqualFold(key.String(), "tools") {
			toolFields++
			tools = value
		}
		return true
	})
	if toolFields > 1 || tools.Exists() && tools.Type != gjson.Null && !tools.IsArray() {
		return nil, errors.New("ambiguous or invalid Responses tools")
	}
	var models []string
	for _, tool := range tools.Array() {
		kind, err := gatewayResponseImageToolField(tool, "type")
		if err != nil {
			return nil, err
		}
		if kind != "image_generation" {
			continue
		}
		model, err := gatewayResponseImageToolField(tool, "model")
		if err != nil {
			return nil, err
		}
		if model == "" {
			model = "gpt-image-2"
		}
		models = append(models, model)
	}
	return models, nil
}

func gatewayResponseImageToolField(tool gjson.Result, name string) (string, error) {
	count, result, valid := 0, "", true
	tool.ForEach(func(key, value gjson.Result) bool {
		if strings.EqualFold(key.String(), name) {
			count++
			if value.Type != gjson.String && value.Type != gjson.Null {
				valid = false
			}
			result = strings.TrimSpace(value.String())
		}
		return true
	})
	if count > 1 || !valid {
		return "", errors.New("ambiguous or invalid Responses image tool " + name)
	}
	return result, nil
}
