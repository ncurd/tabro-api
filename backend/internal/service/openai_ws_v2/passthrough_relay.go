package openai_ws_v2

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/tidwall/gjson"
)

const (
	defaultUpstreamDrainTimeout = 30 * time.Second
	defaultMaxActiveTurns       = 16
)

// ErrMaxActiveTurnsExceeded indicates that a single relay connection already
// has the maximum number of provider turns in flight.
var ErrMaxActiveTurnsExceeded = errors.New("maximum active websocket turns exceeded")

// ErrSessionExpired indicates that the authenticated websocket session may no
// longer send frames to the provider.
var ErrSessionExpired = errors.New("websocket session expired")

type FrameConn interface {
	ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error)
	WriteFrame(ctx context.Context, msgType coderws.MessageType, payload []byte) error
	Close() error
}

type Usage struct {
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
	ServiceTier              string
}

// RelayPreparedTurn is the validated, optionally rewritten response.create
// frame that will be sent upstream. RequestModel remains the client-facing
// model while UpstreamModel records the model after routing/mapping.
type RelayPreparedTurn struct {
	Payload       []byte
	RequestModel  string
	UpstreamModel string
}

type RelayResult struct {
	RequestModel            string
	Usage                   Usage
	RequestID               string
	TerminalEventType       string
	FirstTokenMs            *int
	Duration                time.Duration
	ClientToUpstreamFrames  int64
	UpstreamToClientFrames  int64
	DroppedDownstreamFrames int64
}

type RelayTurnResult struct {
	Turn              int
	RequestModel      string
	UpstreamModel     string
	Usage             Usage
	RequestID         string
	TerminalEventType string
	Duration          time.Duration
	FirstTokenMs      *int
}

type RelayExit struct {
	Turn            int
	Stage           string
	Err             error
	WroteDownstream bool
}

type RelayOptions struct {
	WriteTimeout         time.Duration
	IdleTimeout          time.Duration
	UpstreamDrainTimeout time.Duration
	MaxActiveTurns       int
	SessionExpiresAt     time.Time
	FirstMessageType     coderws.MessageType
	OnPrepareTurn        func(turn int, msgType coderws.MessageType, payload []byte) (RelayPreparedTurn, error)
	OnBeforeTurn         func(turn int) error
	OnUsageParseFailure  func(eventType string, usageRaw string)
	OnTurnComplete       func(turn RelayTurnResult)
	OnTrace              func(event RelayTraceEvent)
	Now                  func() time.Time
}

type RelayTraceEvent struct {
	Stage           string
	Direction       string
	MessageType     string
	PayloadBytes    int
	Graceful        bool
	WroteDownstream bool
	Error           string
}

type relayState struct {
	aggregatesMu      sync.Mutex
	usage             Usage
	requestModel      string
	lastResponseID    string
	terminalEventType string
	firstTokenMs      *int
	turnTimingByID    map[string]*relayTurnTiming

	turnsMu              sync.Mutex
	activeTurns          map[int]*relayActiveTurn
	pendingTurns         []int
	activeResponseTurns  map[string]int
	completedResponseIDs map[string]struct{}
	drainRequested       atomic.Bool
}

type relayActiveTurn struct {
	turn          int
	requestModel  string
	upstreamModel string
	startedAt     time.Time
	firstTokenMs  *int
	responseID    string
}

type relaySessionGate struct {
	expiresAt time.Time
	mu        sync.Mutex
	expired   atomic.Bool
}

type relayExitSignal struct {
	turn            int
	stage           string
	err             error
	graceful        bool
	wroteDownstream bool
}

type observedUpstreamEvent struct {
	terminal         bool
	completedTurn    bool
	allTurnsTerminal bool
	turn             int
	eventType        string
	responseID       string
	requestModel     string
	upstreamModel    string
	usage            Usage
	duration         time.Duration
	firstToken       *int
}

type relayTurnWriteError struct {
	turn  int
	stage string
	err   error
}

func (e *relayTurnWriteError) Error() string {
	if e == nil || e.err == nil {
		return "websocket turn was not written upstream"
	}
	return e.err.Error()
}

