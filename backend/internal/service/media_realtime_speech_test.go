package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	ws "github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type realtimeTestHTTPUpstream struct{}

func (realtimeTestHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}
func (u realtimeTestHTTPUpstream) DoWithTLS(req *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, p, id, n)
}

type speechTestFrame struct {
	kind ws.MessageType
	data []byte
}
type speechTestClient struct {
	incoming chan speechTestFrame
	mu       sync.Mutex
	events   [][]byte
	onWrite  func([]byte)
}

func (c *speechTestClient) Read(ctx context.Context) (ws.MessageType, []byte, error) {
	select {
	case frame := <-c.incoming:
		return frame.kind, frame.data, nil
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}
func (c *speechTestClient) Write(_ context.Context, kind ws.MessageType, data []byte) error {
	c.mu.Lock()
	c.events = append(c.events, append([]byte(nil), data...))
	c.mu.Unlock()
	if c.onWrite != nil {
		c.onWrite(data)
	}
	return nil
}

func realtimeSpeechFixture(t *testing.T, handler http.HandlerFunc) (*MediaGenerationService, *mediaGenerationJobRepoStub, *Account, MediaRequestMeta) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	repo := newMediaGenerationJobRepoStub()
	svc := NewMediaGenerationService(nil, repo, nil, realtimeTestHTTPUpstream{}, cfg, nil)
	account := &Account{ID: 50, Platform: PlatformDashScope, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "supplier-key", "base_url": server.URL, "model_mapping": map[string]any{"public-listen": "paraformer-realtime-v2", "public-speak": "qwen3-tts-flash-realtime"}}}
	meta := MediaRequestMeta{UserID: 12, APIKeyID: 13, GroupID: int64PtrForMediaGenerationTest(14), RequestJSON: []byte(`{}`)}
	return svc, repo, account, meta
}
func speechJSON(t *testing.T, conn *ws.Conn, ctx context.Context, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, ws.MessageText, data))
}
func speechStart(model, task string) []byte {
	data, _ := json.Marshal(map[string]any{"header": map[string]any{"action": "run-task", "task_id": task, "streaming": "duplex"}, "payload": map[string]any{"task_group": "audio", "task": "asr", "function": "recognition", "model": model, "input": map[string]any{}, "parameters": map[string]any{"format": "pcm", "sample_rate": 16000}}})
	return data
}
func speechFinish(task string) []byte {
	data, _ := json.Marshal(map[string]any{"header": map[string]any{"action": "finish-task", "task_id": task, "streaming": "duplex"}, "payload": map[string]any{"input": map[string]any{}}})
	return data
}

func TestRealtimeASRRelaysPCMAndPartialBeforeFinishAndStoresOnlyUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	task := uuid.NewString()
	pcm := []byte{0x01, 0, 0x02, 0}
	partialSeen := make(chan struct{})
	supplierDone := make(chan struct{})
	svc, repo, account, meta := realtimeSpeechFixture(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(supplierDone)
		require.Equal(t, "/api-ws/v1/inference", r.URL.Path)
		require.Equal(t, "Bearer supplier-key", r.Header.Get("Authorization"))
		conn, err := ws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()
		_, start, err := conn.Read(ctx)
		require.NoError(t, err)
		require.Equal(t, "paraformer-realtime-v2", gjson.GetBytes(start, "payload.model").String())
		speechJSON(t, conn, ctx, map[string]any{"header": map[string]any{"event": "task-started", "task_id": task}, "payload": map[string]any{}})
		kind, audio, err := conn.Read(ctx)
		require.NoError(t, err)
		require.Equal(t, ws.MessageBinary, kind)
		require.Equal(t, pcm, audio)
		speechJSON(t, conn, ctx, map[string]any{"header": map[string]any{"event": "result-generated", "task_id": task}, "payload": map[string]any{"output": map[string]any{"sentence": map[string]any{"begin_time": 0, "text": "私密", "sentence_end": false}}}})
		select {
		case <-partialSeen:
		case <-ctx.Done():
			t.Error("partial was not delivered while audio was streaming")
			return
		}
		_, finish, err := conn.Read(ctx)
		require.NoError(t, err)
		require.Equal(t, "finish-task", gjson.GetBytes(finish, "header.action").String())
		speechJSON(t, conn, ctx, map[string]any{"header": map[string]any{"event": "result-generated", "task_id": task}, "payload": map[string]any{"output": map[string]any{"sentence": map[string]any{"begin_time": 0, "text": "私密文本。", "sentence_end": true}}, "usage": map[string]any{"duration": 2}}})
		speechJSON(t, conn, ctx, map[string]any{"header": map[string]any{"event": "task-finished", "task_id": task}, "payload": map[string]any{}})
	})
	client := &speechTestClient{incoming: make(chan speechTestFrame, 2)}
	client.onWrite = func(data []byte) {
		switch gjson.GetBytes(data, "header.event").String() {
		case "task-started":
			client.incoming <- speechTestFrame{ws.MessageBinary, pcm}
		case "result-generated":
			if !gjson.GetBytes(data, "payload.output.sentence.sentence_end").Bool() {
				close(partialSeen)
				client.incoming <- speechTestFrame{ws.MessageText, speechFinish(task)}
			}
		}
	}
	require.NoError(t, svc.StreamAudioTranscription(ctx, meta, account, "public-listen", speechStart("public-listen", task), client))
	<-supplierDone
	require.Len(t, repo.created, 1)
	stored, err := repo.GetByPublicID(ctx, repo.created[0].PublicID)
	require.NoError(t, err)
	require.Equal(t, MediaJobStatusSucceeded, stored.Status)
	require.Equal(t, float64(2), gjson.GetBytes(stored.UpstreamResponseJSON, "usage.seconds").Float())
	require.NotContains(t, string(stored.UpstreamResponseJSON), "私密")
	require.NotContains(t, string(stored.RequestJSON), "supplier-key")
	require.Len(t, client.events, 4)
}

