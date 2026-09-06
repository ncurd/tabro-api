package openai_ws_v2

import (
	"context"
	"errors"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestRelayBeforeTurnRejectsBeforeFirstUpstreamWrite(t *testing.T) {
	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn(nil, false)
	denied := errors.New("billing denied")

	_, relayExit := Relay(
		context.Background(),
		clientConn,
		upstreamConn,
		[]byte(`{"type":"response.create","model":"gpt-6-astra"}`),
		RelayOptions{OnBeforeTurn: func(turn int) error {
			require.Equal(t, 1, turn)
			return denied
		}},
	)

	require.NotNil(t, relayExit)
	require.Equal(t, "before_turn", relayExit.Stage)
	require.ErrorIs(t, relayExit.Err, denied)
	require.Empty(t, upstreamConn.Writes())
}

func TestRelayBeforeTurnRunsForEachResponseCreate(t *testing.T) {
	clientConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.create","model":"gpt-6-astra","input":"second"}`),
		},
	}, false)
	upstreamBase := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.completed","response":{"id":"resp_first","usage":{"input_tokens":4,"output_tokens":2}}}`),
		},
	}, true)
	upstreamConn := &delayedReadFrameConn{base: upstreamBase, firstDelay: 50 * time.Millisecond}
	secondTurnDenied := errors.New("second turn billing denied")
	turns := make(chan int, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result, relayExit := Relay(
		ctx,
		clientConn,
		upstreamConn,
		[]byte(`{"type":"response.create","model":"gpt-6-astra","input":"first"}`),
		RelayOptions{
			UpstreamDrainTimeout: 500 * time.Millisecond,
			OnBeforeTurn: func(turn int) error {
				turns <- turn
				if turn == 2 {
					return secondTurnDenied
				}
				return nil
			},
		},
	)

	require.NotNil(t, relayExit)
	require.Equal(t, "before_turn", relayExit.Stage)
	require.Equal(t, 2, relayExit.Turn)
	require.ErrorIs(t, relayExit.Err, secondTurnDenied)
	require.Equal(t, 1, <-turns)
	require.Equal(t, 2, <-turns)
	require.Len(t, upstreamBase.Writes(), 1, "rejected second turn must not reach the provider")
	require.Equal(t, "resp_first", result.RequestID, "the already-started turn must be drained to terminal usage")
	require.Equal(t, 4, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestIsResponseCreateClientFrame(t *testing.T) {
	require.True(t, isResponseCreateClientFrame(coderws.MessageText, []byte(`{"type":"response.create"}`)))
	require.True(t, isResponseCreateClientFrame(coderws.MessageBinary, []byte(`{"type":"response.create"}`)))
	require.True(t, isResponseCreateClientFrame(coderws.MessageText, []byte(`{"type":"response.cancel","type":"response.create"}`)))
	require.False(t, isResponseCreateClientFrame(coderws.MessageText, []byte(`{"type":"response.create","type":"response.cancel"}`)))
	require.False(t, isResponseCreateClientFrame(coderws.MessageText, []byte(`{"type":"response.cancel"}`)))
	require.False(t, isResponseCreateClientFrame(coderws.MessageText, []byte(`{"type":`)))
	require.False(t, isResponseCreateClientFrame(coderws.MessageType(99), []byte(`{"type":"response.create"}`)))
}

func TestRelayPrepareTurnRewritesEveryCreateAndPreservesStableMetadata(t *testing.T) {
	clientConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{
			msgType: coderws.MessageText,
			payload: []byte(`{"type":"response.create","model":"client-model-2","input":"second"}`),
		},
	}, false)
	upstreamBase := newPassthroughTestFrameConn([]passthroughTestFrame{
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.created","response":{"id":"resp_prepare_1"}}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.created","response":{"id":"resp_prepare_2"}}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_prepare_2","usage":{"input_tokens":2,"output_tokens":1}}}`)},
		{msgType: coderws.MessageText, payload: []byte(`{"type":"response.completed","response":{"id":"resp_prepare_1","usage":{"input_tokens":4,"output_tokens":3}}}`)},
	}, true)
	upstreamConn := &delayedReadFrameConn{base: upstreamBase, firstDelay: 50 * time.Millisecond}
	preparedTurns := make([]int, 0, 2)
	completedTurns := make([]RelayTurnResult, 0, 2)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, relayExit := Relay(
		ctx,
		clientConn,
		upstreamConn,
		[]byte(`{"type":"response.create","model":"client-model-1","input":"first"}`),
		RelayOptions{
			OnPrepareTurn: func(turn int, _ coderws.MessageType, payload []byte) (RelayPreparedTurn, error) {
				preparedTurns = append(preparedTurns, turn)
				if turn == 1 {
					return RelayPreparedTurn{
						Payload:       []byte(`{"type":"response.create","model":"upstream-model-1","input":"first"}`),
						RequestModel:  "client-model-1",
						UpstreamModel: "upstream-model-1",
					}, nil
				}
				return RelayPreparedTurn{
					Payload:       []byte(`{"type":"response.create","model":"upstream-model-2","input":"second"}`),
					RequestModel:  "client-model-2",
					UpstreamModel: "upstream-model-2",
				}, nil
			},
			OnTurnComplete: func(turn RelayTurnResult) {
				completedTurns = append(completedTurns, turn)
			},
		},
	)

	require.Nil(t, relayExit)
	require.Equal(t, []int{1, 2}, preparedTurns)
	upstreamWrites := upstreamBase.Writes()
	require.Len(t, upstreamWrites, 2)
	require.JSONEq(t, `{"type":"response.create","model":"upstream-model-1","input":"first"}`, string(upstreamWrites[0].payload))
	require.JSONEq(t, `{"type":"response.create","model":"upstream-model-2","input":"second"}`, string(upstreamWrites[1].payload))
	require.Len(t, completedTurns, 2)
	require.Equal(t, 2, completedTurns[0].Turn)
	require.Equal(t, "client-model-2", completedTurns[0].RequestModel)
	require.Equal(t, "upstream-model-2", completedTurns[0].UpstreamModel)
	require.Equal(t, 1, completedTurns[1].Turn)
	require.Equal(t, "client-model-1", completedTurns[1].RequestModel)
	require.Equal(t, "upstream-model-1", completedTurns[1].UpstreamModel)
}
