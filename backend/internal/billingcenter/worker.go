package billingcenter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type EventSender interface {
	Deliver(context.Context, Event) (Reservation, error)
}
type WorkerConfig struct {
	PollInterval   time.Duration
	LeaseDuration  time.Duration
	AttemptTimeout time.Duration
	BatchSize      int
}
type Worker struct {
	store  OutboxStore
	sender EventSender
	config WorkerConfig
}

func NewWorker(store OutboxStore, sender EventSender, cfg WorkerConfig) (*Worker, error) {
	if store == nil || sender == nil {
		return nil, errors.New("billing outbox store and sender are required")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = time.Minute
	}
	if cfg.AttemptTimeout <= 0 {
		cfg.AttemptTimeout = 20 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 8
	}
	if cfg.BatchSize > 100 || cfg.AttemptTimeout+5*time.Second >= cfg.LeaseDuration {
		return nil, errors.New("billing lease must exceed delivery timeout and acknowledge margin")
	}
	return &Worker{store: store, sender: sender, config: cfg}, nil
}

// Run stops on context cancellation. Database failures are reported and retried
// on the next tick; callbacks receive only sanitized classifications.
func (w *Worker) Run(ctx context.Context, onError func(error)) {
	ticker := time.NewTicker(w.config.PollInterval)
	defer ticker.Stop()
	for {
		if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) error {
	reconciliationErr := w.reconcile(ctx)
	events, err := w.store.Claim(ctx, w.config.LeaseDuration, w.config.BatchSize)
	if err != nil {
		return errors.Join(reconciliationErr, err)
	}
	var wg sync.WaitGroup
	errorsCh := make(chan error, len(events))
	for _, event := range events {
		wg.Add(1)
		go func(event Event) {
			defer wg.Done()
			if err := w.deliver(ctx, event); err != nil {
				errorsCh <- err
			}
		}(event)
	}
	wg.Wait()
	close(errorsCh)
	var failures []error
	for err := range errorsCh {
		failures = append(failures, err)
	}
	return errors.Join(append(failures, reconciliationErr)...)
}

func (w *Worker) deliver(ctx context.Context, event Event) error {
	attemptCtx, cancel := context.WithTimeout(ctx, w.config.AttemptTimeout)
	remote, err := w.sender.Deliver(attemptCtx, event)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	} // Keep the lease; a surviving worker recovers it.
	if err == nil {
		target := "settled"
		if event.Kind == ReleaseEvent {
			target = "released"
		}
		if remote.OperationID != event.OperationID || remote.ReservationID != event.ReservationID || remote.State != target {
			return w.store.Retry(ctx, event, 0, "unexpected_remote_result", true)
		}
		// If acknowledgement fails, leave the leased event intact. Redelivery of
		// the same event must return the center's original committed result.
		return w.store.Complete(ctx, event, remote)
	}
	permanent := !IsRetryable(err) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)
	code := "delivery_unavailable"
	var re *RemoteError
	if errors.As(err, &re) {
		code = fmt.Sprintf("http_%d", re.Status)
	}
	if permanent {
		code = "delivery_rejected"
	}
	return w.store.Retry(ctx, event, retryDelay(event.Attempts), code, permanent)
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 10 {
		attempt = 10
	}
	delay := time.Second * time.Duration(1<<uint(attempt-1))
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}