func TestRealtimeASRRejectsModelMismatchAndUnsupportedProtocolBeforeDial(t *testing.T) {
	svc, _, account, meta := realtimeSpeechFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid audio request reached supplier") })
	client := &speechTestClient{incoming: make(chan speechTestFrame)}
	task := uuid.NewString()
	for _, tc := range []struct {
		model string
		start []byte
	}{{"public-listen", speechStart("other-model", task)}, {"qwen3-asr-flash-realtime", speechStart("qwen3-asr-flash-realtime", task)}, {"qwen-audio-3.1-asr-flash-streaming", speechStart("qwen-audio-3.1-asr-flash-streaming", task)}} {
		account.Credentials["model_mapping"] = map[string]any{}
		require.Error(t, svc.StreamAudioTranscription(context.Background(), meta, account, tc.model, tc.start, client))
	}
}

func TestRealtimeASRBillingDenialDoesNotStartSupplierWork(t *testing.T) {
	svc, repo, account, meta := realtimeSpeechFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("billing denial reached supplier") })
	called := false
	session := &GatewayBillingSession{Mode: "central", Finish: func(context.Context, *bc.Execution) error { return nil }, PrepareMedia: func(_ context.Context, path, id string, body []byte) (*bc.Execution, error) {
		called = true
		require.Equal(t, "/audio/transcriptions/stream", path)
		require.NotEmpty(t, id)
		require.Equal(t, "public-listen", gjson.GetBytes(body, "model").String())
		return nil, bc.ErrState
	}}
	ctx := WithGatewayBillingSession(context.Background(), session)
	err := svc.StreamAudioTranscription(ctx, meta, account, "public-listen", speechStart("public-listen", uuid.NewString()), &speechTestClient{})
	require.ErrorIs(t, err, bc.ErrState)
	require.True(t, called)
	require.Empty(t, repo.created)
}

