package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	ws "github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const RealtimeSpeechMaxSeconds = 600
const realtimeSpeechFrameLimit = 256 * 1024
const realtimeSpeechAudioLimit = RealtimeSpeechMaxSeconds * 16000 * 2

type SpeechSocket interface {
	Read(context.Context) (ws.MessageType, []byte, error)
	Write(context.Context, ws.MessageType, []byte) error
}

type speechRoundTripper struct {
	service *MediaGenerationService
	account *Account
}

func (t speechRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.service.doUpstream(req, t.account)
}

func (s *MediaGenerationService) dialSpeech(ctx context.Context, account *Account, path, model string) (*ws.Conn, error) {
	base, err := url.Parse(dashScopeBaseURL(account))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || strings.Trim(base.Path, "/") != "" {
		return nil, &InvalidMediaRequestError{Message: "DashScope speech base_url must be the region or workspace root URL"}
	}
	switch base.Scheme {
	case "https":
		base.Scheme = "wss"
	case "http":
		base.Scheme = "ws"
	default:
		return nil, &InvalidMediaRequestError{Message: "invalid DashScope speech endpoint"}
	}
	base.Path = path
	if model != "" {
		q := base.Query()
		q.Set("model", model)
		base.RawQuery = q.Encode()
	}
	header := http.Header{"Authorization": {"Bearer " + account.GetCredential("api_key")}}
	client := &http.Client{Transport: speechRoundTripper{s, account}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	conn, response, err := ws.Dial(ctx, base.String(), &ws.DialOptions{HTTPHeader: header, HTTPClient: client})
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, fmt.Errorf("speech upstream connection failed")
	}
	conn.SetReadLimit(2 * 1024 * 1024)
	return conn, nil
}

func validateNativeASRStart(start []byte, model string) (string, error) {
	if len(start) > 64*1024 || !gjson.ValidBytes(start) || gjson.GetBytes(start, "header.action").String() != "run-task" || gjson.GetBytes(start, "header.streaming").String() != "duplex" || gjson.GetBytes(start, "payload.model").String() != model || gjson.GetBytes(start, "payload.task_group").String() != "audio" || gjson.GetBytes(start, "payload.task").String() != "asr" || gjson.GetBytes(start, "payload.function").String() != "recognition" || gjson.GetBytes(start, "payload.parameters.format").String() != "pcm" || gjson.GetBytes(start, "payload.parameters.sample_rate").Int() != 16000 {
		return "", &InvalidMediaRequestError{Message: "expected run-task for the selected model with mono PCM16 audio at 16000 Hz"}
	}
	task := gjson.GetBytes(start, "header.task_id").String()
	if _, err := uuid.Parse(task); err != nil {
		return "", &InvalidMediaRequestError{Message: "task_id must be a UUID"}
	}
	return task, nil
}

