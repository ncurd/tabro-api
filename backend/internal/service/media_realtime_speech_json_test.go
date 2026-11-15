package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRealtimeASRRejectsAmbiguousStartIdentityBeforeBillingAndSupplier(t *testing.T) {
	task := uuid.NewString()
	header := fmt.Sprintf(`{"action":"run-task","task_id":%q,"streaming":"duplex"}`, task)
	parameters := `{"format":"pcm","sample_rate":16000}`
	payload := `{"task_group":"audio","task":"asr","function":"recognition","model":"public-listen","parameters":` + parameters + `,"input":{}}`
	wrap := func(h, p string) string { return `{"header":` + h + `,"payload":` + p + `}` }
	withModel := func(field string) string {
		return strings.Replace(payload, `"model":"public-listen"`, `"model":"public-listen",`+field, 1)
	}
	for _, tc := range []struct{ name, body string }{
		{"duplicate model", wrap(header, withModel(`"model":"unpriced-model"`))},
		{"duplicate identical model", wrap(header, withModel(`"model":"public-listen"`))},
		{"escaped duplicate model", wrap(header, withModel(`"m\u006fdel":"unpriced-model"`))},
		{"case variant model", wrap(header, withModel(`"Model":"unpriced-model"`))},
		{"missing canonical model", wrap(header, strings.Replace(payload, `"model":`, `"Model":`, 1))},
		{"duplicate payload", strings.TrimSuffix(wrap(header, payload), "}") + `,"payload":` + strings.Replace(payload, "public-listen", "unpriced-model", 1) + `}`},
		{"case variant payload", strings.TrimSuffix(wrap(header, payload), "}") + `,"Payload":` + payload + `}`},
		{"duplicate header", strings.TrimSuffix(wrap(header, payload), "}") + `,"header":` + header + `}`},
		{"case variant task ID", wrap(strings.TrimSuffix(header, "}")+`,"Task_ID":`+fmt.Sprintf("%q", uuid.NewString())+`}`, payload)},
		{"duplicate parameters", wrap(header, strings.TrimSuffix(payload, "}")+`,"parameters":{"format":"mp3","sample_rate":16000}}`)},
		{"case variant nested audio format", wrap(header, strings.Replace(payload, `"format":"pcm"`, `"format":"pcm","Format":"mp3"`, 1))},
		{"duplicate key in array object", wrap(header, strings.TrimSuffix(payload, "}")+`,"extra":[{"option":true,"option":false}]}`)},
		{"unicode equal fold cycle", wrap(header, strings.TrimSuffix(payload, "}")+`,"extra":{"K":1,"\u212a":2}}`)},
		{"excessive nesting", wrap(header, strings.TrimSuffix(payload, "}")+`,"extra":`+strings.Repeat("[", 65)+`0`+strings.Repeat("]", 65)+`}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, account, meta := realtimeSpeechFixture(t, func(http.ResponseWriter, *http.Request) {
				t.Fatal("ambiguous identities must not reach the supplier")
			})
			quoted := false
			ctx := WithGatewayBillingSession(context.Background(), &GatewayBillingSession{
				PrepareMedia: func(context.Context, string, string, []byte) (*bc.Execution, error) {
					quoted = true
					return nil, bc.ErrState
				},
				Finish: func(context.Context, *bc.Execution) error { return nil },
			})
			err := svc.StreamAudioTranscription(ctx, meta, account, "public-listen", []byte(tc.body), &speechTestClient{})
			var invalid *InvalidMediaRequestError
			require.ErrorAs(t, err, &invalid)
			require.Equal(t, "speech_invalid_request", DescribeRealtimeSpeechFailure(err).Code)
			require.Empty(t, repo.created, "invalid identity must be rejected before creating billable media work")
			require.False(t, quoted, "ambiguous model identities must not reach billing authorization")
		})
	}
}

func TestRealtimeASRAcceptsUniqueOptionalParametersAndIndependentArrayObjects(t *testing.T) {
	task := uuid.NewString()
	start := speechStart("public-listen", task)
	// The same option name in separate objects is unambiguous and must remain
	// usable. Restriction applies to names within one JSON object only.
	start = []byte(strings.TrimSuffix(string(start), "}") + `,"metadata":[{"option":1},{"option":2}]}`)
	got, err := validateNativeASRStart(start, "public-listen")
	require.NoError(t, err)
	require.Equal(t, task, got)
}