func TestRealtimeTTSProducesWAVFromActualProviderPCMAndCorrectCharacterUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pcm := []byte{1, 0, 2, 0, 3, 0, 4, 0}
	svc, repo, account, _ := realtimeSpeechFixture(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api-ws/v1/realtime", r.URL.Path)
		require.Equal(t, "qwen3-tts-flash-realtime", r.URL.Query().Get("model"))
		conn, err := ws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()
		speechJSON(t, conn, ctx, map[string]any{"type": "session.created", "session": map[string]any{"id": "tts-test"}})
		_, update, err := conn.Read(ctx)
		require.NoError(t, err)
		require.Equal(t, "Cherry", gjson.GetBytes(update, "session.voice").String())
		speechJSON(t, conn, ctx, map[string]any{"type": "session.updated"})
		for _, event := range []string{"input_text_buffer.append", "input_text_buffer.commit", "session.finish"} {
			_, data, err := conn.Read(ctx)
			require.NoError(t, err)
			require.Equal(t, event, gjson.GetBytes(data, "type").String())
			if event == "input_text_buffer.append" {
				require.Equal(t, "你好A。", gjson.GetBytes(data, "text").String())
			}
		}
		speechJSON(t, conn, ctx, map[string]any{"type": "response.audio.delta", "delta": base64.StdEncoding.EncodeToString(pcm)})
		speechJSON(t, conn, ctx, map[string]any{"type": "response.done", "response": map[string]any{"id": "tts-response", "status": "completed", "usage": map[string]any{"characters": 25}}})
		speechJSON(t, conn, ctx, map[string]any{"type": "session.finished"})
	})
	billing, prices, ledger, _, meta, _ := mediaBillingFixture()
	prices.price.BillingMode = BillingModeAudio
	svc.mediaBilling, svc.cfg.RunMode = billing, config.RunModeStandard
	result, audio, headers, err := svc.ForwardQwenRealtimeSpeech(ctx, meta, account, AzureSpeechRequest{Model: "public-speak", Input: "你好A。", Voice: "Cherry", ResponseFormat: "wav"})
	require.NoError(t, err)
	require.Equal(t, "audio/wav", headers.Get("Content-Type"))
	require.Equal(t, "tts-test", result.RequestID)
	require.Equal(t, "RIFF", string(audio[:4]))
	require.Equal(t, uint32(24000), binary.LittleEndian.Uint32(audio[24:28]))
	require.True(t, bytes.Equal(pcm, audio[44:]))
	require.Equal(t, 6, repo.created[0].AudioCharacterCount)
	stored, err := repo.GetByPublicID(ctx, repo.created[0].PublicID)
	require.NoError(t, err)
	require.Equal(t, int64(25), gjson.GetBytes(stored.UpstreamResponseJSON, "usage.characters").Int())
	require.InDelta(t, 25*0.25*1.5, ledger.commands[stored.PublicID].BalanceCost, 1e-10, "supplier usage must override the local six-character estimate")
	require.NotContains(t, string(stored.UpstreamResponseJSON), "你好")
}

func TestRealtimeTTSUsageRequiresEveryResponseAndAccumulatesWithoutDuplicateBilling(t *testing.T) {
	done := func(id string, usage any) map[string]any {
		return map[string]any{"type": "response.done", "response": map[string]any{"id": id, "status": "completed", "usage": usage}}
	}
	for _, tc := range []struct {
		name   string
		events []map[string]any
		want   int64
		known  int64
	}{
		{"multiple responses", []map[string]any{done("one", map[string]any{"characters": 2}), done("two", map[string]any{"characters": 3}), done("two", map[string]any{"characters": 3})}, 5, 5},
		{"no response usage", nil, 0, 0},
		{"missing characters", []map[string]any{done("one", map[string]any{})}, 0, 0},
		{"second response missing usage", []map[string]any{done("one", map[string]any{"characters": 2}), done("two", map[string]any{})}, 0, 2},
		{"response never completed", []map[string]any{{"type": "response.created", "response": map[string]any{"id": "unfinished"}}, done("one", map[string]any{"characters": 2})}, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			svc, repo, account, _ := realtimeSpeechFixture(t, func(w http.ResponseWriter, r *http.Request) {
				conn, err := ws.Accept(w, r, nil)
				require.NoError(t, err)
				defer func() { _ = conn.CloseNow() }()
				_, _, err = conn.Read(ctx)
				require.NoError(t, err)
				speechJSON(t, conn, ctx, map[string]any{"type": "session.updated"})
				for i := 0; i < 3; i++ {
					_, _, err = conn.Read(ctx)
					require.NoError(t, err)
				}
				speechJSON(t, conn, ctx, map[string]any{"type": "response.audio.delta", "delta": "AQACAA=="})
				for _, event := range tc.events {
					speechJSON(t, conn, ctx, event)
				}
				_ = conn.Write(ctx, ws.MessageText, []byte(`{"type":"session.finished"}`))
			})
			billing, prices, ledger, _, meta, _ := mediaBillingFixture()
			prices.price.BillingMode = BillingModeAudio
			svc.mediaBilling, svc.cfg.RunMode = billing, config.RunModeStandard
			_, audio, _, err := svc.ForwardQwenRealtimeSpeech(ctx, meta, account, AzureSpeechRequest{Model: "public-speak", Input: "你好A。"})
			stored, readErr := repo.GetByPublicID(context.Background(), repo.created[0].PublicID)
			require.NoError(t, readErr)
			if tc.want > 0 {
				require.NoError(t, err)
				require.NotEmpty(t, audio)
				require.Equal(t, MediaJobStatusSucceeded, stored.Status)
				require.Equal(t, tc.want, gjson.GetBytes(stored.UpstreamResponseJSON, "usage.characters").Int())
			} else {
				require.ErrorIs(t, err, ErrMediaUsageIncomplete)
				require.Empty(t, audio)
				require.Equal(t, MediaJobStatusUnknown, stored.Status)
				require.Nil(t, stored.UsageRecordedAt)
				require.Zero(t, ledger.applied)
				require.Equal(t, tc.known, gjson.GetBytes(stored.UpstreamResponseJSON, "usage.characters").Int())
				require.False(t, gjson.GetBytes(stored.UpstreamResponseJSON, "usage_complete").Bool())
				require.NoError(t, svc.recordMediaUsage(context.Background(), stored))
				reloaded, err := repo.GetByPublicID(context.Background(), stored.PublicID)
				require.NoError(t, err)
				require.Nil(t, reloaded.UsageRecordedAt, "uncertain work must not be marked billed by reconciliation")
			}
		})
	}
}