// StreamAudioTranscription handles one bounded task. Audio and transcripts are never stored in the media job.
func (s *MediaGenerationService) StreamAudioTranscription(ctx context.Context, meta MediaRequestMeta, account *Account, model string, start []byte, client SpeechSocket) (err error) {
	if err = validateQwenAudioAccount(account, model); err != nil {
		return err
	}
	task, err := validateNativeASRStart(start, model)
	if err != nil {
		return err
	}
	upstreamModel, err := s.resolveMediaUpstreamModel(ctx, meta, account, model)
	if err != nil {
		return err
	}
	if !IsNativeRealtimeASRModel(upstreamModel) {
		return &InvalidMediaRequestError{Message: "this model is not supported by the native realtime ASR relay; select Paraformer realtime, Fun-ASR realtime, or Qwen-Audio 3.0 ASR streaming"}
	}
	quoteBody, _ := json.Marshal(map[string]any{"model": model, "max_duration_seconds": RealtimeSpeechMaxSeconds})
	meta.RequestJSON = quoteBody
	if session := GatewayBillingSessionFromContext(ctx); session != nil {
		selected := session.WithKey(meta.APIKey)
		if selected == nil || selected.PrepareMedia == nil || selected.Finish == nil {
			return ErrMediaBillingUnavailable
		}
		operationID := HashUsageRequestPayload([]byte(fmt.Sprintf("speech\x00%d\x00%d\x00%s\x00%s", meta.UserID, meta.APIKeyID, meta.OperationID, task)))
		execution, prepareErr := selected.PrepareMedia(ctx, "/audio/transcriptions/stream", operationID, quoteBody)
		if prepareErr != nil {
			return prepareErr
		}
		ctx = bc.WithExecution(ctx, execution)
		defer func() {
			finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			_ = selected.Finish(finishCtx, execution)
		}()
	} else if bc.IsCentral(ctx) {
		return ErrMediaBillingUnavailable
	}
	snapshot, err := s.prepareQwenAudioBilling(ctx, meta, account, model, upstreamModel, MediaJobKindAudioTranscription)
	if err != nil {
		return err
	}
	job, err := s.beginQwenAudioJob(ctx, meta, account, model, MediaJobKindAudioTranscription, nil, snapshot, 0, "")
	if err != nil {
		return err
	}
	succeeded, dispatched := false, false
	var seconds float64
	defer func() {
		if !succeeded {
			if dispatched {
				err = errors.Join(err, s.retainUncertainSpeechUsage(ctx, job, speechUsageEvidence(task, "seconds", seconds, false)))
			} else {
				s.failQwenAudioJob(ctx, job)
			}
		}
	}()
	streamCtx, cancel := context.WithTimeout(ctx, (RealtimeSpeechMaxSeconds+15)*time.Second)
	defer cancel()
	upstream, err := s.dialSpeech(streamCtx, account, "/api-ws/v1/inference", "")
	if err != nil {
		return err
	}
	defer func() { _ = upstream.CloseNow() }()
	start, err = sjson.SetBytes(start, "payload.model", upstreamModel)
	if err != nil {
		return err
	}
	if err = bc.BeforeSupplierRequest(streamCtx); err != nil {
		return err
	}
	dispatched = true
	if err = upstream.Write(streamCtx, ws.MessageText, start); err != nil {
		return err
	}
	var ready atomic.Bool
	clientErrors := make(chan error, 1)
	go func() {
		var sent int64
		for {
			kind, data, readErr := client.Read(streamCtx)
			if readErr != nil {
				clientErrors <- readErr
				cancel()
				return
			}
			if kind == ws.MessageBinary {
				sent += int64(len(data))
				if !ready.Load() || len(data) > realtimeSpeechFrameLimit || len(data)%2 != 0 || sent > realtimeSpeechAudioLimit {
					clientErrors <- errors.New("invalid or excessive realtime audio")
					cancel()
					return
				}
			} else if kind != ws.MessageText || gjson.GetBytes(data, "header.action").String() != "finish-task" || gjson.GetBytes(data, "header.task_id").String() != task {
				clientErrors <- errors.New("only PCM frames and finish-task are permitted")
				cancel()
				return
			}
			if writeErr := upstream.Write(streamCtx, kind, data); writeErr != nil {
				clientErrors <- writeErr
				cancel()
				return
			}
			if kind == ws.MessageText {
				return
			}
		}
	}()
	for {
		kind, data, readErr := upstream.Read(streamCtx)
		if readErr != nil {
			select {
			case clientErr := <-clientErrors:
				return clientErr
			default:
				return readErr
			}
		}
		if kind != ws.MessageText || !gjson.ValidBytes(data) || gjson.GetBytes(data, "header.task_id").String() != task {
			return errors.New("invalid speech upstream event")
		}
		event := gjson.GetBytes(data, "header.event").String()
		if duration := gjson.GetBytes(data, "payload.usage.duration").Float(); duration > seconds && validMediaPrice(duration) {
			seconds = duration
			if err = s.checkpointSpeechUsage(ctx, job, speechUsageEvidence(task, "seconds", seconds, false), MediaJobStatusRunning); err != nil {
				return err
			}
		}
		switch event {
		case "task-started":
			ready.Store(true)
		case "result-generated":
		case "task-finished":
			if seconds <= 0 {
				return ErrMediaUsageIncomplete
			}
			usage := speechUsageEvidence(task, "seconds", seconds, true)
			if err = s.persistAndSettleQwenAudio(ctx, job, usage); err != nil {
				return err
			}
			succeeded = true
		case "task-failed":
			return errors.New("speech upstream task failed")
		default:
			return errors.New("unexpected speech upstream event")
		}
		if err = client.Write(streamCtx, ws.MessageText, data); err != nil {
			return err
		}
		if succeeded {
			return nil
		}
	}
}

