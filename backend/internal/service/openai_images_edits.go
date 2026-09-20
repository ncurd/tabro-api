package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAIImageEditRequest retains uploaded bytes for retries while exposing only
// reference-free JSON metadata to scheduling, billing, and request logging.
type OpenAIImageEditRequest struct {
	Model        string
	Stream       bool
	MetadataBody []byte

	body        []byte
	contentType string
	wireModel   string
	parts       []openAIImageEditPart
}

type openAIImageEditPart struct {
	header   textproto.MIMEHeader
	name     string
	filename string
	data     []byte
}

var openAIImageEditMetadataFields = []string{
	"model", "prompt", "n", "size", "quality", "background", "output_format",
	"output_compression", "input_fidelity", "moderation", "response_format", "user",
	"stream", "partial_images", "style",
}

// ParseOpenAIImageEditRequest accepts the native JSON and multipart encodings.
// It deliberately leaves model-specific image limits to the selected upstream.
func ParseOpenAIImageEditRequest(body []byte, contentType string) (*OpenAIImageEditRequest, error) {
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/json"
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, errors.New("invalid Content-Type")
	}
	r := &OpenAIImageEditRequest{body: body, contentType: contentType}
	metadata := make(map[string]any)
	switch mediaType {
	case "application/json":
		if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
			return nil, errors.New("request body must be a JSON object")
		}
		seen := make(map[string]bool)
		duplicate := false
		gjson.ParseBytes(body).ForEach(func(key, _ gjson.Result) bool {
			if seen[key.String()] {
				duplicate = true
			}
			seen[key.String()] = true
			return !duplicate
		})
		if duplicate {
			return nil, errors.New("duplicate JSON request field")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			return nil, errors.New("invalid JSON request body")
		}
		for _, key := range openAIImageEditMetadataFields {
			if value, ok := fields[key]; ok {
				var decoded any
				if err := json.Unmarshal(value, &decoded); err != nil {
					return nil, fmt.Errorf("invalid %s", key)
				}
				metadata[key] = decoded
			}
		}
		images := gjson.GetBytes(body, "images")
		if !images.IsArray() || len(images.Array()) == 0 {
			return nil, errors.New("images must contain at least one image_url or file_id reference")
		}
		for _, ref := range images.Array() {
			if err := validateOpenAIImageEditReference(ref); err != nil {
				return nil, fmt.Errorf("invalid images reference: %w", err)
			}
		}
		if mask := gjson.GetBytes(body, "mask"); mask.Exists() && mask.Type != gjson.Null {
			if err := validateOpenAIImageEditReference(mask); err != nil {
				return nil, fmt.Errorf("invalid mask: %w", err)
			}
		}
	case "multipart/form-data":
		if params["boundary"] == "" {
			return nil, errors.New("multipart boundary is required")
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		imageCount, maskCount := 0, 0
		for {
			part, err := reader.NextRawPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, errors.New("invalid multipart request body")
			}
			data, err := io.ReadAll(part)
			if err != nil {
				return nil, errors.New("invalid multipart field")
			}
			name := part.FormName()
			if name == "" {
				return nil, errors.New("multipart field name is required")
			}
			r.parts = append(r.parts, openAIImageEditPart{header: part.Header, name: name, filename: part.FileName(), data: data})
			if name == "image" || name == "image[]" || name == "mask" {
				if part.FileName() == "" || len(data) == 0 {
					return nil, fmt.Errorf("%s must be a non-empty uploaded file", name)
				}
				if name == "mask" {
					maskCount++
				} else {
					imageCount++
				}
				continue
			}
			for _, key := range openAIImageEditMetadataFields {
				if name != key {
					continue
				}
				if _, exists := metadata[key]; exists || part.FileName() != "" {
					return nil, fmt.Errorf("%s must be a single text field", key)
				}
				value := string(data)
				switch key {
				case "stream":
					if value != "true" && value != "false" {
						return nil, errors.New("stream must be a boolean")
					}
					metadata[key] = value == "true"
				case "n", "partial_images", "output_compression":
					n, err := strconv.Atoi(value)
					if err != nil {
						return nil, fmt.Errorf("%s must be an integer", key)
					}
					metadata[key] = n
				default:
					metadata[key] = value
				}
			}
		}
		if imageCount == 0 {
			return nil, errors.New("image or image[] must contain at least one uploaded image")
		}
		if maskCount > 1 {
			return nil, errors.New("mask must contain a single uploaded file")
		}
	default:
		return nil, errors.New("Content-Type must be application/json or multipart/form-data")
	}
	if prompt, ok := metadata["prompt"].(string); !ok || strings.TrimSpace(prompt) == "" {
		return nil, errors.New("prompt must be a non-empty string")
	}
	if model, exists := metadata["model"]; exists {
		var ok bool
		r.wireModel, ok = model.(string)
		if !ok || strings.TrimSpace(r.wireModel) == "" {
			return nil, errors.New("model must be a non-empty string")
		}
	}
	if stream, exists := metadata["stream"]; exists {
		var ok bool
		r.Stream, ok = stream.(bool)
		if !ok {
			return nil, errors.New("stream must be a boolean")
		}
	}
	r.MetadataBody, err = json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(r.wireModel)
	if model == "" {
		model = "gpt-image-2"
	}
	if err := r.SetModel(model); err != nil {
		return nil, err
	}
	return r, nil
}