type speechReconciliationStore struct{ *gatewayBillingStoreStub }

func (s *speechReconciliationStore) MarkReconciliation(_ context.Context, _ bc.Key, version int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.op.Version != version {
		return bc.ErrState
	}
	s.op.State, s.op.Version = bc.ReconciliationRequired, s.op.Version+1
	return nil
}

func TestRealtimeASRInterruptedTaskRetainsConfirmedUsageInLocalAndCentralModes(t *testing.T) {
	for _, central := range []bool{false, true} {
		for _, interruption := range []string{"client canceled", "supplier disconnected", "no usage"} {
			t.Run(fmt.Sprintf("central=%t/%s", central, interruption), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				task := uuid.NewString()
				svc, repo, account, _ := realtimeSpeechFixture(t, func(w http.ResponseWriter, r *http.Request) {
					conn, err := ws.Accept(w, r, nil)
					require.NoError(t, err)
					defer func() { _ = conn.CloseNow() }()
					_, _, err = conn.Read(ctx)
					require.NoError(t, err)
					speechJSON(t, conn, ctx, map[string]any{"header": map[string]any{"task_id": task, "event": "task-started"}, "payload": map[string]any{}})
					_, _, err = conn.Read(ctx)
					require.NoError(t, err)
					if interruption != "no usage" {
						speechJSON(t, conn, ctx, map[string]any{"header": map[string]any{"task_id": task, "event": "result-generated"}, "payload": map[string]any{"usage": map[string]any{"duration": 2}, "output": map[string]any{"sentence": map[string]any{"text": "已确认", "sentence_end": true}}}})
					}
					if interruption == "client canceled" {
						_, _, _ = conn.Read(ctx)
					}
				})
				billing, prices, ledger, _, meta, _ := mediaBillingFixture()
				prices.price.BillingMode = BillingModeAudio
				svc.mediaBilling, svc.cfg.RunMode = billing, config.RunModeStandard
				var store *speechReconciliationStore
				if central {
					coordinator, originalStore, _, principal := gatewayBillingFixture()
					store = &speechReconciliationStore{originalStore}
					coordinator.repo = store
					billing.apiKeys = &APIKeyService{GatewayBilling: coordinator}
					ctx = WithGatewayBillingSession(ctx, coordinator.Session(*store.route, principal, "proof", nil, meta.APIKey))
				}
				client := &speechTestClient{incoming: make(chan speechTestFrame, 1)}
				client.onWrite = func(data []byte) {
					if gjson.GetBytes(data, "header.event").String() == "task-started" {
						client.incoming <- speechTestFrame{ws.MessageBinary, []byte{1, 0}}
					}
					if gjson.GetBytes(data, "header.event").String() == "result-generated" {
						checkpoint, err := repo.GetByPublicID(context.Background(), repo.created[0].PublicID)
						require.NoError(t, err)
						require.Equal(t, float64(2), gjson.GetBytes(checkpoint.UpstreamResponseJSON, "usage.seconds").Float(), "usage is durable before forwarding the transcript")
						if interruption == "client canceled" {
							cancel()
						}
					}
				}
				require.Error(t, svc.StreamAudioTranscription(ctx, meta, account, "public-listen", speechStart("public-listen", task), client))
				stored, err := repo.GetByPublicID(context.Background(), repo.created[0].PublicID)
				require.NoError(t, err)
				require.Equal(t, MediaJobStatusUnknown, stored.Status)
				require.Nil(t, stored.UsageRecordedAt)
				require.Equal(t, "speech_usage_uncertain", stored.ErrorCode)
				require.NotContains(t, string(stored.UpstreamResponseJSON), "已确认")
				if interruption == "no usage" {
					require.False(t, gjson.GetBytes(stored.UpstreamResponseJSON, "usage.seconds").Exists())
				} else {
					require.Equal(t, float64(2), gjson.GetBytes(stored.UpstreamResponseJSON, "usage.seconds").Float())
				}
				require.Zero(t, ledger.applied, "partial usage is retained as evidence rather than pretending a final complete amount")
				require.NoError(t, svc.recordMediaUsage(context.Background(), stored))
				if central {
					require.Equal(t, bc.ReconciliationRequired, store.op.State)
				}
			})
		}
	}
}

