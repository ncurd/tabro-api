package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newAudioTestService(response string) (*MediaGenerationService, *mediaGenerationHTTPUpstreamRecorder, *mediaGenerationJobRepoStub, *Account, MediaRequestMeta) {
	upstream := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(http.StatusOK, response)}
	repo := newMediaGenerationJobRepoStub()
	svc := NewMediaGenerationService(nil, repo, nil, upstream, &config.Config{RunMode: config.RunModeSimple}, nil)
	account := &Account{ID: 50, Platform: PlatformDashScope, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-secret"}}
	meta := MediaRequestMeta{UserID: 12, APIKeyID: 13, GroupID: int64PtrForMediaGenerationTest(14), RequestJSON: []byte(`{"audio_base64":"private-sample"}`)}
	return svc, upstream, repo, account, meta
}

func TestTranscribeAudioNativeProtocolAndRedaction(t *testing.T) {
	svc, upstream, repo, account, meta := newAudioTestService(`{"request_id":"asr-1","output":{"choices":[{"message":{"content":[{"text":"你好"},{"text":"世界"}]}}]},"usage":{"seconds":2}}`)
	itn := false
	result, err := svc.TranscribeAudio(context.Background(), meta, account, AudioTranscriptionRequest{Model: "qwen3-asr-flash", AudioBase64: "aGVsbG8=", MIMEType: "audio/wav", Language: "zh", Prompt: "专有词", EnableITN: &itn})
	require.NoError(t, err)
	require.Equal(t, "你好世界", result.Text)
	require.Equal(t, "asr-1", result.RequestID)
	require.Equal(t, int64(2), gjson.GetBytes(result.Usage, "seconds").Int())
	require.Equal(t, "https://dashscope.aliyuncs.com"+qwenMultimodalPath, upstream.lastReq.URL.String())
	require.Equal(t, "Bearer test-secret", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "system", gjson.GetBytes(upstream.lastBody, "input.messages.0.role").String())
	require.Equal(t, "专有词", gjson.GetBytes(upstream.lastBody, "input.messages.0.content.0.text").String())
	require.Equal(t, "data:audio/wav;base64,aGVsbG8=", gjson.GetBytes(upstream.lastBody, "input.messages.1.content.0.audio").String())
	require.Equal(t, "zh", gjson.GetBytes(upstream.lastBody, "parameters.asr_options.language").String())
	require.Equal(t, "false", gjson.GetBytes(upstream.lastBody, "parameters.asr_options.enable_itn").Raw)
	require.Len(t, repo.created, 1)
	require.Equal(t, `{}`, string(repo.created[0].RequestJSON))
	require.Equal(t, MediaJobKindAudioTranscription, repo.created[0].Kind)
}

func TestCreateClonedVoiceProtocolAndOwnerMetadata(t *testing.T) {
	svc, upstream, repo, account, meta := newAudioTestService(`{"request_id":"clone-1","output":{"voice":"qwen3-tts-vc-provider-secret","target_model":"qwen3-tts-vc-2026-01-22","fallback_mode":true,"fallback_reason":"quality"},"usage":{"count":1}}`)
	voice, err := svc.CreateClonedVoice(context.Background(), meta, account, VoiceCloneRequest{Model: QwenVoiceCloneModel, Name: "voice_1", AudioURL: "https://example.com/private-sample.wav", Text: "private transcript", Language: "zh"})
	require.NoError(t, err)
	require.Equal(t, "active", voice.Status)
	require.Equal(t, "audio.voice", voice.Object)
	require.Equal(t, account.ID, voice.AccountID)
	require.Equal(t, QwenVoiceCloneModel, voice.UpstreamModel)
	require.True(t, voice.FallbackMode)
	require.Contains(t, voice.ID, "voice_")
	require.Equal(t, QwenVoiceEnrollmentModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "create", gjson.GetBytes(upstream.lastBody, "input.action").String())
	require.Equal(t, QwenVoiceCloneModel, gjson.GetBytes(upstream.lastBody, "input.target_model").String())
	require.Equal(t, "https://example.com/private-sample.wav", gjson.GetBytes(upstream.lastBody, "input.audio.data").String())
	require.Equal(t, "private transcript", gjson.GetBytes(upstream.lastBody, "input.text").String())
	require.Len(t, repo.created, 1)
	stored := repo.created[0]
	require.Equal(t, meta.GroupID, stored.GroupID)
	require.NotContains(t, string(stored.RequestJSON), "private")
	require.NotContains(t, string(stored.RequestJSON), "audio")
	encoded, err := json.Marshal(voice)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "provider-secret")
	require.NotContains(t, string(encoded), "account")
}