// ForwardQwenRealtimeSpeech uses the provider's realtime TTS protocol and returns actual audio bytes.
func (s *MediaGenerationService) ForwardQwenRealtimeSpeech(ctx context.Context, meta MediaRequestMeta, account *Account, req AzureSpeechRequest) (result *MediaSyncAudioResult, audio []byte, headers http.Header, err error) {
	if err := validateQwenAudioAccount(account, req.Model); err != nil {
		return nil, nil, nil, err
	}
	upstreamModel, err := s.resolveMediaUpstreamModel(ctx, meta, account, req.Model)
	if err != nil {
		return nil, nil, nil, err
	}
	if !IsQwenRealtimeTTSModel(upstreamModel) {
		return nil, nil, nil, &InvalidMediaRequestError{Message: "this DashScope model is not supported by the realtime speech adapter"}
	}
	if strings.TrimSpace(req.Input) == "" || len([]rune(req.Input)) > 4000 {
		return nil, nil, nil, &InvalidMediaRequestError{Message: "realtime speech input must contain 1 to 4000 characters"}
	}
	if req.ResponseFormat != "" && req.ResponseFormat != "wav" && req.ResponseFormat != "pcm" {
		return nil, nil, nil, &InvalidMediaRequestError{Message: "realtime speech supports response_format wav or pcm"}
	}
	if req.Speed != 0 && req.Speed != 1 {
		return nil, nil, nil, &InvalidMediaRequestError{Message: "this realtime speech adapter currently supports speed 1 only"}
	}
	snapshot, err := s.prepareQwenAudioBilling(ctx, meta, account, req.Model, upstreamModel, MediaJobKindAudioSpeech)
	if err != nil {
		return nil, nil, nil, err
	}
	format := req.ResponseFormat
	if format == "" {
		format = "wav"
	}
	job, err := s.beginQwenAudioJob(ctx, meta, account, req.Model, MediaJobKindAudioSpeech, nil, snapshot, AliyunSpeechCharacters(req.Input), req.Voice, format)
	if err != nil {
		return nil, nil, nil, err
	}
	succeeded, dispatched := false, false
	requestID := ""
	var characters int64
	defer func() {
		if !succeeded {
			if dispatched {
				err = errors.Join(err, s.retainUncertainSpeechUsage(ctx, job, speechUsageEvidence(requestID, "characters", characters, false)))
			} else {
				s.failQwenAudioJob(ctx, job)
			}
		}
	}()
	streamCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	upstream, err := s.dialSpeech(streamCtx, account, "/api-ws/v1/realtime", upstreamModel)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = upstream.CloseNow() }()
	voice := req.Voice
	if voice == "" {
		voice = "Cherry"
	}
	write := func(value any) error {
		data, e := json.Marshal(value)
		if e != nil {
			return e
		}
		return upstream.Write(streamCtx, ws.MessageText, data)
	}
	if err = write(map[string]any{"event_id": uuid.NewString(), "type": "session.update", "session": map[string]any{"mode": "commit", "voice": voice, "response_format": "pcm", "sample_rate": 24000}}); err != nil {
		return nil, nil, nil, err
	}
	var pcm bytes.Buffer
	submitted := false
	responses := map[string]bool{}
	responseUsage := map[string]int64{}
	for {
		kind, data, readErr := upstream.Read(streamCtx)
		if readErr != nil {
			return nil, nil, nil, readErr
		}
		if kind != ws.MessageText || !gjson.ValidBytes(data) {
			return nil, nil, nil, errors.New("invalid realtime speech event")
		}
		switch gjson.GetBytes(data, "type").String() {
		case "session.created":
			requestID = gjson.GetBytes(data, "session.id").String()
		case "session.updated":
			if submitted {
				return nil, nil, nil, errors.New("duplicate realtime speech session update")
			}
			if err = bc.BeforeSupplierRequest(streamCtx); err != nil {
				return nil, nil, nil, err
			}
			dispatched = true
			if err = write(map[string]any{"event_id": uuid.NewString(), "type": "input_text_buffer.append", "text": req.Input}); err != nil {
				return nil, nil, nil, err
			}
			if err = write(map[string]any{"event_id": uuid.NewString(), "type": "input_text_buffer.commit"}); err != nil {
				return nil, nil, nil, err
			}
			if err = write(map[string]any{"event_id": uuid.NewString(), "type": "session.finish"}); err != nil {
				return nil, nil, nil, err
			}
			submitted = true
		case "response.created":
			if id := gjson.GetBytes(data, "response.id").String(); id != "" {
				responses[id] = true
			}
		case "response.audio.delta":
			if id := gjson.GetBytes(data, "response_id").String(); id != "" {
				responses[id] = true
			}
			decoded, e := base64.StdEncoding.DecodeString(gjson.GetBytes(data, "delta").String())
			if e != nil || pcm.Len()+len(decoded) > 32*1024*1024 {
				return nil, nil, nil, errors.New("invalid or excessive synthesized audio")
			}
			_, _ = pcm.Write(decoded)
		case "error":
			return nil, nil, nil, errors.New("realtime speech synthesis failed")
		case "response.done":
			if status := gjson.GetBytes(data, "response.status").String(); status != "" && status != "completed" {
				return nil, nil, nil, errors.New("realtime speech response did not complete")
			}
			id := firstNonEmpty(gjson.GetBytes(data, "response.id").String(), gjson.GetBytes(data, "response_id").String())
			quantity := gjson.GetBytes(data, "response.usage.characters")
			count, parseErr := strconv.ParseInt(quantity.Raw, 10, 64)
			if id == "" || quantity.Type != gjson.Number || parseErr != nil || count <= 0 {
				return nil, nil, nil, ErrMediaUsageIncomplete
			}
			if previous, exists := responseUsage[id]; exists {
				if previous != count {
					return nil, nil, nil, ErrMediaUsageIncomplete
				}
				continue // A repeated terminal event must not charge the same response twice.
			}
			if characters > math.MaxInt64-count {
				return nil, nil, nil, ErrMediaUsageIncomplete
			}
			responses[id], responseUsage[id] = true, count
			characters += count
			if err = s.checkpointSpeechUsage(ctx, job, speechUsageEvidence(requestID, "characters", characters, false), MediaJobStatusRunning); err != nil {
				return nil, nil, nil, err
			}
		case "session.finished":
			if !submitted || pcm.Len() == 0 || pcm.Len()%2 != 0 {
				return nil, nil, nil, errors.New("speech provider returned no audio")
			}
			if characters <= 0 || len(responseUsage) != len(responses) {
				return nil, nil, nil, ErrMediaUsageIncomplete
			}
			usage := speechUsageEvidence(requestID, "characters", characters, true)
			if err = s.persistAndSettleQwenAudio(ctx, job, usage); err != nil {
				return nil, nil, nil, err
			}
			succeeded = true
			audio, contentType := pcm.Bytes(), "audio/pcm"
			if req.ResponseFormat != "pcm" {
				audio = pcmWAV(audio, 24000)
				contentType = "audio/wav"
			}
			return &MediaSyncAudioResult{RequestID: requestID, ContentType: contentType}, audio, http.Header{"Content-Type": {contentType}}, nil
		}
	}
}

