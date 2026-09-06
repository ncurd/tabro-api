package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSTurnUsageContextDerivesStableUniqueBillingIDs(t *testing.T) {
	baseCtx := context.WithValue(context.Background(), ctxkey.GatewayBillingRequestID, "connection-idempotency-base")
	connectionID := openAIWSBillingBaseID(baseCtx)
	secondConnectionID := openAIWSBillingBaseID(baseCtx)
	require.Len(t, connectionID, 64)
	require.Equal(t, connectionID, secondConnectionID, "the same Idempotency-Key must survive a websocket reconnect")

	turnOneA := openAIWSTurnUsageContext(baseCtx, connectionID, 42, 1)
	turnOneB := openAIWSTurnUsageContext(baseCtx, connectionID, 42, 1)
	turnTwo := openAIWSTurnUsageContext(baseCtx, connectionID, 42, 2)

	turnOneAID, _ := turnOneA.Value(ctxkey.GatewayBillingRequestID).(string)
	turnOneBID, _ := turnOneB.Value(ctxkey.GatewayBillingRequestID).(string)
	turnTwoID, _ := turnTwo.Value(ctxkey.GatewayBillingRequestID).(string)
	require.Len(t, turnOneAID, 64)
	require.Equal(t, turnOneAID, turnOneBID)
	require.NotEqual(t, turnOneAID, turnTwoID)
	require.NotEqual(t, "connection-idempotency-base", turnOneAID)

	reconnectedTurnOne := openAIWSTurnUsageContext(baseCtx, secondConnectionID, 42, 1)
	reconnectedTurnOneID, _ := reconnectedTurnOne.Value(ctxkey.GatewayBillingRequestID).(string)
	require.Equal(t, turnOneAID, reconnectedTurnOneID, "same key and turn must produce the same billing deduplication ID")

	withoutIdempotency := context.WithValue(context.Background(), ctxkey.ClientRequestID, "request-correlation-only")
	require.NotEqual(
		t,
		openAIWSBillingBaseID(withoutIdempotency),
		openAIWSBillingBaseID(withoutIdempotency),
		"connections without Idempotency-Key must receive independent billing generations",
	)

	payload := []byte(`{"type":"response.create","model":"gpt-6-astra"}`)
	require.Equal(t, openAIWSTurnPayloadHash(payload, 1), openAIWSTurnPayloadHash(payload, 1))
	require.NotEqual(t, openAIWSTurnPayloadHash(payload, 1), openAIWSTurnPayloadHash(payload, 2))
}

func TestOpenAIWSSessionContextKeepsAcceptedTurnAliveAtOIDCExpiration(t *testing.T) {
	expiresAt := time.Now().Add(50 * time.Millisecond)
	requestCtx := context.WithValue(context.Background(), ctxkey.OIDCExpiresAt, expiresAt)
	wsCtx, cancel := openAIWSSessionContext(requestCtx)
	defer cancel()

	select {
	case <-wsCtx.Done():
		t.Fatal("accepted websocket turn context must not be canceled at token expiration")
	case <-time.After(80 * time.Millisecond):
	}
	require.NoError(t, wsCtx.Err())
	require.True(t, openAIWSOIDCTokenExpired(wsCtx, time.Now()))
}

func TestOpenAIWSContextWithoutOIDCExpirationUsesParentLifetime(t *testing.T) {
	wsCtx, cancel := openAIWSSessionContext(context.Background())
	require.NoError(t, wsCtx.Err())
	require.False(t, openAIWSOIDCTokenExpired(wsCtx, time.Now()))

	cancel()
	require.ErrorIs(t, wsCtx.Err(), context.Canceled)
}

func TestParseOpenAIWSResponseCreateTurnValidatesEveryTurnPayload(t *testing.T) {
	t.Parallel()

	model, previousResponseID, err := parseOpenAIWSResponseCreateTurn([]byte(`{"type":"response.create","model":"gpt-6-astra","previous_response_id":"resp_123"}`))
	require.NoError(t, err)
	require.Equal(t, "gpt-6-astra", model)
	require.Equal(t, "resp_123", previousResponseID)

	model, _, err = parseOpenAIWSResponseCreateTurn([]byte(`{"type":"response.cancel","type":"response.create","model":"first-model","model":"last-model"}`))
	require.NoError(t, err)
	require.Equal(t, "last-model", model, "duplicate top-level keys must use encoding/json last-key-wins semantics")

	_, _, err = parseOpenAIWSResponseCreateTurn([]byte(`{"type":"response.create","type":"response.cancel","model":"gpt-6-astra"}`))
	require.ErrorContains(t, err, "unsupported websocket request type")

	tests := []struct {
		name    string
		payload string
		wantErr string
	}{
		{name: "invalid json", payload: `{"type":"response.create"`, wantErr: "invalid JSON"},
		{name: "wrong type", payload: `{"type":"response.cancel","model":"gpt-6-astra"}`, wantErr: "unsupported websocket request type"},
		{name: "missing model", payload: `{"type":"response.create"}`, wantErr: "model is required"},
		{name: "message id used as response id", payload: `{"type":"response.create","model":"gpt-6-astra","previous_response_id":"msg_123"}`, wantErr: "must be a response.id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseOpenAIWSResponseCreateTurn([]byte(tt.payload))
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestRewriteOpenAIWSResponseCreateTurnModelCanonicalizesDuplicateKeys(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"response.cancel","type":"response.create","model":"first-model","model":"client-model","input":[]}`)
	rewritten, err := rewriteOpenAIWSResponseCreateTurnModel(payload, "upstream-model")
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"response.create","model":"upstream-model","input":[]}`, string(rewritten))
	require.Equal(t, 1, strings.Count(string(rewritten), `"type"`))
	require.Equal(t, 1, strings.Count(string(rewritten), `"model"`))
}
