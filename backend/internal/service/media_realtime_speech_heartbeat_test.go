package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	ws "github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestRealtimeASRSilenceHeartbeatFollowsResolvedModelAndCallerPreference(t *testing.T) {
	for _, tc := range []struct {
		name, upstream string
		provided       *bool
		wantPresent    bool
		wantValue      bool
	}{
		{"Paraformer public alias defaults to keeping silent PCM alive", "paraformer-realtime-v2", nil, true, true},
		{"caller can keep Paraformer silence timeout", "paraformer-realtime-v2", boolPointer(false), true, false},
		{"caller heartbeat remains enabled", "paraformer-realtime-v2", boolPointer(true), true, true},
		{"Fun-ASR receives no Paraformer-specific parameter", "fun-asr-realtime", nil, false, false},
		{"Qwen Streaming receives no Paraformer-specific parameter", "qwen-audio-3.0-asr-flash-streaming", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			task := uuid.NewString()
			svc, _, account, meta := realtimeSpeechFixture(t, func(w http.ResponseWriter, r *http.Request) {
				conn, err := ws.Accept(w, r, nil)
				require.NoError(t, err)
				defer func() { _ = conn.CloseNow() }()
				_, start, err := conn.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, tc.upstream, gjson.GetBytes(start, "payload.model").String())
				heartbeat := gjson.GetBytes(start, "payload.parameters.heartbeat")
				require.Equal(t, tc.wantPresent, heartbeat.Exists())
				require.Equal(t, tc.wantValue, heartbeat.Bool())
				// No audio is generated in this wire-contract test.
				speechJSON(t, conn, ctx, map[string]any{"header": map[string]any{
					"event": "task-failed", "task_id": task, "error_code": "CLIENT_ERROR",
				}, "payload": map[string]any{}})
			})
			account.Credentials["model_mapping"] = map[string]any{"public-listen": tc.upstream}
			start := speechStart("public-listen", task)
			if tc.provided != nil {
				var err error
				start, err = sjson.SetBytes(start, "payload.parameters.heartbeat", *tc.provided)
				require.NoError(t, err)
			}
			err := svc.StreamAudioTranscription(ctx, meta, account, "public-listen", start,
				&speechTestClient{incoming: make(chan speechTestFrame)})
			require.Equal(t, "CLIENT_ERROR", DescribeRealtimeSpeechFailure(err).ProviderCode)
		})
	}
}

func boolPointer(value bool) *bool { return &value }
