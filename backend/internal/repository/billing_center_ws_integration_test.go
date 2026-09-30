//go:build billing_integration

package repository

import (
	"context"
	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	wsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"io"
	"sync"
	"testing"
	"time"
)

type billingWSFrames struct {
	mu       sync.Mutex
	incoming [][]byte
	writes   [][]byte
	wait     bool
}

func (f *billingWSFrames) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	f.mu.Lock()
	if len(f.incoming) > 0 {
		body := f.incoming[0]
		f.incoming = f.incoming[1:]
		f.mu.Unlock()
		return coderws.MessageText, body, nil
	}
	f.mu.Unlock()
	if f.wait {
		<-ctx.Done()
		return 0, nil, ctx.Err()
	}
	return 0, nil, io.EOF
}
func (f *billingWSFrames) WriteFrame(_ context.Context, _ coderws.MessageType, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, append([]byte(nil), body...))
	return nil
}
func (f *billingWSFrames) Close() error { return nil }
func TestBillingCenterWebSocketEachPhysicalTurnOwnsDurablePermit(t *testing.T) {
	repo, _ := billingCenterTestRepository(t)
	executions := make(map[int]*bc.Execution)
	for turn := 1; turn <= 2; turn++ {
		op := billingCenterTestOperation(t, repo)
		executions[turn] = &bc.Execution{Key: op.Key, Mode: "central", Coordinator: bc.Coordinator{Store: repo, Authority: &dispatchAuthorityStub{op: op}}}
	}
	first := []byte(`{"type":"response.create","model":"gpt-6-sol","input":"one"}`)
	second := []byte(`{"type":"response.create","model":"gpt-6-sol","input":"two"}`)
	client := &billingWSFrames{incoming: [][]byte{second}}
	supplier := &billingWSFrames{wait: true}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, exit := wsv2.Relay(ctx, client, supplier, first, wsv2.RelayOptions{UpstreamDrainTimeout: 20 * time.Millisecond, OnBeforeWrite: func(turn int) error { return bc.BeforeSupplierRequest(bc.WithExecution(ctx, executions[turn])) }})
	require.NotNil(t, exit)
	supplier.mu.Lock()
	require.Len(t, supplier.writes, 2)
	supplier.mu.Unlock()
	require.NotEqual(t, executions[1].Key.OperationID, executions[2].Key.OperationID)
	for _, e := range executions {
		op, err := repo.Get(context.Background(), e.Key)
		require.NoError(t, err)
		require.Equal(t, bc.Dispatched, op.State)
		restarted := &bc.Execution{Key: e.Key, Mode: "central", Coordinator: e.Coordinator}
		require.ErrorIs(t, bc.BeforeSupplierRequest(bc.WithExecution(context.Background(), restarted)), bc.ErrState, "retry/restarted relay cannot send this paid turn twice")
	}
}
