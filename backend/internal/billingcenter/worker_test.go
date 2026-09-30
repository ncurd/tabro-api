package billingcenter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recoveryOutbox struct {
	event       Event
	ackFailures int
	complete    bool
	blocked     bool
	attempts    int
	lastCode    string
}

func (s *recoveryOutbox) Claim(context.Context, time.Duration, int) ([]Event, error) {
	if s.complete || s.blocked {
		return nil, nil
	}
	s.attempts++
	s.event.Attempts = s.attempts
	return []Event{s.event}, nil
}
func (s *recoveryOutbox) Complete(context.Context, Event, Reservation) error {
	if s.ackFailures > 0 {
		s.ackFailures--
		return errors.New("database unavailable after remote commit")
	}
	s.complete = true
	return nil
}
func (s *recoveryOutbox) Retry(_ context.Context, _ Event, _ time.Duration, code string, permanent bool) error {
	s.lastCode = code
	s.blocked = permanent
	return nil
}

type idempotentCenter struct {
	received []Event
	charged  map[string]bool
	fail     error
}

func (s *idempotentCenter) Deliver(_ context.Context, event Event) (Reservation, error) {
	s.received = append(s.received, event)
	if s.fail != nil {
		return Reservation{}, s.fail
	}
	s.charged[event.ID] = true
	return testReservation("settled", 3), nil
}

func TestOutboxReplaysSameEventWhenRemoteCommitSucceedsButAcknowledgementFails(t *testing.T) {
	event, err := NewSettlement(testKey(), "reservation", SettleRequest{TransitionRequest: TransitionRequest{EventID: "stable-event", ExpectedVersion: 2}, UsageItemID: "usage-item", Usage: map[string]Decimal{"input": "100"}, EvidenceReference: "usage:1", UsageComplete: true})
	require.NoError(t, err)
	store := &recoveryOutbox{event: event, ackFailures: 1}
	center := &idempotentCenter{charged: map[string]bool{}}
	worker, err := NewWorker(store, center, WorkerConfig{})
	require.NoError(t, err)
	require.Error(t, worker.RunOnce(context.Background()))
	require.False(t, store.complete)
	require.NoError(t, worker.RunOnce(context.Background()))
	require.True(t, store.complete)
	require.Len(t, center.received, 2)
	require.Equal(t, center.received[0].ID, center.received[1].ID)
	require.Equal(t, string(center.received[0].Body), string(center.received[1].Body))
	require.Len(t, center.charged, 1)
}

func TestOutboxRetriesUnknownOutcomeAndBlocksPermanentRejectionWithoutCompleting(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		blocked bool
	}{
		{"timeout", &RemoteError{Retryable: true, UnknownOutcome: true}, false},
		{"forbidden", &RemoteError{Status: 403}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &recoveryOutbox{event: Event{Key: testKey(), ID: "event", Kind: SettleEvent, ReservationID: "reservation"}}
			center := &idempotentCenter{fail: tc.failure}
			worker, err := NewWorker(store, center, WorkerConfig{})
			require.NoError(t, err)
			require.NoError(t, worker.RunOnce(context.Background()))
			require.Equal(t, tc.blocked, store.blocked)
			require.False(t, store.complete)
			require.NotEmpty(t, store.lastCode)
		})
	}
}

func TestWorkerRequiresLeaseLongerThanAttemptAndBoundsBackoff(t *testing.T) {
	_, err := NewWorker(&recoveryOutbox{}, &idempotentCenter{}, WorkerConfig{LeaseDuration: 10 * time.Second, AttemptTimeout: 10 * time.Second})
	require.Error(t, err)
	require.Equal(t, time.Second, retryDelay(1))
	require.Equal(t, 5*time.Minute, retryDelay(1000))
}
