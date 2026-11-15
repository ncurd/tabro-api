package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	ws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestMediaSpeechDurationCapabilitiesRequireImplementedBoundedAdapter(t *testing.T) {
	for _, model := range []string{"qwen3-tts-flash-realtime", "qwen3-tts-flash-realtime-2026-01-15", "azure-tts"} {
		limit, supported := MediaSpeechOutputSecondsLimit(model)
		require.True(t, supported, model)
		require.Equal(t, 600, limit)
	}
	for _, model := range []string{"public-alias", "qwen3-tts-vc-2026-01-22", "qwen-audio-3.1-tts-next", "unknown"} {
		_, supported := MediaSpeechOutputSecondsLimit(model)
		require.False(t, supported, model)
	}
}

func speechDurationTestAppendWAVChunk(wav []byte, name string, data []byte) []byte {
	result := append([]byte(nil), wav...)
	result = append(result, name...)
	result = binary.LittleEndian.AppendUint32(result, uint32(len(data)))
	result = append(result, data...)
	if len(data)%2 != 0 {
		result = append(result, 0)
	}
	binary.LittleEndian.PutUint32(result[4:8], uint32(len(result)-8))
	return result
}

func TestMediaSpeechDurationParsesRealWAVDataAndRejectsAmbiguousAudio(t *testing.T) {
	pcm := make([]byte, 72000) // 1.5 seconds, independent of HTTP elapsed time.
	wav := pcmWAV(pcm, 24000)
	withMetadata := speechDurationTestAppendWAVChunk(wav, "LIST", []byte("metadata"))
	for _, valid := range [][]byte{wav, withMetadata} {
		size, err := mediaSpeechWAVPCMBytes(valid)
		require.NoError(t, err)
		require.Equal(t, int64(len(pcm)), size)
		seconds, err := mediaSpeechPCMSeconds(size)
		require.NoError(t, err)
		require.Equal(t, "1.5", seconds.String())
	}
	mutate := func(offset int, value uint32) []byte {
		body := append([]byte(nil), wav...)
		binary.LittleEndian.PutUint32(body[offset:offset+4], value)
		return body
	}
	for name, invalid := range map[string][]byte{
		"truncated data":         wav[:len(wav)-2],
		"forged RIFF size":       mutate(4, 36),
		"wrong rate":             mutate(24, 48000),
		"forged byte rate":       mutate(28, 1),
		"compressed encoding":    mutate(20, 0x10003),
		"duplicate data":         speechDurationTestAppendWAVChunk(wav, "data", []byte{1, 0}),
		"duplicate format":       speechDurationTestAppendWAVChunk(wav, "fmt ", wav[20:36]),
		"odd PCM sample":         speechDurationTestAppendWAVChunk(wav[:36], "data", []byte{1}),
		"metadata without audio": speechDurationTestAppendWAVChunk(wav[:36], "LIST", []byte{1, 0}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := mediaSpeechWAVPCMBytes(invalid)
			require.ErrorIs(t, err, ErrMediaUsageIncomplete)
		})
	}
	_, err := mediaSpeechPCMSeconds(3)
	require.ErrorIs(t, err, ErrMediaUsageIncomplete)
}

