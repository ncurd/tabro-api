package service

import (
	"strconv"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBillingTextNullableOptionalParametersUseDefaultBounds(t *testing.T) {
	for _, endpoint := range []struct{ path, field string }{
		{"/v1/chat/completions", "max_completion_tokens"},
		{"/v1/responses", "max_output_tokens"},
		{"/v1/messages", "max_tokens"},
	} {
		t.Run(endpoint.path, func(t *testing.T) {
			body := []byte(`{"model":"qwen4-max","messages":[{"role":"user","content":"hello"}],"max_tokens":null,"max_completion_tokens": null ,"max_output_tokens":null,"n":null,"background":null,"enable_thinking":null}`)
			bounded, reserved, err := boundTextRequest(endpoint.path, body, nil)
			require.NoError(t, err)
			require.EqualValues(t, 262144, reserved, "null thinking retains the model's default mode")
			require.EqualValues(t, 262144, gjson.GetBytes(bounded, endpoint.field).Int())
			for _, field := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
				if field != endpoint.field {
					require.False(t, gjson.GetBytes(bounded, field).Exists())
				}
			}
			quote, err := textBillingQuoteRequest(endpoint.path, "nullable", bounded)
			require.NoError(t, err)
			require.Equal(t, bc.Decimal("262144"), quote.MaximumUsage["output_tokens"])
			ordinary, err := textBillingQuoteRequest(endpoint.path, "ordinary", []byte(`{"model":"qwen4-max","messages":[{"role":"user","content":"hello"}]}`))
			require.NoError(t, err)
			require.Equal(t, ordinary.MaximumUsage, quote.MaximumUsage, "null optional values retain the same billing limits as omission")
		})
	}
}

func TestBillingTextNullableBoundsPreserveExplicitLimitsAndThinkingMode(t *testing.T) {
	for _, test := range []struct {
		name, request string
		output        int64
	}{
		{"explicit_completion", `{"model":"qwen4-max","max_tokens":null,"max_completion_tokens":64,"max_output_tokens":null,"n":null,"background":null}`, 64},
		{"explicit_legacy", `{"model":"qwen4-max","max_tokens":64,"max_completion_tokens":null,"max_output_tokens":null}`, 64},
		{"explicit_output", `{"model":"qwen4-max","max_tokens":null,"max_completion_tokens":null,"max_output_tokens":64}`, 64},
		{"null_thinking", `{"model":"qwen4-max","enable_thinking":null,"max_completion_tokens":200000}`, 200000},
		{"thinking_disabled", `{"model":"qwen4-max","enable_thinking":false,"max_tokens":null}`, 131072},
		{"thinking_enabled", `{"model":"qwen4-max","enable_thinking":true,"max_tokens":null}`, 262144},
	} {
		t.Run(test.name, func(t *testing.T) {
			bounded, reserved, err := boundTextRequest("/v1/chat/completions", []byte(test.request), nil)
			require.NoError(t, err)
			require.Equal(t, test.output, reserved)
			require.Equal(t, test.output, gjson.GetBytes(bounded, "max_completion_tokens").Int())
			quote, err := textBillingQuoteRequest("/v1/chat/completions", "explicit", bounded)
			require.NoError(t, err)
			require.Equal(t, bc.Decimal(strconv.FormatInt(test.output, 10)), quote.MaximumUsage["output_tokens"])
		})
	}
}

func TestBillingTextNullableParametersDoNotRelaxInvalidRequests(t *testing.T) {
	for _, body := range []string{
		`{"model":"qwen4-max","max_tokens":0,"max_completion_tokens":null}`,
		`{"model":"qwen4-max","max_tokens":null,"max_completion_tokens":-1}`,
		`{"model":"qwen4-max","max_tokens":null,"max_output_tokens":262145}`,
		`{"model":"qwen4-max","max_tokens":null,"max_output_tokens":"64"}`,
		`{"model":"qwen4-max","max_tokens":64,"max_completion_tokens":null,"max_output_tokens":65}`,
		`{"model":"qwen4-max","enable_thinking":false,"max_tokens":null,"max_completion_tokens":131073}`,
		`{"model":"qwen4-max-invented","max_tokens":null}`,
		`{"model":"MiniMax/MiniMax-M3","max_tokens":null}`,
	} {
		_, _, err := boundTextRequest("/v1/chat/completions", []byte(body), nil)
		require.Error(t, err, body)
		_, err = textBillingQuoteRequest("/v1/chat/completions", "invalid", []byte(body))
		require.Error(t, err, body)
	}
	for _, body := range []string{
		`{"model":"qwen4-max","max_tokens":null,"n":0}`,
		`{"model":"qwen4-max","max_tokens":null,"n":2}`,
		`{"model":"qwen4-max","max_tokens":null,"n":"1"}`,
		`{"model":"qwen4-max","max_tokens":null,"background":true}`,
		`{"model":"qwen4-max","max_tokens":null,"background":"false"}`,
	} {
		_, err := textBillingQuoteRequest("/v1/chat/completions", "invalid", []byte(body))
		require.Error(t, err, body)
	}
}