func validateOpenAIImageEditReference(ref gjson.Result) error {
	if !ref.IsObject() {
		return errors.New("expected an object with image_url or file_id")
	}
	url, id := ref.Get("image_url"), ref.Get("file_id")
	validURL := url.Type == gjson.String && strings.TrimSpace(url.String()) != ""
	validID := id.Type == gjson.String && strings.TrimSpace(id.String()) != ""
	if validURL == validID || (url.Exists() && !validURL) || (id.Exists() && !validID) {
		return errors.New("provide exactly one non-empty image_url or file_id")
	}
	if validURL {
		if err := ValidateMediaInputURL(url.String(), "image", true, false); err != nil {
			return err
		}
		if strings.HasPrefix(url.String(), "data:") {
			mediaType, _, _ := validateMediaDataURL(url.String())
			switch mediaType {
			case "image/png", "image/jpeg", "image/webp":
			default:
				return errors.New("inline image must use image/png, image/jpeg, or image/webp")
			}
		}
	}
	return nil
}

// SetModel applies group mappings without rewriting uploaded file data.
func (r *OpenAIImageEditRequest) SetModel(model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return errors.New("model must be a non-empty string")
	}
	metadata, err := sjson.SetBytes(r.MetadataBody, "model", model)
	if err != nil {
		return err
	}
	r.Model, r.MetadataBody = model, metadata
	return nil
}