func TestRealtimeTTSDurationUsesOutputSamplesAndRetainsCharacters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pcm := make([]byte, 60000) // 1.25 seconds at the negotiated PCM format.
	svc, repo, account, _ := realtimeSpeechFixture(t, func(w http.ResponseWriter, r *http.Request) {
		conn, err := ws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() { _ = conn.CloseNow() }()
		_, update, err := conn.Read(ctx)
		require.NoError(t, err)
		require.Equal(t, int64(24000), gjson.GetBytes(update, "session.sample_rate").Int())
		require.Equal(t, "pcm", gjson.GetBytes(update, "session.response_format").String())
		speechJSON(t, conn, ctx, map[string]any{"type": "session.updated"})
		for i := 0; i < 3; i++ {
			_, _, err = conn.Read(ctx)
			require.NoError(t, err)
		}
		for _, chunk := range [][]byte{pcm[:48000], pcm[48000:]} {
			speechJSON(t, conn, ctx, map[string]any{"type": "response.audio.delta", "response_id": "response-1", "delta": base64.StdEncoding.EncodeToString(chunk)})
		}
		speechJSON(t, conn, ctx, map[string]any{"type": "response.done", "response": map[string]any{"id": "response-1", "usage": map[string]any{"characters": 25}}})
		speechJSON(t, conn, ctx, map[string]any{"type": "session.finished"})
	})
	billing, prices, ledger, _, meta, _ := mediaBillingFixture()
	price := 0.25
	prices.price = &ChannelModelPricing{BillingMode: BillingModeDuration, PriceUnit: "second", OutputAudioPricePerSecond: &price}
	svc.mediaBilling, svc.cfg.RunMode = billing, config.RunModeStandard
	_, audio, _, err := svc.ForwardQwenRealtimeSpeech(ctx, meta, account, AzureSpeechRequest{Model: "public-speak", Input: "短文本", ResponseFormat: "pcm"})
	require.NoError(t, err)
	require.Len(t, audio, len(pcm))
	stored, err := repo.GetByPublicID(ctx, repo.created[0].PublicID)
	require.NoError(t, err)
	require.Equal(t, "1.25", gjson.GetBytes(stored.UpstreamResponseJSON, "usage.audio_seconds").Raw)
	require.Equal(t, int64(25), gjson.GetBytes(stored.UpstreamResponseJSON, "usage.characters").Int())
	require.InDelta(t, 1.25*price*1.5, ledger.commands[stored.PublicID].BalanceCost, 1e-10)
	require.True(t, gjson.GetBytes(stored.UpstreamResponseJSON, "usage_complete").Bool())
}

func TestRealtimeTTSDurationStopsAtFrozenOutputLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Each frame fits the relay's event limit, but their total crosses 600s.
	chunk := base64.StdEncoding.EncodeToString(make([]byte, 1000000))
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
		message := []byte(`{"type":"response.audio.delta","delta":"` + chunk + `"}`)
		for i := 0; i < 29; i++ {
			if err := conn.Write(ctx, ws.MessageText, message); err != nil {
				return
			}
		}
		_, _, _ = conn.Read(ctx) // Wait until the relay closes this failed task.
	})
	billing, prices, ledger, _, meta, _ := mediaBillingFixture()
	price := 0.25
	prices.price = &ChannelModelPricing{BillingMode: BillingModeDuration, PriceUnit: "second", OutputAudioPricePerSecond: &price}
	svc.mediaBilling, svc.cfg.RunMode = billing, config.RunModeStandard
	_, audio, _, err := svc.ForwardQwenRealtimeSpeech(ctx, meta, account, AzureSpeechRequest{Model: "public-speak", Input: "test"})
	require.ErrorContains(t, err, "excessive synthesized audio")
	require.Empty(t, audio)
	stored, err := repo.GetByPublicID(context.Background(), repo.created[0].PublicID)
	require.NoError(t, err)
	require.Equal(t, MediaJobStatusUnknown, stored.Status)
	require.False(t, gjson.GetBytes(stored.UpstreamResponseJSON, "usage_complete").Bool())
	require.LessOrEqual(t, gjson.GetBytes(stored.UpstreamResponseJSON, "usage.audio_seconds").Float(), float64(600))
	require.Zero(t, ledger.applied, "a truncated task must remain under reconciliation")
}

