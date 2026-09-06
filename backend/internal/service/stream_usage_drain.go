package service

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// clientDisconnectUsageDrainTimeout bounds how long a request may keep an
// upstream response open after the downstream client can no longer receive
// data. The window is long enough for the provider's terminal usage event in
// normal operation while guaranteeing that a stalled upstream cannot pin the
// handler forever.
const clientDisconnectUsageDrainTimeout = 30 * time.Second

func streamClientContext(c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return nil
	}
	return c.Request.Context()
}

// clientDisconnectUsageDrain switches a streaming handler from forwarding to
// drain-only mode when the client context is canceled or the first downstream
// write fails. Its timer closes the upstream body, which unblocks a synchronous
// Scanner read without an additional reader goroutine.
type clientDisconnectUsageDrain struct {
	body         io.Closer
	timeout      time.Duration
	disconnected atomic.Bool
	timer        *time.Timer
	timedOut     atomic.Bool
	timerMu      sync.Mutex
	stopped      bool
	stopWatcher  func() bool
}

func newClientDisconnectUsageDrain(clientCtx context.Context, body io.Closer) *clientDisconnectUsageDrain {
	return newClientDisconnectUsageDrainWithTimeout(clientCtx, body, clientDisconnectUsageDrainTimeout)
}

func newClientDisconnectUsageDrainWithTimeout(clientCtx context.Context, body io.Closer, timeout time.Duration) *clientDisconnectUsageDrain {
	if timeout <= 0 {
		timeout = clientDisconnectUsageDrainTimeout
	}
	drain := &clientDisconnectUsageDrain{body: body, timeout: timeout}
	if clientCtx != nil {
		if clientCtx.Err() != nil {
			drain.markDisconnected()
		} else {
			drain.stopWatcher = context.AfterFunc(clientCtx, drain.markDisconnected)
		}
	}
	return drain
}

func (d *clientDisconnectUsageDrain) markDisconnected() {
	if d == nil || !d.disconnected.CompareAndSwap(false, true) {
		return
	}
	d.timerMu.Lock()
	defer d.timerMu.Unlock()
	if !d.stopped && d.body != nil {
		d.timer = time.AfterFunc(d.timeout, func() {
			d.timedOut.Store(true)
			_ = d.body.Close()
		})
	}
}

func (d *clientDisconnectUsageDrain) isDisconnected() bool {
	return d != nil && d.disconnected.Load()
}

func (d *clientDisconnectUsageDrain) didTimeOut() bool {
	return d != nil && d.timedOut.Load()
}

func (d *clientDisconnectUsageDrain) stop() {
	if d == nil {
		return
	}
	if d.stopWatcher != nil {
		d.stopWatcher()
	}
	d.timerMu.Lock()
	d.stopped = true
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timerMu.Unlock()
}

// detachedCancelableStreamContext preserves request-scoped values and gives a
// canceled downstream request a bounded grace period in which the provider can
// emit terminal usage. The caller must cancel the returned context after the
// upstream response is fully handled.
func detachedCancelableStreamContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return detachedCancelableStreamContextWithGrace(ctx, clientDisconnectUsageDrainTimeout)
}

func detachedCancelableStreamContextWithGrace(ctx context.Context, grace time.Duration) (context.Context, context.CancelFunc) {
	if grace <= 0 {
		grace = clientDisconnectUsageDrainTimeout
	}
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	upstreamCtx, cancelUpstream := context.WithCancel(base)

	var (
		mu         sync.Mutex
		stopped    bool
		graceTimer *time.Timer
	)
	stopClientWatcher := func() bool { return true }
	if ctx != nil {
		stopClientWatcher = context.AfterFunc(ctx, func() {
			mu.Lock()
			defer mu.Unlock()
			if stopped {
				return
			}
			graceTimer = time.AfterFunc(grace, cancelUpstream)
		})
	}

	cleanup := func() {
		stopClientWatcher()
		mu.Lock()
		stopped = true
		if graceTimer != nil {
			graceTimer.Stop()
		}
		mu.Unlock()
		cancelUpstream()
	}
	return upstreamCtx, cleanup
}