// A checkpoint is evidence of the supplier's cumulative usage, not proof that the task completed.
func speechUsageEvidence(requestID, meter string, value any, complete bool) []byte {
	usage := map[string]any{}
	switch n := value.(type) {
	case float64:
		if n > 0 {
			usage[meter] = n
		}
	case int64:
		if n > 0 {
			usage[meter] = n
		}
	}
	body, _ := json.Marshal(map[string]any{"request_id": requestID, "usage": usage, "usage_complete": complete})
	return body
}

func (s *MediaGenerationService) checkpointSpeechUsage(ctx context.Context, job *MediaGenerationJob, evidence []byte, status string) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	update := MediaGenerationJobUpdate{Status: status, UpstreamRequestID: gjson.GetBytes(evidence, "request_id").String(), UpstreamResponseJSON: evidence}
	if status == MediaJobStatusUnknown {
		update.UpstreamStatus, update.ErrorCode = "USAGE_INCOMPLETE", "speech_usage_uncertain"
		update.ErrorMessage = "Speech task ended without complete supplier usage; reconciliation is required."
	}
	updated, err := s.jobRepo.UpdateFromUpstream(persistCtx, job.PublicID, update)
	if err != nil {
		return err
	}
	if updated == nil {
		return errors.New("speech usage resource is missing")
	}
	*job = *updated
	return nil
}

func (s *MediaGenerationService) retainUncertainSpeechUsage(ctx context.Context, job *MediaGenerationJob, evidence []byte) error {
	if err := s.checkpointSpeechUsage(ctx, job, evidence, MediaJobStatusUnknown); err != nil {
		return err
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	return s.mediaBilling.ReconcileFailure(persistCtx, job)
}

func pcmWAV(pcm []byte, rate uint32) []byte {
	result := make([]byte, 44+len(pcm))
	copy(result, "RIFF")
	binary.LittleEndian.PutUint32(result[4:], uint32(36+len(pcm)))
	copy(result[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(result[16:], 16)
	binary.LittleEndian.PutUint16(result[20:], 1)
	binary.LittleEndian.PutUint16(result[22:], 1)
	binary.LittleEndian.PutUint32(result[24:], rate)
	binary.LittleEndian.PutUint32(result[28:], rate*2)
	binary.LittleEndian.PutUint16(result[32:], 2)
	binary.LittleEndian.PutUint16(result[34:], 16)
	copy(result[36:], "data")
	binary.LittleEndian.PutUint32(result[40:], uint32(len(pcm)))
	copy(result[44:], pcm)
	return result
}