func (r *OpenAIImageEditRequest) upstreamBody() ([]byte, string, error) {
	if r.Model == r.wireModel {
		return r.body, r.contentType, nil
	}
	if r.parts == nil {
		body, err := sjson.SetBytes(r.body, "model", r.Model)
		return body, r.contentType, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	hasModel := false
	for _, part := range r.parts {
		dest, err := writer.CreatePart(part.header)
		if err != nil {
			return nil, "", err
		}
		data := part.data
		if part.name == "model" {
			data = []byte(r.Model)
			hasModel = true
		}
		if _, err := dest.Write(data); err != nil {
			return nil, "", err
		}
	}
	if !hasModel {
		if err := writer.WriteField("model", r.Model); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func (r *OpenAIImageEditRequest) oauthBody() ([]byte, error) {
	body := r.MetadataBody
	if r.parts == nil {
		body, _ = sjson.SetRawBytes(body, "images", []byte(gjson.GetBytes(r.body, "images").Raw))
		if mask := gjson.GetBytes(r.body, "mask"); mask.Exists() && mask.Type != gjson.Null {
			body, _ = sjson.SetRawBytes(body, "mask", []byte(mask.Raw))
		}
		return body, nil
	}
	for _, part := range r.parts {
		if part.name != "image" && part.name != "image[]" && part.name != "mask" {
			continue
		}
		mediaType, _, _ := mime.ParseMediaType(part.header.Get("Content-Type"))
		if !strings.HasPrefix(mediaType, "image/") {
			mediaType = http.DetectContentType(part.data)
		}
		if !strings.HasPrefix(mediaType, "image/") {
			mediaType = mime.TypeByExtension(strings.ToLower(filepath.Ext(part.filename)))
		}
		if !strings.HasPrefix(mediaType, "image/") {
			return nil, fmt.Errorf("%s has an unsupported image type", part.name)
		}
		ref := map[string]string{"image_url": "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(part.data)}
		path := "images.-1"
		if part.name == "mask" {
			path = "mask"
		}
		var err error
		body, err = sjson.SetBytes(body, path, ref)
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

func addOpenAIImageEditInputs(responsesBody, imagesBody []byte) ([]byte, error) {
	for _, ref := range gjson.GetBytes(imagesBody, "images").Array() {
		if err := validateOpenAIImageEditReference(ref); err != nil {
			return nil, err
		}
		input := map[string]any{"type": "input_image", "detail": "auto"}
		for _, key := range []string{"image_url", "file_id"} {
			if value := ref.Get(key); value.Exists() {
				input[key] = value.String()
			}
		}
		responsesBody, _ = sjson.SetBytes(responsesBody, "input.0.content.-1", input)
	}
	responsesBody, _ = sjson.SetBytes(responsesBody, "tools.0.action", "edit")
	if mask := gjson.GetBytes(imagesBody, "mask"); mask.Exists() && mask.Type != gjson.Null {
		responsesBody, _ = sjson.SetRawBytes(responsesBody, "tools.0.input_image_mask", []byte(mask.Raw))
	}
	return responsesBody, nil
}

func buildOpenAIImagesEditsURL(base string) string {
	normalized := strings.TrimRight(strings.TrimSpace(base), "/")
	for _, suffix := range []string{"/images/edits", "/images/generations"} {
		if strings.HasSuffix(normalized, suffix) {
			return strings.TrimSuffix(normalized, suffix) + "/images/edits"
		}
	}
	if strings.HasSuffix(normalized, "/v1") {
		return normalized + "/images/edits"
	}
	return normalized + "/v1/images/edits"
}

// ForwardImagesEdits forwards uploads intact for API-key accounts and bridges
// OAuth accounts to Responses with every reference image and mask preserved.
func (s *OpenAIGatewayService) ForwardImagesEdits(ctx context.Context, c *gin.Context, account *Account, request *OpenAIImageEditRequest) (*OpenAIForwardResult, error) {
	if account == nil || request == nil {
		return nil, errors.New("account and image edit request are required")
	}
	mapped := *request
	if err := mapped.SetModel(account.GetMappedModel(request.Model)); err != nil {
		return nil, err
	}
	var result *OpenAIForwardResult
	var err error
	setOpsUpstreamRequestBody(c, mapped.MetadataBody)
	switch account.Type {
	case AccountTypeOAuth:
		body, bodyErr := mapped.oauthBody()
		if bodyErr != nil {
			return nil, bodyErr
		}
		_, result, err = s.forwardImagesOAuth(ctx, c, account, body, true, true)
	case AccountTypeAPIKey:
		token, _, tokenErr := s.GetAccessToken(ctx, account)
		if tokenErr != nil {
			return nil, tokenErr
		}
		baseURL, validateErr := s.validateUpstreamBaseURL(account.GetOpenAIBaseURL())
		if validateErr != nil {
			return nil, validateErr
		}
		body, contentType, bodyErr := mapped.upstreamBody()
		if bodyErr != nil {
			return nil, bodyErr
		}
		upstreamReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, buildOpenAIImagesEditsURL(baseURL), bytes.NewReader(body))
		if reqErr != nil {
			return nil, reqErr
		}
		if c != nil && c.Request != nil {
			for _, key := range []string{"Accept", "Accept-Language", "User-Agent"} {
				if value := c.Request.Header.Get(key); value != "" {
					upstreamReq.Header.Set(key, value)
				}
			}
		}
		upstreamReq.Header.Set("Authorization", "Bearer "+token)
		upstreamReq.Header.Set("Content-Type", contentType)
		if mapped.Stream {
			upstreamReq.Header.Set("Accept", "text/event-stream")
		}
		_, result, err = s.forwardImagesAPIKey(ctx, c, account, upstreamReq, mapped.MetadataBody, true)
	default:
		return nil, errors.New("image edits require an OpenAI API key or OAuth account")
	}
	if result != nil {
		result.Model, result.UpstreamModel = request.Model, mapped.Model
	}
	return result, err
}