func addTestClonedVoice(t *testing.T, repo *mediaGenerationJobRepoStub, meta MediaRequestMeta, account *Account) {
	t.Helper()
	require.NoError(t, repo.Create(context.Background(), &MediaGenerationJob{PublicID: "voice_owned", Kind: MediaJobKindVoiceClone, Status: MediaJobStatusSucceeded, Provider: MediaProviderDashScope, Platform: PlatformDashScope, AccountID: account.ID, UserID: meta.UserID, APIKeyID: meta.APIKeyID, GroupID: meta.GroupID, Model: QwenVoiceCloneModel, AudioVoice: "qwen-provider-voice", RequestJSON: []byte(`{"name":"owned","upstream_model":"qwen3-tts-vc-2026-01-22"}`)}))
}

func TestClonedVoiceOwnershipIncludesUserKeyAndGroup(t *testing.T) {
	svc, upstream, repo, account, meta := newAudioTestService(`{}`)
	addTestClonedVoice(t, repo, meta, account)
	cases := []MediaRequestMeta{
		{UserID: meta.UserID + 1, APIKeyID: meta.APIKeyID, GroupID: meta.GroupID},
		{UserID: meta.UserID, APIKeyID: meta.APIKeyID + 1, GroupID: meta.GroupID},
		{UserID: meta.UserID, APIKeyID: meta.APIKeyID, GroupID: int64PtrForMediaGenerationTest(15)},
		{UserID: meta.UserID, APIKeyID: meta.APIKeyID},
	}
	for _, wrongOwner := range cases {
		_, err := svc.GetClonedVoice(context.Background(), wrongOwner, "voice_owned")
		require.ErrorIs(t, err, ErrClonedVoiceNotFound)
		_, err = svc.SynthesizeClonedVoice(context.Background(), wrongOwner, account, ClonedSpeechRequest{Voice: "voice_owned", Input: "hi"})
		require.ErrorIs(t, err, ErrClonedVoiceNotFound)
		_, err = svc.DeleteClonedVoice(context.Background(), wrongOwner, account, "voice_owned")
		require.ErrorIs(t, err, ErrClonedVoiceNotFound)
	}
	_, err := svc.GetClonedVoice(context.Background(), meta, "qwen-provider-voice")
	require.ErrorIs(t, err, ErrClonedVoiceNotFound)
	require.Nil(t, upstream.lastReq)
	voice, err := svc.GetClonedVoice(context.Background(), meta, "voice_owned")
	require.NoError(t, err)
	require.Equal(t, account.ID, voice.AccountID)
}

func TestSynthesizeClonedVoiceUsesPinnedModelAndProviderVoice(t *testing.T) {
	svc, upstream, repo, account, meta := newAudioTestService(`{"request_id":"tts-1","output":{"audio":{"url":"http://example.com/result.wav","expires_at":1900000000}},"usage":{"characters":2}}`)
	addTestClonedVoice(t, repo, meta, account)
	result, err := svc.SynthesizeClonedVoice(context.Background(), meta, account, ClonedSpeechRequest{Voice: "voice_owned", Input: "你好", Language: "zh", ResponseFormat: "url"})
	require.NoError(t, err)
	require.Equal(t, "http://example.com/result.wav", result.URL)
	require.Equal(t, int64(1900000000), *result.ExpiresAt)
	require.Equal(t, "voice_owned", result.Voice)
	require.Equal(t, QwenVoiceCloneModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "qwen-provider-voice", gjson.GetBytes(upstream.lastBody, "input.voice").String())
	require.Equal(t, "Chinese", gjson.GetBytes(upstream.lastBody, "input.language_type").String())
	require.Equal(t, 2, repo.created[1].AudioCharacterCount)
	require.Equal(t, `{}`, string(repo.created[1].RequestJSON))
}