func TestRealtimeSpeechBoundsAndNativeModelCapabilities(t *testing.T) {
	for _, model := range []string{"paraformer-realtime-v2", "fun-asr-realtime", "qwen-audio-3.0-asr-flash-streaming"} {
		require.Equal(t, []string{SpeechTaskTranscriptionStreaming}, implementedSpeechTasks(model))
	}
	for _, model := range []string{"qwen-audio-3.1-asr-flash-streaming", "qwen3-asr-flash-realtime", "gpt-realtime", "qwen3-asr-flash", "cosyvoice-v3.5-flash", "paraformer-realtime-8k-v2"} {
		require.Empty(t, implementedSpeechTasks(model))
	}
	q, ok, err := mediaBillingQuoteRequest("/v1/audio/transcriptions/stream", "op", []byte(`{"model":"public-listen"}`))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, bc.Decimal("600"), q.MaximumUsage["audio_seconds"])
	q, ok, err = mediaBillingQuoteRequest("/v1/audio/speech", "op", []byte(`{"model":"qwen3-tts-flash-realtime","input":"你好A。"}`))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, bc.Decimal("6"), q.MaximumUsage["audio_characters"])
}

func TestRealtimeASRCentralSessionQuotesBeforeDispatchAndSettlesExactAudioSeconds(t *testing.T) {
	coordinator, store, authority, principal := gatewayBillingFixture()
	billing, prices, ledger, _, meta, account := mediaBillingFixture()
	prices.price.BillingMode = BillingModeAudio
	billing.apiKeys = &APIKeyService{GatewayBilling: coordinator}
	meta.RequestJSON = []byte(`{"model":"paraformer-realtime-v2","max_duration_seconds":600}`)
	session := coordinator.Session(*store.route, principal, "transient-proof", nil, meta.APIKey)
	execution, err := session.PrepareMedia(context.Background(), "/audio/transcriptions/stream", "audio-op", meta.RequestJSON)
	require.NoError(t, err)
	require.Equal(t, bc.Decimal("600"), authority.quoted.MaximumUsage["audio_seconds"])
	require.Zero(t, authority.dispatches)
	ctx := bc.WithExecution(context.Background(), execution)
	snap, err := billing.Prepare(ctx, meta, account, "paraformer-realtime-v2", "paraformer-realtime-v2", MediaJobKindAudioTranscription, "")
	require.NoError(t, err)
	require.NotContains(t, string(snap.JSON()), "transient-proof")
	require.NoError(t, session.BeforeSupplierWrite(ctx, execution))
	require.Equal(t, 1, authority.dispatches)
	job := billingTestJob(snap)
	job.Kind = MediaJobKindAudioTranscription
	job.Model = "paraformer-realtime-v2"
	job.UpstreamResponseJSON = []byte(`{"usage":{"seconds":3}}`)
	require.NoError(t, billing.Settle(context.Background(), job))
	cmd := ledger.commands[job.PublicID]
	require.NotNil(t, cmd)
	require.Zero(t, cmd.BalanceCost)
	require.NotNil(t, cmd.CentralEvent)
	var settled bc.SettleRequest
	require.NoError(t, json.Unmarshal(cmd.CentralEvent.Body, &settled))
	require.Equal(t, bc.Decimal("3"), settled.Usage["audio_seconds"])
}
