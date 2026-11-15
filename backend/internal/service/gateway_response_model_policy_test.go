//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayResponseImageModelsSharesBridgeAndNativeSelection(t *testing.T) {
	for _, test := range []struct {
		body string
		want []string
	}{
		{`{"model":"text","input":"hello"}`, nil},
		{`{"model":"text","input":"draw","tools":[{"type":"image_generation"}]}`, []string{"gpt-image-2"}},
		{`{"model":"text","input":"draw","tools":[{"type":"image_generation","model":"custom-image"}]}`, []string{"custom-image"}},
		{`{"model":"gpt-image-custom","input":"draw","tools":[{"type":"image_generation","model":"unused-image"}]}`, []string{"gpt-image-custom"}},
		{`{"model":"text","input":"edit","previous_response_id":"existing","tools":[{"type":"image_generation","model":"custom-image"}]}`, []string{"custom-image"}},
		{`{"model":"text","input":"edit","previous_response_id":"existing","tools":[{"type":"image_generation"}]}`, []string{"gpt-image-2"}},
		{`{"model":"text","input":"edit","previous_response_id":"existing","Tools":[{"Type":"image_generation","Model":"custom-image"}]}`, []string{"custom-image"}},
	} {
		models, err := GatewayResponseImageModels([]byte(test.body))
		require.NoError(t, err)
		require.Equal(t, test.want, models)
	}
	for _, body := range []string{
		`[]`, `{"model":`,
		`{"model":"text","input":"edit","previous_response_id":"existing","tools":[],"Tools":[{"type":"image_generation","model":"off"}]}`,
		`{"model":"text","input":"edit","previous_response_id":"existing","tools":[{"type":"image_generation","model":"on","Model":"off"}]}`,
	} {
		_, err := GatewayResponseImageModels([]byte(body))
		require.Error(t, err)
	}
}

func TestGatewayNativeResponseImageModelsChecksEveryToolWithoutBridgePrecedence(t *testing.T) {
	body := []byte(`{"model":"gpt-image-top","input":"draw","tools":[{"type":"image_generation","model":"first-image"},{"type":"image_generation"}]}`)
	models, err := GatewayResponseImageModels(body)
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-image-top"}, models)
	models, err = GatewayNativeResponseImageModels(body)
	require.NoError(t, err)
	require.Equal(t, []string{"first-image", "gpt-image-2"}, models)
	for _, body := range []string{
		`{"model":"text","input":"draw","tools":[],"Tools":[{"type":"image_generation","model":"off"}]}`,
		`{"model":"text","input":"draw","tools":[{"type":"image_generation","model":"on","Model":"off"}]}`,
	} {
		_, err = GatewayNativeResponseImageModels([]byte(body))
		require.Error(t, err)
	}
}