func TestClonedSpeechRejectsWrongAccountModelAndFormat(t *testing.T) {
	for _, tt := range []struct {
		name          string
		accountID     int64
		model, format string
	}{
		{"account", 51, QwenVoiceCloneModel, "url"},
		{"model", 50, "qwen3-tts-flash", "url"},
		{"format", 50, QwenVoiceCloneModel, "mp3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, upstream, repo, account, meta := newAudioTestService(`{}`)
			addTestClonedVoice(t, repo, meta, account)
			account.ID = tt.accountID
			_, err := svc.SynthesizeClonedVoice(context.Background(), meta, account, ClonedSpeechRequest{Voice: "voice_owned", Model: tt.model, Input: "hi", ResponseFormat: tt.format})
			require.Error(t, err)
			require.Nil(t, upstream.lastReq)
		})
	}
}

func TestDeleteClonedVoicePersistsDeletionAndIsIdempotent(t *testing.T) {
	svc, upstream, repo, account, meta := newAudioTestService(`{"request_id":"delete-1","output":{}}`)
	addTestClonedVoice(t, repo, meta, account)
	deleted, err := svc.DeleteClonedVoice(context.Background(), meta, account, "voice_owned")
	require.NoError(t, err)
	require.Equal(t, "deleted", deleted.Status)
	require.Equal(t, "delete", gjson.GetBytes(upstream.lastBody, "input.action").String())
	require.Equal(t, "qwen-provider-voice", gjson.GetBytes(upstream.lastBody, "input.voice").String())
	upstream.lastReq = nil
	_, err = svc.DeleteClonedVoice(context.Background(), meta, nil, "voice_owned")
	require.NoError(t, err)
	_, err = svc.SynthesizeClonedVoice(context.Background(), meta, account, ClonedSpeechRequest{Voice: "voice_owned", Input: "hi"})
	require.ErrorContains(t, err, "not active")
	require.Nil(t, upstream.lastReq)
}

func TestQwenAudioRejectsInvalidInputBeforeForwarding(t *testing.T) {
	for _, req := range []AudioTranscriptionRequest{
		{Model: "qwen3-asr-flash", AudioURL: "file:///private.wav"},
		{Model: "qwen3-asr-flash", AudioURL: "http://127.0.0.1/test.wav"},
		{Model: "qwen3-asr-flash", AudioURL: "https://example.com/test.wav", AudioBase64: "aGVsbG8=", MIMEType: "audio/wav"},
		{Model: "qwen3-asr-flash", AudioBase64: "aGVsbG8="},
		{Model: "qwen3-asr-flash", AudioBase64: "!bad!", MIMEType: "audio/wav"},
		{Model: "qwen3-asr-flash", AudioURL: "data:image/png;base64,aGVsbG8="},
		{Model: "qwen3-asr-flash-filetrans", AudioURL: "https://example.com/test.wav"},
	} {
		svc, upstream, _, account, meta := newAudioTestService(`{}`)
		_, err := svc.TranscribeAudio(context.Background(), meta, account, req)
		require.Error(t, err)
		require.Nil(t, upstream.lastReq)
	}
	_, err := normalizeAudioInput("data:audio/ogg;base64,aGVsbG8=", "", "", true)
	require.ErrorContains(t, err, "voice cloning requires")
}

func TestQwenAudioFailsClosedWithoutBillingInStandardMode(t *testing.T) {
	svc, upstream, _, account, meta := newAudioTestService(`{}`)
	svc.cfg.RunMode = config.RunModeStandard
	_, err := svc.TranscribeAudio(context.Background(), meta, account, AudioTranscriptionRequest{Model: "qwen3-asr-flash", AudioURL: "https://example.com/test.wav"})
	require.ErrorContains(t, err, "billing")
	require.Nil(t, upstream.lastReq)
}