func (e *relayTurnWriteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

type relayTurnTiming struct {
	startAt      time.Time
	firstTokenMs *int
}

func (g *relaySessionGate) checkValid() error {
	if g == nil || g.expiresAt.IsZero() {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.expired.Load() || !time.Now().Before(g.expiresAt) {
		g.expired.Store(true)
		return ErrSessionExpired
	}
	return nil
}

func (g *relaySessionGate) writeIfValid(write func(expiresAt time.Time) error) error {
	if write == nil {
		return nil
	}
	if g == nil || g.expiresAt.IsZero() {
		return write(time.Time{})
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.expired.Load() || !time.Now().Before(g.expiresAt) {
		g.expired.Store(true)
		return ErrSessionExpired
	}
	return write(g.expiresAt)
}

func (g *relaySessionGate) expire() bool {
	if g == nil || g.expiresAt.IsZero() {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.expired.Load() {
		return false
	}
	g.expired.Store(true)
	return true
}

func Relay(
	ctx context.Context,
	clientConn FrameConn,
	upstreamConn FrameConn,
	firstClientMessage []byte,
	options RelayOptions,
) (RelayResult, *RelayExit) {
	result := RelayResult{RequestModel: strings.TrimSpace(gjson.GetBytes(firstClientMessage, "model").String())}
	if clientConn == nil || upstreamConn == nil {
		return result, &RelayExit{Stage: "relay_init", Err: errors.New("relay connection is nil")}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	nowFn := options.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	writeTimeout := options.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = 2 * time.Minute
	}
	drainTimeout := options.UpstreamDrainTimeout
	if drainTimeout <= 0 {
		drainTimeout = defaultUpstreamDrainTimeout
	}
	maxActiveTurns := options.MaxActiveTurns
	if maxActiveTurns <= 0 {
		maxActiveTurns = defaultMaxActiveTurns
	}
	firstMessageType := options.FirstMessageType
	if firstMessageType != coderws.MessageBinary {
		firstMessageType = coderws.MessageText
	}
	sessionGate := &relaySessionGate{expiresAt: options.SessionExpiresAt}
	if err := sessionGate.checkValid(); err != nil {
		return result, &RelayExit{Turn: 1, Stage: "session_expired", Err: err}
	}
	firstPrepared, err := prepareRelayTurn(options.OnPrepareTurn, 1, firstMessageType, firstClientMessage)
	if err != nil {
		return result, &RelayExit{Turn: 1, Stage: "prepare_turn", Err: err}
	}
	if err := sessionGate.checkValid(); err != nil {
		return result, &RelayExit{Turn: 1, Stage: "session_expired", Err: err}
	}
	firstClientMessage = firstPrepared.Payload
	result.RequestModel = firstPrepared.RequestModel
	startAt := nowFn()
	state := &relayState{requestModel: result.RequestModel}
	onTrace := options.OnTrace

	relayCtx, relayCancel := context.WithCancel(ctx)
	defer relayCancel()

	lastActivity := atomic.Int64{}
	lastActivity.Store(nowFn().UnixNano())
	markActivity := func() {
		lastActivity.Store(nowFn().UnixNano())
	}

	writeUpstream := func(msgType coderws.MessageType, payload []byte) error {
		return sessionGate.writeIfValid(func(expiresAt time.Time) error {
			writeDeadline := time.Now().Add(writeTimeout)
			if !expiresAt.IsZero() && expiresAt.Before(writeDeadline) {
				writeDeadline = expiresAt
			}
			writeCtx, cancel := context.WithDeadline(relayCtx, writeDeadline)
			defer cancel()
			return upstreamConn.WriteFrame(writeCtx, msgType, payload)
		})
	}
	writeClient := func(msgType coderws.MessageType, payload []byte) error {
		writeCtx, cancel := context.WithTimeout(relayCtx, writeTimeout)
		defer cancel()
		return clientConn.WriteFrame(writeCtx, msgType, payload)
	}

	clientToUpstreamFrames := &atomic.Int64{}
	upstreamToClientFrames := &atomic.Int64{}
	droppedDownstreamFrames := &atomic.Int64{}
	emitRelayTrace(onTrace, RelayTraceEvent{
		Stage:        "relay_start",
		PayloadBytes: len(firstClientMessage),
		MessageType:  relayMessageTypeString(firstMessageType),
	})

	if options.OnBeforeTurn != nil {
		if err := options.OnBeforeTurn(1); err != nil {
			result.Duration = nowFn().Sub(startAt)
			emitRelayTrace(onTrace, RelayTraceEvent{
				Stage:     "before_turn_failed",
				Direction: "client_to_upstream",
				Error:     err.Error(),
			})
			return result, &RelayExit{Turn: 1, Stage: "before_turn", Err: err}
		}
	}
	if err := sessionGate.checkValid(); err != nil {
		result.Duration = nowFn().Sub(startAt)
		return result, &RelayExit{Turn: 1, Stage: "session_expired", Err: err}
	}
	state.markTurnStarted(1, firstPrepared, startAt)
	if err := writeUpstream(firstMessageType, firstClientMessage); err != nil {
		state.markTurnAborted(1)
		result.Duration = nowFn().Sub(startAt)
		stage := "write_upstream"
		if errors.Is(err, ErrSessionExpired) {
			stage = "session_expired"
		}
		emitRelayTrace(onTrace, RelayTraceEvent{
			Stage:        "write_first_message_failed",
			Direction:    "client_to_upstream",
			MessageType:  relayMessageTypeString(firstMessageType),
			PayloadBytes: len(firstClientMessage),
			Error:        err.Error(),
		})
		return result, &RelayExit{Turn: 1, Stage: stage, Err: err}
	}
	clientToUpstreamFrames.Add(1)
	emitRelayTrace(onTrace, RelayTraceEvent{
		Stage:        "write_first_message_ok",
		Direction:    "client_to_upstream",
		MessageType:  relayMessageTypeString(firstMessageType),
		PayloadBytes: len(firstClientMessage),
	})
	markActivity()

	exitCh := make(chan relayExitSignal, 8)
	dropDownstreamWrites := atomic.Bool{}
	startedTurns := atomic.Int32{}
	startedTurns.Store(1)
	writeClientFrameUpstream := func(msgType coderws.MessageType, payload []byte) error {
		if err := sessionGate.checkValid(); err != nil {
			return &relayTurnWriteError{stage: "session_expired", err: err}
		}
		if isResponseCreateClientFrame(msgType, payload) {
			turn := int(startedTurns.Load()) + 1
			if state.activeTurnCount() >= maxActiveTurns {
				return &relayTurnWriteError{turn: turn, stage: "active_turn_limit", err: ErrMaxActiveTurnsExceeded}
			}
			prepared, err := prepareRelayTurn(options.OnPrepareTurn, turn, msgType, payload)
			if err != nil {
				return &relayTurnWriteError{turn: turn, stage: "prepare_turn", err: err}
			}
			if err := sessionGate.checkValid(); err != nil {
				return &relayTurnWriteError{turn: turn, stage: "session_expired", err: err}
			}
			if options.OnBeforeTurn != nil {
				if err := options.OnBeforeTurn(turn); err != nil {
					return &relayTurnWriteError{turn: turn, stage: "before_turn", err: err}
				}
			}
			if err := sessionGate.checkValid(); err != nil {
				return &relayTurnWriteError{turn: turn, stage: "session_expired", err: err}
			}
			state.markTurnStarted(turn, prepared, nowFn())
			if err := writeUpstream(msgType, prepared.Payload); err != nil {
				state.markTurnAborted(turn)
				stage := "write_upstream"
				if errors.Is(err, ErrSessionExpired) {
					stage = "session_expired"
				}
				return &relayTurnWriteError{turn: turn, stage: stage, err: err}
			}
			startedTurns.Add(1)
			return nil
		}
		if err := writeUpstream(msgType, payload); err != nil {
			if errors.Is(err, ErrSessionExpired) {
				return &relayTurnWriteError{stage: "session_expired", err: err}
			}
			return err
		}
		return nil
	}
	go runClientToUpstream(relayCtx, clientConn, writeClientFrameUpstream, markActivity, clientToUpstreamFrames, onTrace, exitCh)
	go runUpstreamToClient(
		relayCtx,
		upstreamConn,
		writeClient,
		startAt,
		nowFn,
		state,
		options.OnUsageParseFailure,
		options.OnTurnComplete,
		&dropDownstreamWrites,
		upstreamToClientFrames,
		droppedDownstreamFrames,
		markActivity,
		onTrace,
		exitCh,
	)
	go runIdleWatchdog(relayCtx, nowFn, options.IdleTimeout, &lastActivity, onTrace, exitCh)
	go runSessionExpiryWatchdog(relayCtx, sessionGate, state, &dropDownstreamWrites, exitCh)

	firstExit := <-exitCh
	emitRelayTrace(onTrace, RelayTraceEvent{
		Stage:           "first_exit",
		Direction:       relayDirectionFromStage(firstExit.stage),
		Graceful:        firstExit.graceful,
		WroteDownstream: firstExit.wroteDownstream,
		Error:           relayErrorString(firstExit.err),
	})
	combinedWroteDownstream := firstExit.wroteDownstream
	secondExit := relayExitSignal{graceful: true}
	hasSecondExit := false

	// 客户端断开或后续 turn 被拒绝后，在有界时间内继续读取上游，
	// 直到所有已经写入上游的 turn 都收到 terminal 事件，避免丢失 usage。
	drainAfterFirstExit := shouldDrainActiveTurns(firstExit)
	if drainAfterFirstExit {
		state.requestDrain()
		if isClientDisconnectExit(firstExit) {
			dropDownstreamWrites.Store(true)
		}
		var extraWroteDownstream bool
		secondExit, hasSecondExit, extraWroteDownstream = waitRelayDrainExit(exitCh, state, drainTimeout)
		combinedWroteDownstream = combinedWroteDownstream || extraWroteDownstream
	} else {
		relayCancel()
		_ = upstreamConn.Close()
		secondExit, hasSecondExit = waitRelayExit(exitCh, 200*time.Millisecond)
	}
	if hasSecondExit {
		combinedWroteDownstream = combinedWroteDownstream || secondExit.wroteDownstream
		emitRelayTrace(onTrace, RelayTraceEvent{
			Stage:           "second_exit",
			Direction:       relayDirectionFromStage(secondExit.stage),
			Graceful:        secondExit.graceful,
			WroteDownstream: secondExit.wroteDownstream,
			Error:           relayErrorString(secondExit.err),
		})
	}

	relayCancel()
	_ = upstreamConn.Close()

	enrichResult(&result, state, nowFn().Sub(startAt))
	result.ClientToUpstreamFrames = clientToUpstreamFrames.Load()
	result.UpstreamToClientFrames = upstreamToClientFrames.Load()
	result.DroppedDownstreamFrames = droppedDownstreamFrames.Load()
	if drainAfterFirstExit {
		stage := firstExit.stage
		exitTurn := firstExit.turn
		if isClientDisconnectExit(firstExit) {
			stage = "client_disconnected"
		}
		exitErr := firstExit.err
		if hasSecondExit && !secondExit.graceful && !isRejectedTurnExit(firstExit) {
			stage = secondExit.stage
			exitTurn = secondExit.turn
			exitErr = secondExit.err
		}
		if exitErr == nil {
			exitErr = io.EOF
		}
		emitRelayTrace(onTrace, RelayTraceEvent{
			Stage:           "relay_exit",
			Direction:       relayDirectionFromStage(stage),
			Graceful:        false,
			WroteDownstream: combinedWroteDownstream,
			Error:           relayErrorString(exitErr),
		})
		return result, &RelayExit{
			Turn:            exitTurn,
			Stage:           stage,
			Err:             exitErr,
			WroteDownstream: combinedWroteDownstream,
		}
	}
	if firstExit.graceful && (!hasSecondExit || secondExit.graceful) {
		emitRelayTrace(onTrace, RelayTraceEvent{
			Stage:           "relay_complete",
			Graceful:        true,
			WroteDownstream: combinedWroteDownstream,
		})
		_ = clientConn.Close()
		return result, nil
	}
	if !firstExit.graceful {
		emitRelayTrace(onTrace, RelayTraceEvent{
			Stage:           "relay_exit",
			Direction:       relayDirectionFromStage(firstExit.stage),
			Graceful:        false,
			WroteDownstream: combinedWroteDownstream,
			Error:           relayErrorString(firstExit.err),
		})
		return result, &RelayExit{
			Turn:            firstExit.turn,
			Stage:           firstExit.stage,
			Err:             firstExit.err,
			WroteDownstream: combinedWroteDownstream,
		}
	}
	if hasSecondExit && !secondExit.graceful {
		emitRelayTrace(onTrace, RelayTraceEvent{
			Stage:           "relay_exit",
			Direction:       relayDirectionFromStage(secondExit.stage),
			Graceful:        false,
			WroteDownstream: combinedWroteDownstream,
			Error:           relayErrorString(secondExit.err),
		})
		return result, &RelayExit{
			Turn:            secondExit.turn,
			Stage:           secondExit.stage,
			Err:             secondExit.err,
			WroteDownstream: combinedWroteDownstream,
		}
	}
	emitRelayTrace(onTrace, RelayTraceEvent{
		Stage:           "relay_complete",
		Graceful:        true,
		WroteDownstream: combinedWroteDownstream,
	})
	_ = clientConn.Close()
	return result, nil
}

func runClientToUpstream(
	ctx context.Context,
	clientConn FrameConn,
	writeUpstream func(msgType coderws.MessageType, payload []byte) error,
	markActivity func(),
	forwardedFrames *atomic.Int64,
	onTrace func(event RelayTraceEvent),
	exitCh chan<- relayExitSignal,
) {
	for {
		msgType, payload, err := clientConn.ReadFrame(ctx)
		if err != nil {
			emitRelayTrace(onTrace, RelayTraceEvent{
				Stage:     "read_client_failed",
				Direction: "client_to_upstream",
				Error:     err.Error(),
				Graceful:  isDisconnectError(err),
			})
			exitCh <- relayExitSignal{stage: "read_client", err: err, graceful: isDisconnectError(err)}
			return
		}
		markActivity()
		if err := writeUpstream(msgType, payload); err != nil {
			stage := "write_upstream"
			turn := 0
			var turnWriteErr *relayTurnWriteError
			if errors.As(err, &turnWriteErr) {
				stage = turnWriteErr.stage
				turn = turnWriteErr.turn
			}
			emitRelayTrace(onTrace, RelayTraceEvent{
				Stage:        stage + "_failed",
				Direction:    "client_to_upstream",
				MessageType:  relayMessageTypeString(msgType),
				PayloadBytes: len(payload),
				Error:        err.Error(),
			})
			exitCh <- relayExitSignal{turn: turn, stage: stage, err: err}
			return
		}
		if forwardedFrames != nil {
			forwardedFrames.Add(1)
		}
		markActivity()
	}
}

func runUpstreamToClient(
	ctx context.Context,
	upstreamConn FrameConn,
	writeClient func(msgType coderws.MessageType, payload []byte) error,
	startAt time.Time,
	nowFn func() time.Time,
	state *relayState,
	onUsageParseFailure func(eventType string, usageRaw string),
	onTurnComplete func(turn RelayTurnResult),
	dropDownstreamWrites *atomic.Bool,
	forwardedFrames *atomic.Int64,
	droppedFrames *atomic.Int64,
	markActivity func(),
	onTrace func(event RelayTraceEvent),
	exitCh chan<- relayExitSignal,
) {
	wroteDownstream := false
	for {
		msgType, payload, err := upstreamConn.ReadFrame(ctx)
		if err != nil {
			emitRelayTrace(onTrace, RelayTraceEvent{
				Stage:           "read_upstream_failed",
				Direction:       "upstream_to_client",
				Error:           err.Error(),
				Graceful:        isDisconnectError(err),
				WroteDownstream: wroteDownstream,
			})
			exitCh <- relayExitSignal{
				stage:           "read_upstream",
				err:             err,
				graceful:        isDisconnectError(err),
				wroteDownstream: wroteDownstream,
			}
			return
		}
		markActivity()
		observedEvent := observedUpstreamEvent{}
		switch msgType {
		case coderws.MessageText:
			observedEvent = observeUpstreamMessage(state, payload, startAt, nowFn, onUsageParseFailure)
		case coderws.MessageBinary:
			// binary frame 直接透传，不进入 JSON 观测路径（避免无效解析开销）。
		}
		emitTurnComplete(onTurnComplete, state, observedEvent)
		if dropDownstreamWrites != nil && dropDownstreamWrites.Load() {
			if droppedFrames != nil {
				droppedFrames.Add(1)
			}
			emitRelayTrace(onTrace, RelayTraceEvent{
				Stage:           "drop_downstream_frame",
				Direction:       "upstream_to_client",
				MessageType:     relayMessageTypeString(msgType),
				PayloadBytes:    len(payload),
				WroteDownstream: wroteDownstream,
			})
			if observedEvent.terminal && observedEvent.allTurnsTerminal {
				exitCh <- relayExitSignal{
					stage:           "drain_terminal",
					graceful:        true,
					wroteDownstream: wroteDownstream,
				}
				return
			}
			markActivity()
			continue
		}
		if err := writeClient(msgType, payload); err != nil {
			graceful := isDisconnectError(err)
			emitRelayTrace(onTrace, RelayTraceEvent{
				Stage:           "write_client_failed",
				Direction:       "upstream_to_client",
				MessageType:     relayMessageTypeString(msgType),
				PayloadBytes:    len(payload),
				WroteDownstream: wroteDownstream,
				Error:           err.Error(),
				Graceful:        graceful,
			})
			if dropDownstreamWrites != nil {
				dropDownstreamWrites.Store(true)
			}
			if state != nil {
				state.requestDrain()
			}
			exitCh <- relayExitSignal{stage: "write_client", err: err, graceful: graceful, wroteDownstream: wroteDownstream}
			if state == nil || state.activeTurnCount() == 0 || (observedEvent.terminal && observedEvent.allTurnsTerminal) {
				return
			}
			continue
		}
		wroteDownstream = true
		if forwardedFrames != nil {
			forwardedFrames.Add(1)
		}
		markActivity()
		if observedEvent.terminal && observedEvent.allTurnsTerminal && state != nil && state.shouldDrain() {
			exitCh <- relayExitSignal{
				stage:           "drain_terminal",
				graceful:        true,
				wroteDownstream: wroteDownstream,
			}
			return
		}
	}
}

func runIdleWatchdog(
	ctx context.Context,
	nowFn func() time.Time,
	idleTimeout time.Duration,
	lastActivity *atomic.Int64,
	onTrace func(event RelayTraceEvent),
	exitCh chan<- relayExitSignal,
) {
	if idleTimeout <= 0 {
		return
	}
	checkInterval := minDuration(idleTimeout/4, 5*time.Second)
	if checkInterval < time.Second {
		checkInterval = time.Second
	}
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			last := time.Unix(0, lastActivity.Load())
			if nowFn().Sub(last) < idleTimeout {
				continue
			}
			emitRelayTrace(onTrace, RelayTraceEvent{
				Stage:     "idle_timeout_triggered",
				Direction: "watchdog",
				Error:     context.DeadlineExceeded.Error(),
			})
			exitCh <- relayExitSignal{stage: "idle_timeout", err: context.DeadlineExceeded}
			return
		}
	}
}

func runSessionExpiryWatchdog(
	ctx context.Context,
	sessionGate *relaySessionGate,
	state *relayState,
	dropDownstreamWrites *atomic.Bool,
	exitCh chan<- relayExitSignal,
) {
	if sessionGate == nil || sessionGate.expiresAt.IsZero() {
		return
	}
	wait := time.Until(sessionGate.expiresAt)
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
	if !sessionGate.expire() {
		return
	}
	if dropDownstreamWrites != nil {
		dropDownstreamWrites.Store(true)
	}
	if state != nil {
		state.requestDrain()
	}
	select {
	case exitCh <- relayExitSignal{stage: "session_expired", err: ErrSessionExpired}:
	case <-ctx.Done():
	}
}

func emitRelayTrace(onTrace func(event RelayTraceEvent), event RelayTraceEvent) {
	if onTrace == nil {
		return
	}
	onTrace(event)
}

func relayMessageTypeString(msgType coderws.MessageType) string {
	switch msgType {
	case coderws.MessageText:
		return "text"
	case coderws.MessageBinary:
		return "binary"
	default:
		return "unknown(" + strconv.Itoa(int(msgType)) + ")"
	}
}

func isResponseCreateClientFrame(msgType coderws.MessageType, payload []byte) bool {
	if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
		return false
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return false
	}
	return strings.TrimSpace(envelope.Type) == "response.create"
}

func prepareRelayTurn(
	onPrepare func(turn int, msgType coderws.MessageType, payload []byte) (RelayPreparedTurn, error),
	turn int,
	msgType coderws.MessageType,
	payload []byte,
) (RelayPreparedTurn, error) {
	prepared := RelayPreparedTurn{Payload: payload}
	if onPrepare != nil {
		var err error
		prepared, err = onPrepare(turn, msgType, payload)
		if err != nil {
			return RelayPreparedTurn{}, err
		}
		if prepared.Payload == nil {
			prepared.Payload = payload
		}
	}
	if prepared.RequestModel == "" {
		prepared.RequestModel = strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	}
	if prepared.UpstreamModel == "" {
		prepared.UpstreamModel = strings.TrimSpace(gjson.GetBytes(prepared.Payload, "model").String())
	}
	if prepared.UpstreamModel == "" {
		prepared.UpstreamModel = prepared.RequestModel
	}
	return prepared, nil
}

func relayDirectionFromStage(stage string) string {
	switch stage {
	case "read_client", "write_upstream", "prepare_turn", "before_turn", "active_turn_limit":
		return "client_to_upstream"
	case "read_upstream", "write_client", "drain_terminal":
		return "upstream_to_client"
	case "idle_timeout":
		return "watchdog"
	case "session_expired":
		return "session"
	default:
		return ""
	}
}

func relayErrorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type relayObservedTurn struct {
	turn             int
	requestModel     string
	upstreamModel    string
	duration         time.Duration
	firstTokenMs     *int
	completed        bool
	allTurnsTerminal bool
}

func (s *relayState) markTurnStarted(turn int, prepared RelayPreparedTurn, startedAt time.Time) {
	if s == nil || turn <= 0 {
		return
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	if s.activeTurns == nil {
		s.activeTurns = make(map[int]*relayActiveTurn, 4)
	}
	if s.activeResponseTurns == nil {
		s.activeResponseTurns = make(map[string]int, 4)
	}
	if _, exists := s.activeTurns[turn]; exists {
		return
	}
	s.activeTurns[turn] = &relayActiveTurn{
		turn:          turn,
		requestModel:  strings.TrimSpace(prepared.RequestModel),
		upstreamModel: strings.TrimSpace(prepared.UpstreamModel),
		startedAt:     startedAt,
	}
	s.pendingTurns = append(s.pendingTurns, turn)
}

func (s *relayState) markTurnAborted(turn int) {
	if s == nil || turn <= 0 {
		return
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	active := s.activeTurns[turn]
	if active == nil {
		return
	}
	delete(s.activeTurns, turn)
	if active.responseID != "" {
		delete(s.activeResponseTurns, active.responseID)
	}
	s.removePendingTurnLocked(turn)
}

func (s *relayState) requestDrain() {
	if s != nil {
		s.drainRequested.Store(true)
	}
}

func (s *relayState) shouldDrain() bool {
	return s != nil && s.drainRequested.Load()
}

func (s *relayState) activeTurnCount() int {
	if s == nil {
		return 0
	}
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	return len(s.activeTurns)
}

func (s *relayState) observeTurnEvent(responseID string, tokenEvent bool, terminal bool, now time.Time) relayObservedTurn {
	observed := relayObservedTurn{}
	if s == nil {
		observed.allTurnsTerminal = true
		return observed
	}
	responseID = strings.TrimSpace(responseID)
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()

	if terminal && responseID != "" {
		if _, duplicate := s.completedResponseIDs[responseID]; duplicate {
			observed.allTurnsTerminal = len(s.activeTurns) == 0
			return observed
		}
	}

	active := s.activeTurnForResponseLocked(responseID)
	if active == nil && (responseID != "" || terminal) {
		active = s.bindOldestPendingTurnLocked(responseID)
	}
	if active != nil {
		observed.turn = active.turn
		observed.requestModel = active.requestModel
		observed.upstreamModel = active.upstreamModel
		if tokenEvent && active.firstTokenMs == nil && !active.startedAt.IsZero() {
			ms := int(now.Sub(active.startedAt).Milliseconds())
			if ms >= 0 {
				active.firstTokenMs = &ms
			}
		}
		observed.firstTokenMs = openAIWSRelayCloneIntPtr(active.firstTokenMs)
	}

	if terminal {
		if responseID != "" {
			if s.completedResponseIDs == nil {
				s.completedResponseIDs = make(map[string]struct{}, 4)
			}
			s.completedResponseIDs[responseID] = struct{}{}
		}
		if active != nil {
			if !active.startedAt.IsZero() {
				observed.duration = now.Sub(active.startedAt)
				if observed.duration < 0 {
					observed.duration = 0
				}
			}
			delete(s.activeTurns, active.turn)
			if active.responseID != "" {
				delete(s.activeResponseTurns, active.responseID)
			}
			s.removePendingTurnLocked(active.turn)
			observed.completed = true
		}
	}
	observed.allTurnsTerminal = len(s.activeTurns) == 0
	return observed
}

func (s *relayState) activeTurnForResponseLocked(responseID string) *relayActiveTurn {
	if s == nil || responseID == "" {
		return nil
	}
	turn := s.activeResponseTurns[responseID]
	if turn <= 0 {
		return nil
	}
	return s.activeTurns[turn]
}

func (s *relayState) bindOldestPendingTurnLocked(responseID string) *relayActiveTurn {
	if s == nil {
		return nil
	}
	for len(s.pendingTurns) > 0 {
		turn := s.pendingTurns[0]
		s.pendingTurns = s.pendingTurns[1:]
		active := s.activeTurns[turn]
		if active == nil || active.responseID != "" {
			continue
		}
		if responseID != "" {
			active.responseID = responseID
			if s.activeResponseTurns == nil {
				s.activeResponseTurns = make(map[string]int, 4)
			}
			s.activeResponseTurns[responseID] = turn
		}
		return active
	}
	return nil
}

func (s *relayState) removePendingTurnLocked(turn int) {
	for i, pendingTurn := range s.pendingTurns {
		if pendingTurn != turn {
			continue
		}
		copy(s.pendingTurns[i:], s.pendingTurns[i+1:])
		s.pendingTurns = s.pendingTurns[:len(s.pendingTurns)-1]
		return
	}
}

func observeUpstreamMessage(
	state *relayState,
	message []byte,
	startAt time.Time,
	nowFn func() time.Time,
	onUsageParseFailure func(eventType string, usageRaw string),
) observedUpstreamEvent {
	if state == nil || len(message) == 0 {
		return observedUpstreamEvent{}
	}
	values := gjson.GetManyBytes(message, "type", "response.id", "response_id", "id")
	eventType := strings.TrimSpace(values[0].String())
	if eventType == "" {
		return observedUpstreamEvent{}
	}
	responseID := strings.TrimSpace(values[1].String())
	if responseID == "" {
		responseID = strings.TrimSpace(values[2].String())
	}
	// 仅 terminal 事件兜底读取顶层 id，避免把 event_id 当成 response_id 关联到 turn。
	if responseID == "" && isTerminalEvent(eventType) {
		responseID = strings.TrimSpace(values[3].String())
	}
	now := nowFn()
	tokenEvent := isTokenEvent(eventType)
	terminalEvent := isTerminalEvent(eventType)

	state.observeAggregateMetadata(eventType, responseID, tokenEvent, terminalEvent, startAt, now)
	parsedUsage := parseUsageAndAccumulate(state, message, eventType, onUsageParseFailure)
	turnObservation := state.observeTurnEvent(responseID, tokenEvent, terminalEvent, now)
	observed := observedUpstreamEvent{
		terminal:         terminalEvent,
		completedTurn:    turnObservation.completed,
		allTurnsTerminal: turnObservation.allTurnsTerminal,
		turn:             turnObservation.turn,
		eventType:        eventType,
		responseID:       responseID,
		requestModel:     turnObservation.requestModel,
		upstreamModel:    turnObservation.upstreamModel,
		usage:            parsedUsage,
		duration:         turnObservation.duration,
		firstToken:       openAIWSRelayCloneIntPtr(turnObservation.firstTokenMs),
	}
	if !terminalEvent {
		return observed
	}
	return observed
}

func (s *relayState) observeAggregateMetadata(
	eventType string,
	responseID string,
	tokenEvent bool,
	terminalEvent bool,
	startAt time.Time,
	now time.Time,
) {
	if s == nil {
		return
	}
	s.aggregatesMu.Lock()
	defer s.aggregatesMu.Unlock()

	if s.firstTokenMs == nil && tokenEvent {
		ms := int(now.Sub(startAt).Milliseconds())
		if ms >= 0 {
			s.firstTokenMs = &ms
		}
	}
	if !terminalEvent {
		return
	}
	s.terminalEventType = eventType
	if responseID != "" {
		s.lastResponseID = responseID
	}
}

func emitTurnComplete(
	onTurnComplete func(turn RelayTurnResult),
	state *relayState,
	observed observedUpstreamEvent,
) {
	if onTurnComplete == nil || !observed.terminal || !observed.completedTurn {
		return
	}
	responseID := strings.TrimSpace(observed.responseID)
	if responseID == "" {
		return
	}
	onTurnComplete(RelayTurnResult{
		Turn:              observed.turn,
		RequestModel:      observed.requestModel,
		UpstreamModel:     observed.upstreamModel,
		Usage:             observed.usage,
		RequestID:         responseID,
		TerminalEventType: observed.eventType,
		Duration:          observed.duration,
		FirstTokenMs:      openAIWSRelayCloneIntPtr(observed.firstToken),
	})
}

func openAIWSRelayGetOrInitTurnTiming(state *relayState, responseID string, now time.Time) *relayTurnTiming {
	if state == nil {
		return nil
	}
	if state.turnTimingByID == nil {
		state.turnTimingByID = make(map[string]*relayTurnTiming, 8)
	}
	timing, ok := state.turnTimingByID[responseID]
	if !ok || timing == nil || timing.startAt.IsZero() {
		timing = &relayTurnTiming{startAt: now}
		state.turnTimingByID[responseID] = timing
		return timing
	}
	return timing
}

func openAIWSRelayDeleteTurnTiming(state *relayState, responseID string) (relayTurnTiming, bool) {
	if state == nil || state.turnTimingByID == nil {
		return relayTurnTiming{}, false
	}
	timing, ok := state.turnTimingByID[responseID]
	if !ok || timing == nil {
		return relayTurnTiming{}, false
	}
	delete(state.turnTimingByID, responseID)
	return *timing, true
}

func openAIWSRelayCloneIntPtr(v *int) *int {
	if v == nil {
		return nil
	}
	cloned := *v
	return &cloned
}

func parseUsageAndAccumulate(
	state *relayState,
	message []byte,
	eventType string,
	onParseFailure func(eventType string, usageRaw string),
) Usage {
	if state == nil || len(message) == 0 || !shouldParseUsage(eventType) {
		return Usage{}
	}
	usageResult := gjson.GetBytes(message, "response.usage")
	if !usageResult.Exists() {
		return Usage{}
	}
	usageRaw := strings.TrimSpace(usageResult.Raw)
	if usageRaw == "" || !strings.HasPrefix(usageRaw, "{") {
		recordUsageParseFailure()
		if onParseFailure != nil {
			onParseFailure(eventType, usageRaw)
		}
		return Usage{}
	}

	inputResult := gjson.GetBytes(message, "response.usage.input_tokens")
	outputResult := gjson.GetBytes(message, "response.usage.output_tokens")
	cachedResult := gjson.GetBytes(message, "response.usage.input_tokens_details.cached_tokens")
	cacheWriteResult := gjson.GetBytes(message, "response.usage.input_tokens_details.cache_write_tokens")
	serviceTier := strings.TrimSpace(gjson.GetBytes(message, "response.service_tier").String())

	inputTokens, inputOK := parseUsageIntField(inputResult, true)
	outputTokens, outputOK := parseUsageIntField(outputResult, true)
	cachedTokens, cachedOK := parseUsageIntField(cachedResult, false)
	cacheWriteTokens, cacheWriteOK := parseUsageIntField(cacheWriteResult, false)
	if !inputOK || !outputOK || !cachedOK || !cacheWriteOK {
		recordUsageParseFailure()
		if onParseFailure != nil {
			onParseFailure(eventType, usageRaw)
		}
		// 解析失败时不做部分字段累加，避免计费 usage 出现“半有效”状态。
		return Usage{}
	}
	parsedUsage := Usage{
		InputTokens:              inputTokens,
		OutputTokens:             outputTokens,
		CacheReadInputTokens:     cachedTokens,
		CacheCreationInputTokens: cacheWriteTokens,
		ServiceTier:              serviceTier,
	}

	state.aggregatesMu.Lock()
	defer state.aggregatesMu.Unlock()
	state.usage.InputTokens += parsedUsage.InputTokens
	state.usage.OutputTokens += parsedUsage.OutputTokens
	state.usage.CacheReadInputTokens += parsedUsage.CacheReadInputTokens
	state.usage.CacheCreationInputTokens += parsedUsage.CacheCreationInputTokens
	if parsedUsage.ServiceTier != "" {
		state.usage.ServiceTier = parsedUsage.ServiceTier
	}
	return parsedUsage
}

func parseUsageIntField(value gjson.Result, required bool) (int, bool) {
	if !value.Exists() {
		return 0, !required
	}
	if value.Type != gjson.Number {
		return 0, false
	}
	return int(value.Int()), true
}

func enrichResult(result *RelayResult, state *relayState, duration time.Duration) {
	if result == nil {
		return
	}
	result.Duration = duration
	if state == nil {
		return
	}
	state.aggregatesMu.Lock()
	defer state.aggregatesMu.Unlock()
	result.RequestModel = state.requestModel
	result.Usage = state.usage
	result.RequestID = state.lastResponseID
	result.TerminalEventType = state.terminalEventType
	result.FirstTokenMs = openAIWSRelayCloneIntPtr(state.firstTokenMs)
}

func isDisconnectError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return true
	}
	switch coderws.CloseStatus(err) {
	case coderws.StatusNormalClosure, coderws.StatusGoingAway, coderws.StatusNoStatusRcvd, coderws.StatusAbnormalClosure:
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if message == "" {
		return false
	}
	return strings.Contains(message, "failed to read frame header: eof") ||
		strings.Contains(message, "unexpected eof") ||
		strings.Contains(message, "use of closed network connection") ||
		strings.Contains(message, "connection reset by peer") ||
		strings.Contains(message, "broken pipe")
}

func isTerminalEvent(eventType string) bool {
	switch eventType {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

func shouldParseUsage(eventType string) bool {
	return isTerminalEvent(eventType)
}

func isTokenEvent(eventType string) bool {
	if eventType == "" {
		return false
	}
	switch eventType {
	case "response.created", "response.in_progress", "response.output_item.added", "response.output_item.done":
		return false
	}
	if strings.Contains(eventType, ".delta") {
		return true
	}
	if strings.HasPrefix(eventType, "response.output_text") {
		return true
	}
	if strings.HasPrefix(eventType, "response.output") {
		return true
	}
	return eventType == "response.completed" || eventType == "response.done"
}

func minDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}

func shouldDrainActiveTurns(exit relayExitSignal) bool {
	if isClientDisconnectExit(exit) {
		return true
	}
	switch exit.stage {
	case "write_upstream", "write_client", "prepare_turn", "before_turn", "active_turn_limit", "session_expired":
		return true
	default:
		return false
	}
}

func isRejectedTurnExit(exit relayExitSignal) bool {
	return exit.stage == "prepare_turn" || exit.stage == "before_turn" || exit.stage == "active_turn_limit" || exit.stage == "session_expired"
}

func isClientDisconnectExit(exit relayExitSignal) bool {
	if !exit.graceful {
		return false
	}
	return exit.stage == "read_client" || exit.stage == "write_client"
}

func waitRelayDrainExit(
	exitCh <-chan relayExitSignal,
	state *relayState,
	timeout time.Duration,
) (relayExitSignal, bool, bool) {
	if state == nil || state.activeTurnCount() == 0 {
		return relayExitSignal{stage: "drain_terminal", graceful: true}, true, false
	}
	if timeout <= 0 {
		timeout = 200 * time.Millisecond
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	wroteDownstream := false
	for {
		select {
		case exit := <-exitCh:
			wroteDownstream = wroteDownstream || exit.wroteDownstream
			switch exit.stage {
			case "drain_terminal", "read_upstream", "idle_timeout":
				return exit, true, wroteDownstream
			case "write_client":
				// Downstream writes are disabled after the first failure. Keep
				// reading upstream until every provider turn is terminal.
			case "read_client", "write_upstream", "prepare_turn", "before_turn", "active_turn_limit", "session_expired":
				// These are client-side exits. The upstream reader remains active
				// until every accepted turn reaches a terminal event.
			default:
				if !exit.graceful {
					return exit, true, wroteDownstream
				}
			}
			if state.activeTurnCount() == 0 {
				return relayExitSignal{stage: "drain_terminal", graceful: true, wroteDownstream: wroteDownstream}, true, wroteDownstream
			}
		case <-timer.C:
			return relayExitSignal{}, false, wroteDownstream
		}
	}
}

func waitRelayExit(exitCh <-chan relayExitSignal, timeout time.Duration) (relayExitSignal, bool) {
	if timeout <= 0 {
		timeout = 200 * time.Millisecond
	}
	select {
	case sig := <-exitCh:
		return sig, true
	case <-time.After(timeout):
		return relayExitSignal{}, false
	}
}