func TestAzureTTSDurationMeasuresWAVAndRejectsUnmeasurableFormatsBeforeDispatch(t *testing.T) {
	for _, format := range []string{"wav", "pcm", "mp3", "opus", ""} {
		t.Run(format, func(t *testing.T) {
			wav := pcmWAV(make([]byte, 72000), 24000)
			upstream := &mediaGenerationHTTPUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"audio/wav"}}, Body: io.NopCloser(bytes.NewReader(wav))}}
			repo := newMediaGenerationJobRepoStub()
			billing, prices, ledger, _, meta, _ := mediaBillingFixture()
			price := 0.25
			prices.price = &ChannelModelPricing{BillingMode: BillingModeDuration, PriceUnit: "second", OutputAudioPricePerSecond: &price}
			svc := NewMediaGenerationService(nil, repo, nil, upstream, &config.Config{RunMode: config.RunModeStandard}, billing)
			account := &Account{ID: 50, Platform: PlatformAzureSpeech, Type: AccountTypeAPIKey, Credentials: map[string]any{"subscription_key": "test", "region": "eastus"}}
			_, audio, _, err := svc.ForwardAzureSpeech(context.Background(), account, AzureSpeechRequest{Model: "azure-tts", Input: "test", ResponseFormat: format}, meta)
			if format == "wav" || format == "pcm" {
				require.NoError(t, err)
				require.Equal(t, wav, audio)
				stored, err := repo.GetByPublicID(context.Background(), repo.created[0].PublicID)
				require.NoError(t, err)
				require.Equal(t, "1.5", gjson.GetBytes(stored.UpstreamResponseJSON, "usage.audio_seconds").Raw)
				require.Equal(t, int64(4), gjson.GetBytes(stored.UpstreamResponseJSON, "usage.characters").Int())
				require.InDelta(t, 1.5*price*1.5, ledger.commands[stored.PublicID].BalanceCost, 1e-10)
			} else {
				require.ErrorContains(t, err, "response_format wav or pcm")
				require.Nil(t, upstream.lastReq)
				require.Empty(t, repo.created)
			}
		})
	}
}

func TestAzureTTSDurationRejectsMalformedSupplierAudioWithoutCharging(t *testing.T) {
	upstream := &mediaGenerationHTTPUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("fake wav"))}}
	repo := newMediaGenerationJobRepoStub()
	billing, prices, ledger, _, meta, _ := mediaBillingFixture()
	price := 0.25
	prices.price = &ChannelModelPricing{BillingMode: BillingModeDuration, PriceUnit: "second", OutputAudioPricePerSecond: &price}
	svc := NewMediaGenerationService(nil, repo, nil, upstream, &config.Config{RunMode: config.RunModeStandard}, billing)
	account := &Account{ID: 50, Platform: PlatformAzureSpeech, Type: AccountTypeAPIKey, Credentials: map[string]any{"subscription_key": "test", "region": "eastus"}}
	_, audio, _, err := svc.ForwardAzureSpeech(context.Background(), account, AzureSpeechRequest{Model: "azure-tts", Input: "test", ResponseFormat: "wav"}, meta)
	require.ErrorIs(t, err, ErrMediaUsageIncomplete)
	require.Empty(t, audio)
	require.Zero(t, ledger.applied)
	stored, err := repo.GetByPublicID(context.Background(), repo.created[0].PublicID)
	require.NoError(t, err)
	require.Equal(t, MediaJobStatusUnknown, stored.Status)
}

func TestSpeechDurationRejectsUnboundedBatchAndURLBeforeDispatch(t *testing.T) {
	billing, prices, _, _, meta, _ := mediaBillingFixture()
	price := 0.25
	prices.price = &ChannelModelPricing{BillingMode: BillingModeDuration, PriceUnit: "second", OutputAudioPricePerSecond: &price}
	svc, upstream, repo, account, _ := newAudioTestService(`{}`)
	svc.mediaBilling, svc.cfg.RunMode = billing, config.RunModeStandard
	addTestClonedVoice(t, repo, meta, account)
	_, err := svc.SynthesizeClonedVoice(context.Background(), meta, account, ClonedSpeechRequest{Model: QwenVoiceCloneModel, Voice: "voice_owned", Input: "test"})
	require.ErrorContains(t, err, "URL-only cloned speech")
	require.Nil(t, upstream.lastReq)
	require.Len(t, repo.created, 1, "only the pre-existing voice resource may exist")
	account.Platform = PlatformAzureSpeech
	_, err = svc.CreateAudioSpeechJob(context.Background(), meta, account, AzureSpeechRequest{Model: "azure-tts", Input: "test"})
	require.ErrorContains(t, err, "batch synthesis has no enforced")
	require.Nil(t, upstream.lastReq)
	require.Len(t, repo.created, 1)
}