func TestQwenAudioRejectsMalformedSuccess(t *testing.T) {
	for _, response := range []string{`{"output":{}}`, `{"code":"BadRequest","message":"invalid audio"}`, `not-json`} {
		svc, _, repo, account, meta := newAudioTestService(response)
		_, err := svc.TranscribeAudio(context.Background(), meta, account, AudioTranscriptionRequest{Model: "qwen3-asr-flash", AudioURL: "https://example.com/test.wav"})
		require.Error(t, err)
		require.Len(t, repo.created, 1)
		stored, err := repo.GetByPublicID(context.Background(), repo.created[0].PublicID)
		require.NoError(t, err)
		require.Equal(t, MediaJobStatusFailed, stored.Status)
	}
}

type unavailableAudioRepository struct{ *mediaGenerationJobRepoStub }

func (r unavailableAudioRepository) Create(context.Context, *MediaGenerationJob) error {
	return errors.New("database unavailable")
}

func TestQwenAudioPersistsBeforePaidUpstream(t *testing.T) {
	svc, upstream, repo, account, meta := newAudioTestService(`{}`)
	svc.jobRepo = unavailableAudioRepository{repo}
	_, err := svc.CreateClonedVoice(context.Background(), meta, account, VoiceCloneRequest{Model: QwenVoiceCloneModel, Name: "voice", AudioURL: "https://example.com/test.wav"})
	require.ErrorContains(t, err, "database unavailable")
	require.Nil(t, upstream.lastReq)
	_, err = svc.TranscribeAudio(context.Background(), meta, account, AudioTranscriptionRequest{Model: "qwen3-asr-flash", AudioURL: "https://example.com/test.wav"})
	require.ErrorContains(t, err, "database unavailable")
	require.Nil(t, upstream.lastReq)
}

func TestClonedVoiceDeletionSurvivesModelWhitelistChange(t *testing.T) {
	svc, upstream, repo, account, meta := newAudioTestService(`{"request_id":"clone-1","output":{"voice":"provider-voice","target_model":"qwen3-tts-vc-2026-01-22"}}`)
	voice, err := svc.CreateClonedVoice(context.Background(), meta, account, VoiceCloneRequest{Model: QwenVoiceCloneModel, Name: "voice", AudioURL: "https://example.com/test.wav"})
	require.NoError(t, err)
	stored, err := repo.GetByPublicID(context.Background(), voice.ID)
	require.NoError(t, err)
	// Enrollment IDs live in the persisted provider response, never the public resource.
	require.Equal(t, "provider-voice", clonedVoiceProviderID(stored))
	account.Credentials["model_mapping"] = map[string]any{"other-model": "other-model"}
	upstream.respBody = nil
	upstream.resp = jsonResponse(http.StatusOK, `{"request_id":"delete-1","output":{}}`)
	deleted, err := svc.DeleteClonedVoice(context.Background(), meta, account, voice.ID)
	require.NoError(t, err)
	require.Equal(t, "deleted", deleted.Status)
	require.Equal(t, "provider-voice", gjson.GetBytes(upstream.lastBody, "input.voice").String())
}

func TestQwenAudioReturnsDurableSuccessWhileBillingRetries(t *testing.T) {
	billing, pricing, ledger, _, meta, account := mediaBillingFixture()
	pricing.price.BillingMode = BillingModePerRequest
	account.Credentials["api_key"] = "test-secret"
	ledger.err = errors.New("temporary ledger outage")
	upstream := &mediaGenerationHTTPUpstreamRecorder{resp: jsonResponse(http.StatusOK, `{"request_id":"clone-1","output":{"voice":"provider-voice","target_model":"qwen3-tts-vc-2026-01-22"},"usage":{"count":1}}`)}
	repo := newMediaGenerationJobRepoStub()
	svc := NewMediaGenerationService(nil, repo, nil, upstream, &config.Config{RunMode: config.RunModeStandard}, billing)
	voice, err := svc.CreateClonedVoice(context.Background(), meta, account, VoiceCloneRequest{Model: QwenVoiceCloneModel, Name: "voice", AudioURL: "https://example.com/test.wav"})
	require.NoError(t, err)
	require.Equal(t, "active", voice.Status)
	job, err := repo.GetByPublicID(context.Background(), voice.ID)
	require.NoError(t, err)
	require.Equal(t, MediaJobStatusSucceeded, job.Status)
	require.NotEmpty(t, job.BillingSnapshotJSON)
	require.Nil(t, job.UsageRecordedAt)
	ledger.err = nil
	require.NoError(t, billing.Settle(context.Background(), job))
	require.NoError(t, billing.Settle(context.Background(), job))
	require.Equal(t, 1, ledger.applied)
	require.Equal(t, QwenVoiceEnrollmentModel, ledger.commands[job.PublicID].Model)
}

