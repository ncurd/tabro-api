package aliyun

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDocumentedTextCatalogHasUniqueExactIDsAndSources(t *testing.T) {
	seen := make(map[string]bool)
	for _, model := range textModels {
		id := strings.ToLower(model.ID)
		if id == "" || seen[id] {
			t.Fatalf("empty or duplicate model ID %q", model.ID)
		}
		seen[id] = true
		source, err := url.Parse(model.Source)
		if err != nil || source.Scheme != "https" || !(source.Hostname() == "help.aliyun.com" || strings.HasSuffix(source.Hostname(), ".alibabacloud.com") || source.Hostname() == "www.alibabacloud.com") {
			t.Errorf("%s has no official Alibaba Cloud source: %s", model.ID, model.Source)
		}
		if model.MaxInputTokens < 0 || model.MaxOutputTokens < 0 || model.ContextWindow < 0 {
			t.Errorf("%s has a negative capability", model.ID)
		}
	}
	for _, id := range []string{"qwen4-max", "qwen3.8-max", "qwen3-coder-next", "glm-5.3", "deepseek-v4.1-flash"} {
		if entry, ok := LookupTextModel(id); !ok || entry.MaxInputTokens == 0 || entry.MaxOutputTokens == 0 {
			t.Errorf("missing documented capability for %s", id)
		}
	}
	for _, id := range []string{"qwen4-max-invented", "glm-5.3-custom", "deepseek-v4.1-flash-20990101", "openrouter/qwen4-max"} {
		if _, ok := LookupTextModel(id); ok {
			t.Errorf("invented an exact capability for %s", id)
		}
	}
}

func TestOfficialCompatibleEndpointHostnames(t *testing.T) {
	for _, address := range []string{"https://dashscope.aliyuncs.com/compatible-mode/v1", "https://coding.dashscope.aliyuncs.com/v1", "https://coding-intl.dashscope.aliyuncs.com/v1", "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", "https://dashscope-us.aliyuncs.com/compatible-mode/v1", "https://DASHSCOPE.ALIYUNCS.CN/compatible-mode/v1", "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"} {
		if !IsCompatibleBaseURL(address) {
			t.Errorf("unrecognized official endpoint: %s", address)
		}
	}
	for _, address := range []string{"https://dashscope.aliyuncs.com.evil.example/v1", "https://example.com/dashscope.aliyuncs.com/v1", "https://notdashscope.aliyuncs.com/v1", "https://api.openai.com/v1", "https://dashscope.aliyuncs.com@evil.example/v1", "https://workspace.cn-beijing.maas.aliyuncs.com.evil.example/v1", "dashscope.aliyuncs.com/v1", ""} {
		if IsCompatibleBaseURL(address) {
			t.Errorf("misclassified endpoint: %s", address)
		}
	}
}

func TestFrontendAndBackendAliyunSuggestionsStayInSync(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "frontend", "src", "data", "aliyunTextModels.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skip("frontend sources are absent from this backend-only checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	var frontendIDs []string
	if err := json.Unmarshal(raw, &frontendIDs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(DefaultTextModelIDs(), frontendIDs) {
		t.Fatal("frontend suggestions differ from the documented backend catalogue")
	}
}
