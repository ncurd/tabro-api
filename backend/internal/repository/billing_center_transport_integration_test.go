//go:build billing_integration

package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	bc "github.com/Wei-Shaw/sub2api/internal/billingcenter"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type dispatchAuthorityStub struct {
	bc.Authority
	op    bc.Operation
	calls atomic.Int32
}

func (s *dispatchAuthorityStub) Dispatch(_ context.Context, key bc.Key, id string, request bc.TransitionRequest) (bc.Reservation, error) {
	s.calls.Add(1)
	return billingCenterTestRemote(s.op, "dispatched", 2), nil
}

func TestBillingCenterProductionHTTPTransportExecutesSupplierOnlyOnce(t *testing.T) {
	repo, _ := billingCenterTestRepository(t)
	op := billingCenterTestOperation(t, repo)
	authority := &dispatchAuthorityStub{op: op}
	execution := &bc.Execution{Key: op.Key, Mode: "central", Coordinator: bc.Coordinator{Store: repo, Authority: authority}}
	var supplierCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { supplierCalls.Add(1); _, _ = io.WriteString(w, "ok") }))
	defer target.Close()
	upstream := NewHTTPUpstream(&config.Config{})
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(bc.WithExecution(context.Background(), execution), http.MethodPost, target.URL, strings.NewReader(`{"model":"gpt-6-sol"}`))
			require.NoError(t, err)
			response, err := upstream.Do(req, "", 1, 20)
			if err == nil {
				success.Add(1)
				response.Body.Close()
			} else {
				require.ErrorIs(t, err, bc.ErrState)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, supplierCalls.Load())
	require.EqualValues(t, 1, success.Load())
	require.EqualValues(t, 1, authority.calls.Load())
	// A process restart gets a fresh in-memory Execution but cannot regain the
	// persisted permit or send to the supplier a second time.
	restarted := &bc.Execution{Key: op.Key, Mode: "central", Coordinator: execution.Coordinator}
	req, err := http.NewRequestWithContext(bc.WithExecution(context.Background(), restarted), http.MethodPost, target.URL, strings.NewReader(`{}`))
	require.NoError(t, err)
	_, err = upstream.Do(req, "", 1, 20)
	require.ErrorIs(t, err, bc.ErrState)
	require.EqualValues(t, 1, supplierCalls.Load())
}

func TestBillingCenterTransportCannotFollowBillableRedirect(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	req, err := http.NewRequestWithContext(bc.WithExecution(context.Background(), &bc.Execution{Mode: "central"}), http.MethodPost, origin.URL, strings.NewReader(`{}`))
	require.NoError(t, err)
	response, err := doBillingAwareUpstream(origin.Client(), req)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusTemporaryRedirect, response.StatusCode)
	require.Zero(t, redirected.Load())
}