func TestClonedSpeechAcceptsRenamedAliasForSameEnrollmentModel(t *testing.T) {
	svc, upstream, repo, account, meta := newAudioTestService(`{"request_id":"tts-1","output":{"audio":{"url":"https://example.com/result.wav"}},"usage":{"characters":2}}`)
	addTestClonedVoice(t, repo, meta, account)
	repo.jobs["voice_owned"].Model = "old-clone-alias"
	account.Credentials["model_mapping"] = map[string]any{"new-clone-alias": QwenVoiceCloneModel}
	result, err := svc.SynthesizeClonedVoice(context.Background(), meta, account, ClonedSpeechRequest{Voice: "voice_owned", Model: "new-clone-alias", Input: "hi"})
	require.NoError(t, err)
	require.Equal(t, "new-clone-alias", result.Model)
	require.Equal(t, QwenVoiceCloneModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.Nil(t, result.ExpiresAt, "expiry must not be invented when the provider omits it")
}

func TestQwenAudioResolvesChannelThenAccountModel(t *testing.T) {
	t.Run("transcription", func(t *testing.T) {
		svc, upstream, repo, account, meta := newAudioTestService(`{"request_id":"asr-1","output":{"choices":[{"message":{"content":[{"text":"hello"}]}}]},"usage":{"seconds":1}}`)
		svc.mediaBilling = &MediaBillingService{pricing: &mediaPricingStub{mapping: ChannelMappingResult{MappedModel: "account-asr"}}}
		account.Credentials["model_mapping"] = map[string]any{"account-asr": "qwen3-asr-flash"}
		result, err := svc.TranscribeAudio(context.Background(), meta, account, AudioTranscriptionRequest{Model: "public-asr", AudioURL: "https://example.com/test.wav"})
		require.NoError(t, err)
		require.Equal(t, "public-asr", result.Model)
		require.Equal(t, "public-asr", repo.created[0].Model)
		require.Equal(t, "qwen3-asr-flash", gjson.GetBytes(upstream.lastBody, "model").String())
	})
	t.Run("clone_and_speech", func(t *testing.T) {
		svc, upstream, repo, account, meta := newAudioTestService(`{"request_id":"clone-1","output":{"voice":"provider-voice","target_model":"qwen3-tts-vc-2026-01-22"},"usage":{"count":1}}`)
		svc.mediaBilling = &MediaBillingService{pricing: &mediaPricingStub{mapping: ChannelMappingResult{MappedModel: "account-clone"}}}
		account.Credentials["model_mapping"] = map[string]any{"account-clone": QwenVoiceCloneModel}
		voice, err := svc.CreateClonedVoice(context.Background(), meta, account, VoiceCloneRequest{Model: "public-clone", Name: "voice", AudioURL: "https://example.com/test.wav"})
		require.NoError(t, err)
		require.Equal(t, "public-clone", voice.Model)
		require.Equal(t, "public-clone", repo.created[0].Model)
		require.Equal(t, QwenVoiceCloneModel, voice.UpstreamModel)
		require.Equal(t, QwenVoiceCloneModel, gjson.GetBytes(upstream.lastBody, "input.target_model").String())
		upstream.respBody = nil
		upstream.resp = jsonResponse(http.StatusOK, `{"request_id":"tts-1","output":{"audio":{"url":"https://example.com/result.wav"}},"usage":{"characters":2}}`)
		result, err := svc.SynthesizeClonedVoice(context.Background(), meta, account, ClonedSpeechRequest{Model: "public-clone", Voice: voice.ID, Input: "hi"})
		require.NoError(t, err)
		require.Equal(t, "public-clone", result.Model)
		require.Equal(t, QwenVoiceCloneModel, gjson.GetBytes(upstream.lastBody, "model").String())
	})
}
